package httpapi

// End-to-end WebSocket tests through the real router and an in-memory hub:
// join snapshot + presence, live bid fan-out triggered by a REST POST, soft
// close extension broadcast, unknown-auction 404 before upgrade, and the
// public /api/config bootstrap endpoint.

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
	"github.com/FranklinF25/auction-platform/apps/api/internal/auth"
	"github.com/FranklinF25/auction-platform/apps/api/internal/hub"
)

// newTestServer starts an httptest server for handler.
func newTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

// wsDial opens a guest WebSocket connection (no cookie).
func wsDial(t *testing.T, rawURL string) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, resp, err := dialer.Dial(rawURL, nil)
	if err != nil {
		body := ""
		if resp != nil && resp.Body != nil {
			b, _ := io.ReadAll(resp.Body)
			body = string(b)
		}
		t.Fatalf("dial %s: %v (resp: %s)", rawURL, err, body)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// readWS reads one event with a deadline and returns its type and data map.
func readWS(t *testing.T, conn *websocket.Conn, within time.Duration) (string, map[string]any) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(within)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read ws message: %v", err)
	}
	var env struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return env.Type, env.Data
}

// readWSUntil reads events until one of wantType arrives, skipping others.
func readWSUntil(t *testing.T, conn *websocket.Conn, wantType string, within time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		typ, data := readWS(t, conn, time.Until(deadline))
		if typ == wantType {
			return data
		}
	}
}

// wsTime parses an RFC3339 timestamp coming off the wire.
func wsTime(t *testing.T, v any) time.Time {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Fatalf("expected an RFC3339 string, got %T (%v)", v, v)
	}
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q as RFC3339: %v", s, err)
	}
	return ts
}

func TestWSUnknownAuctionAnswersPlainJSONWithoutUpgrade(t *testing.T) {
	clock := &httpFakeClock{t: httpBaseTime}
	handler, _ := newTestHandler(t, clock)
	ts := newTestServer(t, handler)

	resp, err := http.Get(ts.URL + "/ws/auctions/00000000-0000-0000-0000-000000000999")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q, want JSON (no WS upgrade)", ct)
	}
	var e errResp
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if e.Error.Code != "not_found" {
		t.Errorf("code = %q, want not_found", e.Error.Code)
	}
}

func TestWSJoinPresenceAndLiveBids(t *testing.T) {
	clock := &httpFakeClock{t: httpBaseTime}
	handler, repo := newTestHandler(t, clock)
	ts := newTestServer(t, handler)
	base := ts.URL

	sellerClient, _ := registerUser(t, base, "ws-seller@example.com", "Sue")
	bidderClient, bidderID := registerUser(t, base, "ws-bidder@example.com", "Ada")
	repo.setName(bidderID, "Ada")

	created := createAuction(t, sellerClient, base)
	wsURL := "ws://" + strings.TrimPrefix(base, "http://") + "/ws/auctions/" + created.ID

	// Guest connections are allowed: the feed is read-only.
	connA := wsDial(t, wsURL)

	state := readWSUntil(t, connA, auction.EventAuctionState, 5*time.Second)
	if state["auction_id"] != created.ID {
		t.Errorf("state auction_id = %v, want %q", state["auction_id"], created.ID)
	}
	if state["status"] != "active" {
		t.Errorf("state status = %v, want active", state["status"])
	}
	if state["current_price_cents"] != float64(1000) {
		t.Errorf("state current_price_cents = %v, want 1000", state["current_price_cents"])
	}
	if state["bid_count"] != float64(0) {
		t.Errorf("state bid_count = %v, want 0", state["bid_count"])
	}
	if state["reserve_met"] != true {
		t.Errorf("state reserve_met = %v, want true", state["reserve_met"])
	}
	if state["watchers"] != float64(1) {
		t.Errorf("state watchers = %v, want 1", state["watchers"])
	}
	if !wsTime(t, state["ends_at"]).Equal(created.EndsAt) {
		t.Errorf("state ends_at = %v, want %v", state["ends_at"], created.EndsAt)
	}
	if !wsTime(t, state["server_now"]).Equal(httpBaseTime) {
		t.Errorf("state server_now = %v, want %v", state["server_now"], httpBaseTime)
	}

	if data := readWSUntil(t, connA, auction.EventPresenceUpdate, 5*time.Second); data["watchers"] != float64(1) {
		t.Errorf("connA join presence watchers = %v, want 1", data["watchers"])
	}

	connB := wsDial(t, wsURL)
	stateB := readWSUntil(t, connB, auction.EventAuctionState, 5*time.Second)
	if stateB["watchers"] != float64(2) {
		t.Errorf("connB state watchers = %v, want 2", stateB["watchers"])
	}
	if data := readWSUntil(t, connB, auction.EventPresenceUpdate, 5*time.Second); data["watchers"] != float64(2) {
		t.Errorf("connB own presence watchers = %v, want 2", data["watchers"])
	}
	if data := readWSUntil(t, connA, auction.EventPresenceUpdate, 5*time.Second); data["watchers"] != float64(2) {
		t.Errorf("connA presence watchers = %v, want 2", data["watchers"])
	}

	// A REST bid fans out to every watcher with the pinned payload.
	var placed struct {
		BidID string `json:"bid_id"`
	}
	resp, _ := doJSON(t, bidderClient, http.MethodPost, base+"/api/auctions/"+created.ID+"/bids",
		map[string]any{"amount_cents": 1100}, &placed)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("post bid: status %d", resp.StatusCode)
	}

	for name, conn := range map[string]*websocket.Conn{"connA": connA, "connB": connB} {
		data := readWSUntil(t, conn, auction.EventBidPlaced, 5*time.Second)
		if data["bid_id"] != placed.BidID {
			t.Errorf("%s bid_id = %v, want %q", name, data["bid_id"], placed.BidID)
		}
		if data["auction_id"] != created.ID {
			t.Errorf("%s auction_id = %v", name, data["auction_id"])
		}
		if data["bidder_name"] != "Ada" {
			t.Errorf("%s bidder_name = %v, want Ada", name, data["bidder_name"])
		}
		if data["amount_cents"] != float64(1100) {
			t.Errorf("%s amount_cents = %v, want 1100", name, data["amount_cents"])
		}
		if !wsTime(t, data["ends_at"]).Equal(created.EndsAt) {
			t.Errorf("%s ends_at = %v, want unchanged %v", name, data["ends_at"], created.EndsAt)
		}
		wsTime(t, data["server_now"]) // must be present and RFC3339
	}

	// Soft close: a bid 20s before the end extends the deadline for everyone.
	clock.Set(created.EndsAt.Add(-20 * time.Second))
	defer clock.Set(httpBaseTime)
	resp, _ = doJSON(t, bidderClient, http.MethodPost, base+"/api/auctions/"+created.ID+"/bids",
		map[string]any{"amount_cents": 1300}, &placed)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("post soft-close bid: status %d", resp.StatusCode)
	}

	wantEnds := created.EndsAt.Add(40 * time.Second) // now+60s = ends-20s+60s
	for name, conn := range map[string]*websocket.Conn{"connA": connA, "connB": connB} {
		data := readWSUntil(t, conn, auction.EventBidPlaced, 5*time.Second)
		if data["amount_cents"] != float64(1300) {
			t.Errorf("%s soft-close bid amount = %v, want 1300", name, data["amount_cents"])
		}
		if !wsTime(t, data["ends_at"]).Equal(wantEnds) {
			t.Errorf("%s bid.placed ends_at = %v, want extended %v", name, data["ends_at"], wantEnds)
		}
		ext := readWSUntil(t, conn, auction.EventAuctionExtended, 5*time.Second)
		if ext["auction_id"] != created.ID {
			t.Errorf("%s extended auction_id = %v", name, ext["auction_id"])
		}
		if !wsTime(t, ext["new_ends_at"]).Equal(wantEnds) {
			t.Errorf("%s new_ends_at = %v, want %v", name, ext["new_ends_at"], wantEnds)
		}
		wsTime(t, ext["server_now"])
	}
}

// newConfigTestHandler builds a handler with only Config varying.
func newConfigTestHandler(cfg Config) http.Handler {
	clock := &httpFakeClock{t: httpBaseTime}
	authSvc := auth.NewService(
		&fakeUserRepo{},
		&fakeSessionStore{sessions: map[string]auth.Session{}},
		auth.BcryptHasher{Cost: bcrypt.MinCost},
		clock,
	)
	return New(authSvc, auction.NewService(newFakeAuctionRepo(), clock, nil),
		hub.New(), clock.Now,
		slog.New(slog.NewTextHandler(io.Discard, nil)), cfg)
}

func TestWSRoomsAreIsolated(t *testing.T) {
	clock := &httpFakeClock{t: httpBaseTime}
	handler, repo := newTestHandler(t, clock)
	ts := newTestServer(t, handler)
	base := ts.URL

	sellerClient, _ := registerUser(t, base, "iso-seller@example.com", "Sue")
	bidderClient, bidderID := registerUser(t, base, "iso-bidder@example.com", "Ada")
	repo.setName(bidderID, "Ada")

	a := createAuction(t, sellerClient, base)
	b := createAuction(t, sellerClient, base)
	wsBase := "ws://" + strings.TrimPrefix(base, "http://")

	connA := wsDial(t, wsBase+"/ws/auctions/"+a.ID)
	connB := wsDial(t, wsBase+"/ws/auctions/"+b.ID)
	_ = readWSUntil(t, connA, auction.EventAuctionState, 5*time.Second)
	_ = readWSUntil(t, connB, auction.EventAuctionState, 5*time.Second)
	// Drain each connection's own buffered join presence so the silence check
	// below only sees genuinely new traffic.
	_ = readWSUntil(t, connA, auction.EventPresenceUpdate, 5*time.Second)
	_ = readWSUntil(t, connB, auction.EventPresenceUpdate, 5*time.Second)

	var placed struct {
		BidID string `json:"bid_id"`
	}
	resp, _ := doJSON(t, bidderClient, http.MethodPost, base+"/api/auctions/"+a.ID+"/bids",
		map[string]any{"amount_cents": 1100}, &placed)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("post bid: status %d", resp.StatusCode)
	}

	// A's room receives the bid...
	data := readWSUntil(t, connA, auction.EventBidPlaced, 5*time.Second)
	if data["bid_id"] != placed.BidID {
		t.Errorf("connA bid_id = %v, want %q", data["bid_id"], placed.BidID)
	}
	// ...and B's room stays silent: no cross-room leakage.
	if err := connB.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, _, err := connB.ReadMessage(); err == nil {
		t.Fatal("connB received an event for another auction's room")
	}
}

func TestConfigEndpoint(t *testing.T) {
	t.Run("defaults to localhost origin without auth", func(t *testing.T) {
		ts := newTestServer(t, newConfigTestHandler(Config{}))

		var cfg struct {
			WSOrigin string `json:"ws_origin"`
		}
		resp, _ := doJSON(t, &http.Client{}, http.MethodGet, ts.URL+"/api/config", nil, &cfg)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if cfg.WSOrigin != "http://localhost:8080" {
			t.Errorf("ws_origin = %q, want default http://localhost:8080", cfg.WSOrigin)
		}
	})

	t.Run("echoes the configured public origin", func(t *testing.T) {
		ts := newTestServer(t, newConfigTestHandler(Config{PublicOrigin: "https://api.example.com"}))

		var cfg struct {
			WSOrigin string `json:"ws_origin"`
		}
		resp, _ := doJSON(t, &http.Client{}, http.MethodGet, ts.URL+"/api/config", nil, &cfg)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if cfg.WSOrigin != "https://api.example.com" {
			t.Errorf("ws_origin = %q, want https://api.example.com", cfg.WSOrigin)
		}
	})
}
