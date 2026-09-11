package httpapi

// M3 close & dashboard endpoints: the seller dashboard (/api/users/me/auctions),
// the purchases dashboard (/api/users/me/purchases) and the post-close winner
// view on GET /api/auctions/{id}. Auctions are closed through the fake repo's
// CloseDue, which applies the real domain close, so detail and list state come
// from the same rules production uses.

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
	"github.com/FranklinF25/auction-platform/apps/api/internal/auth"
	"github.com/FranklinF25/auction-platform/apps/api/internal/hub"
)

// m3Fixture wires a test server with the closed-auction landscape the M3
// tests need:
//
//	sold        won by Ada at 1500 (no reserve)
//	unsold      Ada's 2000 top bid is below the 5000 reserve
//	zerobid     no bids at all
//	running     still active (ends in the future)
//	gracesWin   won by Grace at 1200
//	otherSellers auction created by a second seller (scoping probe)
type m3Fixture struct {
	ts           *httptest.Server
	repo         *fakeAuctionRepo
	clock        *httpFakeClock
	seller       *http.Client
	sellerID     string
	ada          *http.Client
	adaID        string
	grace        *http.Client
	graceID      string
	sold         auctionResp
	unsold       auctionResp
	zerobid      auctionResp
	running      auctionResp
	gracesWin    auctionResp
	otherSellers auctionResp
}

func newM3Fixture(t *testing.T) *m3Fixture {
	t.Helper()
	clock := &httpFakeClock{t: httpBaseTime}
	handler, repo := newTestHandler(t, clock)
	ts := newTestServer(t, handler)
	base := ts.URL

	seller, sellerID := registerUser(t, base, "m3-seller@example.com", "Sue")
	ada, adaID := registerUser(t, base, "m3-ada@example.com", "Ada")
	grace, graceID := registerUser(t, base, "m3-grace@example.com", "Grace")

	create := func(client *http.Client, title string, reserve *int64, durationMinutes int) auctionResp {
		t.Helper()
		body := map[string]any{
			"title":                title,
			"description":          "m3 fixture",
			"starting_price_cents": 1000,
			"min_increment_cents":  100,
			"duration_minutes":     durationMinutes,
		}
		if reserve != nil {
			body["reserve_price_cents"] = *reserve
		}
		var created auctionResp
		resp, raw := doJSON(t, client, http.MethodPost, base+"/api/auctions", body, &created)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create %s: status %d (%s)", title, resp.StatusCode, raw)
		}
		return created
	}

	f := &m3Fixture{
		ts:       ts,
		repo:     repo,
		clock:    clock,
		seller:   seller,
		sellerID: sellerID,
		ada:      ada,
		adaID:    adaID,
		grace:    grace,
		graceID:  graceID,
		sold:     create(seller, "Sold lot", nil, 30),
		unsold:   create(seller, "Reserve hold lot", int64PtrOf(5000), 30),
		zerobid:  create(seller, "Zero bid lot", nil, 30),
		// running outlives the sweep: 120 minutes instead of the fixture's 30.
		running:   create(seller, "Still running lot", nil, 120),
		gracesWin: create(seller, "Graces win", nil, 30),
	}

	// Seed the bid history (names ride with the bids in the fake).
	f.repo.addBid(f.sold.ID, adaID, "Ada", 1500, httpBaseTime.Add(time.Minute))
	f.repo.addBid(f.unsold.ID, adaID, "Ada", 2000, httpBaseTime.Add(time.Minute))
	f.repo.addBid(f.gracesWin.ID, graceID, "Grace", 1200, httpBaseTime.Add(time.Minute))

	// A second seller's auction must never appear in Sue's dashboard.
	otherSeller, _ := registerUser(t, base, "m3-other@example.com", "Otto")
	f.otherSellers = create(otherSeller, "Other sellers lot", nil, 30)

	// Advance past ends_at and run the real close sweep over the fake store.
	f.clock.Set(f.sold.EndsAt.Add(time.Minute))
	if _, err := f.repo.CloseDue(context.Background(), f.clock.Now()); err != nil {
		t.Fatalf("CloseDue: %v", err)
	}
	return f
}

func int64PtrOf(v int64) *int64 { return &v }

func TestSellerDashboard(t *testing.T) {
	f := newM3Fixture(t)
	base := f.ts.URL

	t.Run("requires authentication", func(t *testing.T) {
		var e errResp
		resp, _ := doJSON(t, &http.Client{}, http.MethodGet, base+"/api/users/me/auctions", nil, &e)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
		if e.Error.Code != "unauthorized" {
			t.Errorf("code = %q, want unauthorized", e.Error.Code)
		}
	})

	t.Run("returns only the sellers auctions in the shared envelope", func(t *testing.T) {
		var lr listResp
		resp, _ := doJSON(t, f.seller, http.MethodGet, base+"/api/users/me/auctions", nil, &lr)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if lr.Total != 5 || len(lr.Items) != 5 {
			t.Fatalf("total=%d items=%d, want 5/5 (sold, unsold, zerobid, running, gracesWin)", lr.Total, len(lr.Items))
		}
		ids := map[string]bool{}
		for _, it := range lr.Items {
			if it.SellerID != f.sellerID {
				t.Errorf("item %s seller_id = %q, want %q", it.ID, it.SellerID, f.sellerID)
			}
			ids[it.ID] = true
		}
		for _, want := range []string{f.sold.ID, f.unsold.ID, f.zerobid.ID, f.running.ID, f.gracesWin.ID} {
			if !ids[want] {
				t.Errorf("auction %s missing from the seller dashboard", want)
			}
		}
		if ids[f.otherSellers.ID] {
			t.Error("another seller's auction leaked into the dashboard")
		}
		if lr.Page != 1 || lr.PageSize != 20 {
			t.Errorf("page/page_size = %d/%d, want the shared defaults 1/20", lr.Page, lr.PageSize)
		}
	})

	t.Run("status filter scopes to closed auctions", func(t *testing.T) {
		var lr listResp
		resp, _ := doJSON(t, f.seller, http.MethodGet, base+"/api/users/me/auctions?status=closed", nil, &lr)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		// sold, unsold, zerobid, gracesWin closed; running stays active.
		if lr.Total != 4 {
			t.Fatalf("closed total = %d, want 4", lr.Total)
		}
		for _, it := range lr.Items {
			if it.Status != "closed" {
				t.Errorf("item %s status = %q under status=closed", it.ID, it.Status)
			}
		}
	})

	t.Run("query filter still matches titles", func(t *testing.T) {
		var lr listResp
		doJSON(t, f.seller, http.MethodGet, base+"/api/users/me/auctions?q=SOLD", nil, &lr)
		if lr.Total != 1 || len(lr.Items) != 1 || lr.Items[0].ID != f.sold.ID {
			t.Fatalf("q=SOLD: total=%d items=%+v, want only the sold lot", lr.Total, lr.Items)
		}
	})
}

func TestPurchasesDashboard(t *testing.T) {
	f := newM3Fixture(t)
	base := f.ts.URL

	t.Run("requires authentication", func(t *testing.T) {
		var e errResp
		resp, _ := doJSON(t, &http.Client{}, http.MethodGet, base+"/api/users/me/purchases", nil, &e)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("lists only the users wins", func(t *testing.T) {
		var lr listResp
		resp, _ := doJSON(t, f.ada, http.MethodGet, base+"/api/users/me/purchases", nil, &lr)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if lr.Total != 1 || len(lr.Items) != 1 {
			t.Fatalf("total=%d items=%d, want 1/1", lr.Total, len(lr.Items))
		}
		got := lr.Items[0]
		if got.ID != f.sold.ID {
			t.Fatalf("purchase = %s, want the sold lot %s", got.ID, f.sold.ID)
		}
		if got.CurrentPriceCents != 1500 {
			t.Errorf("current_price_cents = %d, want the winning 1500", got.CurrentPriceCents)
		}
		if got.Status != "closed" {
			t.Errorf("status = %q, want closed", got.Status)
		}
	})

	t.Run("excludes unsold-reserve, zero-bid and other users wins", func(t *testing.T) {
		var lr listResp
		doJSON(t, f.ada, http.MethodGet, base+"/api/users/me/purchases", nil, &lr)
		ids := map[string]bool{}
		for _, it := range lr.Items {
			ids[it.ID] = true
		}
		if ids[f.unsold.ID] {
			t.Error("reserve-not-met auction appeared in purchases")
		}
		if ids[f.zerobid.ID] {
			t.Error("zero-bid auction appeared in purchases")
		}
		if ids[f.gracesWin.ID] {
			t.Error("another user's win appeared in purchases")
		}
	})

	t.Run("the other winner sees only their own win", func(t *testing.T) {
		var lr listResp
		resp, _ := doJSON(t, f.grace, http.MethodGet, base+"/api/users/me/purchases", nil, &lr)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if lr.Total != 1 || len(lr.Items) != 1 || lr.Items[0].ID != f.gracesWin.ID {
			t.Fatalf("grace purchases = %+v (total %d), want only her win", lr.Items, lr.Total)
		}
	})

	t.Run("the seller has no purchases", func(t *testing.T) {
		var lr listResp
		resp, _ := doJSON(t, f.seller, http.MethodGet, base+"/api/users/me/purchases", nil, &lr)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if lr.Total != 0 || len(lr.Items) != 0 {
			t.Fatalf("seller purchases = %+v (total %d), want none", lr.Items, lr.Total)
		}
	})
}

func TestClosedAuctionDetailWinnerView(t *testing.T) {
	f := newM3Fixture(t)
	base := f.ts.URL

	type detailResp struct {
		auctionResp
		WinnerName *string `json:"winner_name"`
		YouWon     bool    `json:"you_won"`
	}

	get := func(client *http.Client, id string) (detailResp, []byte) {
		t.Helper()
		var got detailResp
		resp, raw := doJSON(t, client, http.MethodGet, base+"/api/auctions/"+id, nil, &got)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status %d", id, resp.StatusCode)
		}
		return got, raw
	}

	t.Run("guest sees the winner name but never you_won", func(t *testing.T) {
		got, _ := get(&http.Client{}, f.sold.ID)
		if got.WinnerName == nil || *got.WinnerName != "Ada" {
			t.Errorf("winner_name = %v, want Ada", got.WinnerName)
		}
		if got.YouWon {
			t.Error("you_won = true for a guest, want false")
		}
		if got.Status != "closed" {
			t.Errorf("status = %q, want closed", got.Status)
		}
	})

	t.Run("the winner sees you_won", func(t *testing.T) {
		got, _ := get(f.ada, f.sold.ID)
		if got.WinnerName == nil || *got.WinnerName != "Ada" {
			t.Errorf("winner_name = %v, want Ada", got.WinnerName)
		}
		if !got.YouWon {
			t.Error("you_won = false for the winner, want true")
		}
	})

	t.Run("another authenticated bidder sees you_won false", func(t *testing.T) {
		got, _ := get(f.grace, f.sold.ID)
		if got.WinnerName == nil || *got.WinnerName != "Ada" {
			t.Errorf("winner_name = %v, want Ada", got.WinnerName)
		}
		if got.YouWon {
			t.Error("you_won = true for a non-winner, want false")
		}
	})

	t.Run("unsold closed auction has no winner", func(t *testing.T) {
		for _, id := range []string{f.unsold.ID, f.zerobid.ID} {
			got, _ := get(f.ada, id)
			if got.WinnerName != nil {
				t.Errorf("%s: winner_name = %v, want nil", id, *got.WinnerName)
			}
			if got.YouWon {
				t.Errorf("%s: you_won = true on an unsold auction, want false", id)
			}
		}
	})

	t.Run("active auction has no winner fields", func(t *testing.T) {
		got, _ := get(f.ada, f.running.ID)
		if got.WinnerName != nil {
			t.Errorf("running winner_name = %v, want nil", *got.WinnerName)
		}
		if got.YouWon {
			t.Error("running you_won = true, want false")
		}
		if got.Status != "active" {
			t.Errorf("status = %q, want active", got.Status)
		}
	})

	t.Run("closed detail never leaks the reserve price", func(t *testing.T) {
		for _, id := range []string{f.sold.ID, f.unsold.ID, f.zerobid.ID} {
			_, raw := get(f.ada, id)
			if strings.Contains(string(raw), "reserve_price_cents") {
				t.Errorf("%s: closed detail leaks the reserve value: %s", id, raw)
			}
			// The reserve amount itself must not appear either.
			if id == f.unsold.ID && strings.Contains(string(raw), "5000") {
				t.Errorf("%s: closed detail leaks the reserve amount: %s", id, raw)
			}
		}
	})
}

// TestClosedAtOnDetailAndList pins the closed_at contract: every auction
// representation (detail and list items) carries closed_at — RFC3339 once the
// closing sweep has closed the auction, JSON null otherwise — so clients never
// derive the close time from ends_at.
func TestClosedAtOnDetailAndList(t *testing.T) {
	f := newM3Fixture(t)
	base := f.ts.URL

	// The fixture advanced the clock to ends_at+1m before running CloseDue, so
	// every closed auction carries that sweep instant as its closed_at.
	wantClosedAt := f.sold.EndsAt.Add(time.Minute)

	type detailResp struct {
		auctionResp
		WinnerName *string `json:"winner_name"`
		YouWon     bool    `json:"you_won"`
	}

	t.Run("detail: closed auction carries closed_at, active one null", func(t *testing.T) {
		var closed detailResp
		resp, _ := doJSON(t, f.ada, http.MethodGet, base+"/api/auctions/"+f.sold.ID, nil, &closed)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("closed detail status = %d, want 200", resp.StatusCode)
		}
		if closed.ClosedAt == nil || !closed.ClosedAt.Equal(wantClosedAt) {
			t.Errorf("closed detail closed_at = %v, want %v", closed.ClosedAt, wantClosedAt)
		}

		var running detailResp
		resp, raw := doJSON(t, f.ada, http.MethodGet, base+"/api/auctions/"+f.running.ID, nil, &running)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("running detail status = %d, want 200", resp.StatusCode)
		}
		if running.ClosedAt != nil {
			t.Errorf("running detail closed_at = %v, want nil", running.ClosedAt)
		}
		// Stable shape: the field is present and explicitly null, not omitted.
		if !strings.Contains(string(raw), `"closed_at":null`) {
			t.Errorf("running detail must emit \"closed_at\":null, got %s", raw)
		}
	})

	t.Run("list items: closed_at set on closed, null on active", func(t *testing.T) {
		var lr listResp
		resp, _ := doJSON(t, f.seller, http.MethodGet, base+"/api/users/me/auctions", nil, &lr)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("seller dashboard status = %d, want 200", resp.StatusCode)
		}
		byID := map[string]auctionResp{}
		for _, it := range lr.Items {
			byID[it.ID] = it
		}
		if got := byID[f.sold.ID]; got.ClosedAt == nil || !got.ClosedAt.Equal(wantClosedAt) {
			t.Errorf("closed list item closed_at = %v, want %v", got.ClosedAt, wantClosedAt)
		}
		if got := byID[f.running.ID]; got.ClosedAt != nil {
			t.Errorf("active list item closed_at = %v, want nil", got.ClosedAt)
		}
	})

	t.Run("purchases carry the close timestamp", func(t *testing.T) {
		var lr listResp
		resp, _ := doJSON(t, f.ada, http.MethodGet, base+"/api/users/me/purchases", nil, &lr)
		if resp.StatusCode != http.StatusOK || lr.Total != 1 {
			t.Fatalf("purchases status = %d total = %d, want 200/1", resp.StatusCode, lr.Total)
		}
		if got := lr.Items[0]; got.ClosedAt == nil || !got.ClosedAt.Equal(wantClosedAt) {
			t.Errorf("purchase closed_at = %v, want %v", got.ClosedAt, wantClosedAt)
		}
	})
}

// TestCloseEventFieldsReachesHub closes an auction while a WS watcher is
// subscribed, asserting the auction.closed event (winner_name, sold,
// final_price_cents) reaches subscribers through the hub — the M3 wiring the
// closer worker drives in production. The stack is built here (not via
// newTestHandler) so the test holds the very hub instance the service
// publishes through.
func TestCloseEventFieldsReachesHub(t *testing.T) {
	clock := &httpFakeClock{t: httpBaseTime}
	repo := newFakeAuctionRepo()
	h := hub.New()
	auctionSvc := auction.NewService(repo, clock, h)
	authSvc := auth.NewService(
		&fakeUserRepo{},
		&fakeSessionStore{sessions: map[string]auth.Session{}},
		auth.BcryptHasher{Cost: bcrypt.MinCost},
		clock,
	)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := newTestServer(t, New(authSvc, auctionSvc, h, clock.Now, logger, Config{}))
	base := ts.URL

	seller, _ := registerUser(t, base, "ws-close-seller@example.com", "Sue")
	_, adaID := registerUser(t, base, "ws-close-ada@example.com", "Ada")

	var created auctionResp
	resp, _ := doJSON(t, seller, http.MethodPost, base+"/api/auctions", map[string]any{
		"title": "WS close lot", "starting_price_cents": 1000, "duration_minutes": 30,
	}, &created)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", resp.StatusCode)
	}
	repo.addBid(created.ID, adaID, "Ada", 1300, httpBaseTime.Add(time.Minute))

	wsURL := "ws://" + strings.TrimPrefix(base, "http://") + "/ws/auctions/" + created.ID
	conn := wsDial(t, wsURL)
	_ = readWSUntil(t, conn, auction.EventAuctionState, 5*time.Second)
	_ = readWSUntil(t, conn, auction.EventPresenceUpdate, 5*time.Second)

	// Close the auction through the service path: repo commits internally,
	// then the service publishes auction.closed through the shared hub.
	clock.Set(created.EndsAt.Add(time.Minute))
	if err := auctionSvc.CloseDue(context.Background()); err != nil {
		t.Fatalf("CloseDue: %v", err)
	}

	data := readWSUntil(t, conn, auction.EventAuctionClosed, 5*time.Second)
	if data["auction_id"] != created.ID {
		t.Errorf("data.auction_id = %v, want %s", data["auction_id"], created.ID)
	}
	if data["winner_name"] != "Ada" {
		t.Errorf("data.winner_name = %v, want Ada", data["winner_name"])
	}
	if data["sold"] != true {
		t.Errorf("data.sold = %v, want true", data["sold"])
	}
	if data["final_price_cents"] != float64(1300) {
		t.Errorf("data.final_price_cents = %v, want 1300", data["final_price_cents"])
	}
	if data["status"] != "closed" {
		t.Errorf("data.status = %v, want closed", data["status"])
	}
	wsTime(t, data["server_now"]) // must be present and RFC3339
}
