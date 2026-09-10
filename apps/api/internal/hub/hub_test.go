package hub

// Behavioral tests for the in-memory hub: fan-out, slow-client dropping,
// presence accounting, room garbage collection, concurrent publish/subscribe
// safety (run under -race) and shutdown semantics.

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
)

// recv reads one message from ch, failing the test on timeout.
func recv(t *testing.T, ch <-chan []byte, within time.Duration) ([]byte, bool) {
	t.Helper()
	select {
	case msg, ok := <-ch:
		return msg, ok
	case <-time.After(within):
		t.Fatalf("timed out after %s waiting for a hub message", within)
		return nil, false
	}
}

// recvUntil reads messages until one of the wanted type arrives (skipping
// presence noise), returning its decoded data map.
func recvUntil(t *testing.T, ch <-chan []byte, wantType string, within time.Duration) map[string]any {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				t.Fatalf("hub channel closed while waiting for %q", wantType)
			}
			var env struct {
				Type string         `json:"type"`
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(msg, &env); err != nil {
				t.Fatalf("decode %s: %v", msg, err)
			}
			if env.Type == wantType {
				return env.Data
			}
		case <-deadline:
			t.Fatalf("timed out after %s waiting for %q", within, wantType)
		}
	}
}

// recvClosed drains any buffered messages and reports whether the channel got
// closed within the deadline.
func recvClosed(t *testing.T, ch <-chan []byte, within time.Duration) bool {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return true
			}
		case <-deadline:
			t.Fatalf("channel not closed within %s", within)
			return false
		}
	}
}

func bidEvent(auctionID string, seq int) auction.Event {
	return auction.Event{
		Type:      auction.EventBidPlaced,
		AuctionID: auctionID,
		Data: map[string]any{
			"auction_id":   auctionID,
			"bid_id":       "bid-1",
			"bidder_name":  "Ada",
			"amount_cents": int64(1100),
			"seq":          seq,
		},
	}
}

func TestFanOutToAllSubscribers(t *testing.T) {
	h := New()
	const room = "auction-1"

	subs := make([]chan []byte, 5)
	unsubs := make([]func(), 5)
	for i := range subs {
		subs[i], unsubs[i] = h.Subscribe(room)
	}

	h.Publish(bidEvent(room, 1))

	for i := range subs {
		data := recvUntil(t, subs[i], auction.EventBidPlaced, 2*time.Second)
		if data["auction_id"] != room {
			t.Errorf("sub %d: auction_id = %v, want %q", i, data["auction_id"], room)
		}
		if got := data["amount_cents"]; got != float64(1100) {
			t.Errorf("sub %d: amount_cents = %v, want 1100", i, got)
		}
		if got := data["bidder_name"]; got != "Ada" {
			t.Errorf("sub %d: bidder_name = %v, want Ada", i, got)
		}
	}
	if n := h.Watchers(room); n != 5 {
		t.Errorf("Watchers = %d, want 5", n)
	}
	for _, unsub := range unsubs {
		unsub()
	}
}

func TestSlowSubscriberDroppedWithoutBlockingOthers(t *testing.T) {
	h := New()
	const room = "auction-2"

	slow, _ := h.Subscribe(room) // never drained: its buffer fills up
	fast, unsubFast := h.Subscribe(room)

	var mu sync.Mutex
	var seen []map[string]any
	fastDone := make(chan struct{})
	go func() {
		defer close(fastDone)
		for msg := range fast {
			var env struct {
				Type string         `json:"type"`
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(msg, &env); err != nil {
				continue
			}
			mu.Lock()
			seen = append(seen, env.Data)
			mu.Unlock()
		}
	}()

	// 100 publishes against a subscriber that never reads: the slow channel
	// (buffer 16) must overflow and be dropped, and Publish must never block.
	// Batches of 10 wait for the collector to keep pace so the FAST subscriber
	// never overflows; only the silent one is dropped.
	const total = 100
	for i := 0; i < total; i++ {
		h.Publish(bidEvent(room, i))
		if (i+1)%10 == 0 {
			seq := i
			waitFor(t, 5*time.Second, func() bool {
				mu.Lock()
				defer mu.Unlock()
				for _, d := range seen {
					if d["seq"] == float64(seq) {
						return true
					}
				}
				return false
			})
		}
	}

	// The fast subscriber receives every event plus the presence.update
	// announcing the slow client's departure.
	waitFor(t, 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, d := range seen {
			if d["seq"] == float64(total-1) {
				return true
			}
		}
		return false
	})

	if !recvClosed(t, slow, 5*time.Second) {
		t.Fatal("slow subscriber channel should have been closed after drop")
	}
	if n := h.Watchers(room); n != 1 {
		t.Errorf("Watchers after drop = %d, want 1", n)
	}

	mu.Lock()
	defer mu.Unlock()
	hasPresence := false
	for _, d := range seen {
		if d["watchers"] == float64(1) {
			hasPresence = true
		}
	}
	if !hasPresence {
		t.Errorf("fast subscriber never saw presence.update watchers=1 after the drop; saw %v", seen)
	}

	unsubFast()
	<-fastDone
}

func TestConcurrentPublishAndSubscribe(t *testing.T) {
	h := New()
	const room = "stress-room"

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Subscribers churning join/leave while publishers hammer the room.
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				ch, unsub := h.Subscribe(room)
				select { // opportunistically drain whatever arrived
				case <-ch:
				default:
				}
				unsub()
			}
		}()
	}

	for p := 0; p < 4; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				h.Publish(bidEvent(room, i))
			}
		}()
	}

	// Publishers complete quickly; the churn goroutines only stop on close(stop).
	// Wait for publishes by launching them in the same WaitGroup: stop the
	// churners once enough time has passed, then require everything to finish
	// (a Publish that blocked on a slow subscriber would deadlock here and trip
	// the test timeout).
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	if n := h.Watchers(room); n != 0 {
		t.Errorf("Watchers after churn = %d, want 0", n)
	}
	h.Publish(bidEvent(room, 0)) // must not panic on a garbage-collected room
}

func TestPresenceCountsAndBroadcasts(t *testing.T) {
	h := New()
	const room = "presence-room"

	ch1, unsub1 := h.Subscribe(room)
	if data := recvUntil(t, ch1, auction.EventPresenceUpdate, 2*time.Second); data["watchers"] != float64(1) {
		t.Errorf("first join presence watchers = %v, want 1", data["watchers"])
	}
	if n := h.Watchers(room); n != 1 {
		t.Errorf("Watchers = %d, want 1", n)
	}

	ch2, unsub2 := h.Subscribe(room)
	if data := recvUntil(t, ch1, auction.EventPresenceUpdate, 2*time.Second); data["watchers"] != float64(2) {
		t.Errorf("existing subscriber join presence watchers = %v, want 2", data["watchers"])
	}
	if data := recvUntil(t, ch2, auction.EventPresenceUpdate, 2*time.Second); data["watchers"] != float64(2) {
		t.Errorf("joining subscriber own presence watchers = %v, want 2", data["watchers"])
	}

	unsub1()
	if data := recvUntil(t, ch2, auction.EventPresenceUpdate, 2*time.Second); data["watchers"] != float64(1) {
		t.Errorf("leave presence watchers = %v, want 1", data["watchers"])
	}
	if n := h.Watchers(room); n != 1 {
		t.Errorf("Watchers after leave = %d, want 1", n)
	}

	unsub2()
	h.mu.RLock()
	rooms := len(h.rooms)
	h.mu.RUnlock()
	if rooms != 0 {
		t.Errorf("rooms after everyone left = %d, want 0 (empty-room GC)", rooms)
	}
}

func TestUnsubscribeIsIdempotent(t *testing.T) {
	h := New()
	ch, unsub := h.Subscribe("once-only")
	unsub()
	unsub() // must not double-close the channel (would panic)
	if !recvClosed(t, ch, 2*time.Second) {
		t.Fatal("expected channel to be closed after unsubscribe")
	}
}

func TestCloseDropsEveryoneAndRefusesNewSubscribers(t *testing.T) {
	h := New()
	ch1, _ := h.Subscribe("room-a")
	ch2, _ := h.Subscribe("room-b")

	h.Close()

	if !recvClosed(t, ch1, 2*time.Second) || !recvClosed(t, ch2, 2*time.Second) {
		t.Fatal("all subscriber channels should close on Hub.Close")
	}

	late, lateUnsub := h.Subscribe("room-a")
	if !recvClosed(t, late, 2*time.Second) {
		t.Fatal("subscribing after Close should yield an already-closed channel")
	}
	lateUnsub()

	h.Publish(bidEvent("room-a", 0)) // no panic
	if n := h.Watchers("room-a"); n != 0 {
		t.Errorf("Watchers after Close = %d, want 0", n)
	}
}

func TestPublishToRoomWithNoSubscribersIsNoop(t *testing.T) {
	h := New()
	h.Publish(bidEvent("ghost-room", 0))
	if n := h.Watchers("ghost-room"); n != 0 {
		t.Errorf("Watchers = %d, want 0", n)
	}
}

// waitFor polls cond until it holds or the deadline expires.
func waitFor(t *testing.T, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", within)
}
