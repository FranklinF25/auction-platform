// Package hub is the driven adapter that implements the domain's
// auction.EventPublisher port: an in-memory registry of per-auction rooms that
// fans domain events out to connected WebSocket clients as raw JSON bytes.
//
// Concurrency model (docs/PRD.md): a RWMutex guards the room registry; each
// subscriber owns a buffered channel (cap sendBuffer). Publishing is
// non-blocking — a subscriber whose buffer is full is a slow client and gets
// dropped (its channel closed) rather than stalling the room. The hub is
// deliberately single-instance; the scale-out path (Redis pub/sub) is a
// documented non-goal.
//
// Hexagonal rule: the hub is an adapter, so it imports the auction domain —
// never the other way around.
package hub

import (
	"encoding/json"
	"sync"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
)

// Compile-time proof that Hub satisfies the domain event port.
var _ auction.EventPublisher = (*Hub)(nil)

// sendBuffer bounds how many events may pile up per connection before the
// subscriber counts as slow and is dropped. Must stay in sync with the
// httpapi write pump expectations (events outlive pings, never the reverse).
const sendBuffer = 16

// envelope is the wire shape of every hub message: {"type": ..., "data": ...}.
type envelope struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

// Hub is the room registry. All state is guarded by mu; Publish takes the
// write lock because dropping slow subscribers mutates rooms, while Watchers
// (the hot read path for snapshots) only needs RLock.
type Hub struct {
	mu     sync.RWMutex
	rooms  map[string]*room
	closed bool
}

// room is one auction's set of live subscribers. The room owns its subscriber
// channels: they are closed exactly once, when the subscriber is removed.
type room struct {
	auctionID string
	subs      map[chan []byte]struct{}
}

// New returns an empty hub.
func New() *Hub {
	return &Hub{rooms: map[string]*room{}}
}

// Subscribe registers a watcher for auctionID and returns its event feed plus
// an idempotent unsubscribe function. Joining broadcasts a presence.update
// with the new watcher count to the room (the joiner included). After Close,
// Subscribe returns an already-closed channel so late joiners shut down too.
func (h *Hub) Subscribe(auctionID string) (chan []byte, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		ch := make(chan []byte)
		close(ch)
		return ch, func() {}
	}

	r := h.rooms[auctionID]
	if r == nil {
		r = &room{auctionID: auctionID, subs: map[chan []byte]struct{}{}}
		h.rooms[auctionID] = r
	}
	ch := make(chan []byte, sendBuffer)
	r.subs[ch] = struct{}{}
	h.broadcastLocked(r, presenceEvent(auctionID, len(r.subs)))

	unsubscribed := false
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if unsubscribed {
			return
		}
		unsubscribed = true
		if r := h.rooms[auctionID]; r != nil {
			h.dropLocked(r, ch)
		}
	}
}

// Publish implements auction.EventPublisher. The event is marshaled once and
// the bytes are fanned out to the auction's room without ever blocking: a
// full buffer means a slow client, which is dropped. Events for auctions with
// no watchers vanish.
func (h *Hub) Publish(evt auction.Event) {
	msg, err := json.Marshal(envelope{Type: evt.Type, Data: evt.Data})
	if err != nil {
		return // payloads are JSON-safe by the event contract
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.rooms[evt.AuctionID]; r != nil {
		h.deliverLocked(r, msg)
	}
}

// Watchers returns how many connections are currently watching auctionID.
func (h *Hub) Watchers(auctionID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if r := h.rooms[auctionID]; r != nil {
		return len(r.subs)
	}
	return 0
}

// Close shuts the hub down: every subscriber channel is closed (write pumps
// see it and send their close frame) and the registry is emptied. Late
// subscribers get an already-closed feed.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.closed = true
	for _, r := range h.rooms {
		for ch := range r.subs {
			close(ch)
		}
	}
	h.rooms = map[string]*room{}
}

// dropLocked removes ch from r and closes it (exactly once — presence in the
// map is the close token), removes the room when it empties, and tells the
// remaining watchers the new count.
func (h *Hub) dropLocked(r *room, ch chan []byte) {
	if _, ok := r.subs[ch]; !ok {
		return
	}
	delete(r.subs, ch)
	close(ch)
	if len(r.subs) == 0 {
		delete(h.rooms, r.auctionID)
		return
	}
	h.broadcastLocked(r, presenceEvent(r.auctionID, len(r.subs)))
}

// deliverLocked offers msg to every subscriber; full buffers are collected
// and dropped after the loop so the map is not mutated mid-range.
func (h *Hub) deliverLocked(r *room, msg []byte) {
	var slow []chan []byte
	for ch := range r.subs {
		select {
		case ch <- msg:
		default:
			slow = append(slow, ch)
		}
	}
	for _, ch := range slow {
		h.dropLocked(r, ch)
	}
}

// broadcastLocked marshals and delivers evt to a room that is known to exist.
func (h *Hub) broadcastLocked(r *room, evt auction.Event) {
	msg, err := json.Marshal(envelope{Type: evt.Type, Data: evt.Data})
	if err != nil {
		return
	}
	h.deliverLocked(r, msg)
}

func presenceEvent(auctionID string, watchers int) auction.Event {
	return auction.Event{
		Type:      auction.EventPresenceUpdate,
		AuctionID: auctionID,
		Data: map[string]any{
			"auction_id": auctionID,
			"watchers":   watchers,
		},
	}
}
