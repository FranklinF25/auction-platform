package httpapi

// The WebSocket endpoint: GET /ws/auctions/{id}. Events over WS, commands over
// REST (docs/PRD.md "Real-time design"): this socket is a pure read-only
// fan-out channel. There are no client→server messages — the read pump only
// discards input and services ping/pong keepalive (gorilla chat pattern).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
)

// Keepalive timings: the server pings every 30s and every write (event or
// ping) gets a 10s deadline. The read deadline (60s, reset on pong) exceeds
// the ping period, so a healthy client that answers pings never times out.
const (
	wsWriteWait  = 10 * time.Second
	wsPingPeriod = 30 * time.Second
	wsPongWait   = 60 * time.Second
	wsReadLimit  = 512 // client messages are discarded; cap them anyway
)

// wsCheckOrigin accepts any http/https origin — deliberately permissive but
// explicit. Rationale: this endpoint is a read-only event feed (bids go over
// REST; no privileged client→server message exists), the session cookie is
// not consulted for anything a guest cannot already read, and the deployment
// is a single-instance portfolio demo. Non-http(s) schemes are rejected so a
// crafted origin cannot smuggle an unexpected scheme through.
func wsCheckOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser clients (curl, tests) send no Origin
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// auctionStateEvent is the full-snapshot message sent on join, exactly per
// the pinned event contract (snake_case, RFC3339 timestamps, server_now).
type auctionStateEvent struct {
	Type string `json:"type"`
	Data struct {
		AuctionID         string    `json:"auction_id"`
		Status            string    `json:"status"`
		CurrentPriceCents int64     `json:"current_price_cents"`
		BidCount          int       `json:"bid_count"`
		EndsAt            time.Time `json:"ends_at"`
		ServerNow         time.Time `json:"server_now"`
		ReserveMet        bool      `json:"reserve_met"`
		Watchers          int       `json:"watchers"`
	} `json:"data"`
}

// handleWSAuction upgrades a watcher onto an auction's live feed. Guests are
// allowed: the feed carries nothing a guest cannot read over REST.
//
// Join sequence (ordering matters):
//
//  1. Resolve the auction BEFORE upgrading so unknown ids get the uniform
//     JSON 404, not a failed handshake.
//  2. Subscribe to the room FIRST — from here on every event for the auction
//     is buffered for this client.
//  3. Build the auction.state snapshot (service read + watcher count) and
//     send it before forwarding buffered/live events.
//
// Accepted, documented race: a bid committed between steps 2 and 3 appears
// both in the snapshot and as a replayed bid.placed event (duplicate, never
// lost); a presence change may make the snapshot's watcher count momentarily
// stale until the matching presence.update arrives. Clients converge because
// every event carries the full authoritative fields (amount, ends_at,
// server_now) and bid ids are unique.
func (s *Server) handleWSAuction(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !isUUID(id) {
		writeError(w, http.StatusBadRequest, "invalid_id", "auction id must be a UUID")
		return
	}
	if _, err := s.auctions.GetAuction(r.Context(), id); err != nil {
		s.writeDomainError(w, err)
		return
	}

	events, unsubscribe := s.ws.Subscribe(id)
	defer unsubscribe()

	snapshot, err := s.auctionStateEvent(r.Context(), id)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already answered the handshake with an HTTP error
	}

	c := &wsClient{conn: conn, send: events, snapshot: snapshot}
	go c.writePump() // single writer goroutine per connection
	c.readPump()     // blocks until the connection dies; then unsubscribe runs
}

// auctionStateEvent builds the join snapshot from a fresh service read plus
// the room's watcher count.
func (s *Server) auctionStateEvent(ctx context.Context, id string) ([]byte, error) {
	a, err := s.auctions.GetAuction(ctx, id)
	if err != nil {
		return nil, err
	}
	var evt auctionStateEvent
	evt.Type = auction.EventAuctionState
	evt.Data.AuctionID = a.ID
	evt.Data.Status = string(a.Status)
	evt.Data.CurrentPriceCents = a.CurrentPrice()
	evt.Data.BidCount = a.BidCount()
	evt.Data.EndsAt = a.EndsAt
	evt.Data.ServerNow = s.now()
	evt.Data.ReserveMet = a.ReserveMet()
	evt.Data.Watchers = s.ws.Watchers(id)
	return json.Marshal(evt)
}

// wsClient is one upgraded connection: the websocket plus its buffered feed.
type wsClient struct {
	conn     *websocket.Conn
	send     <-chan []byte
	snapshot []byte
}

// readPump discards client messages (the contract has none) and services
// keepalive: the read deadline resets on every pong. It runs in the handler
// goroutine and owns closing the network connection, which unblocks the
// write pump's writes.
func (c *wsClient) readPump() {
	defer c.conn.Close()
	c.conn.SetReadLimit(wsReadLimit)
	_ = c.conn.SetReadDeadline(time.Now().Add(wsPongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return // close frame, keepalive timeout or transport error
		}
	}
}

// writePump is the connection's single writer. It sends the join snapshot
// first, then forwards hub events and periodic pings until the feed closes
// (hub drop/shutdown) or a write fails.
func (c *wsClient) writePump() {
	ticker := time.NewTicker(wsPingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(wsWriteWait))
		_ = c.conn.Close()
	}()

	_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	if err := c.conn.WriteMessage(websocket.TextMessage, c.snapshot); err != nil {
		return
	}

	for {
		select {
		case msg, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if !ok {
				return // dropped by the hub (slow) or hub shutdown
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
