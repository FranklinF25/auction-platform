package httpapi

// Tests for the bid hot path endpoint: POST /api/auctions/{id}/bids —
// authentication, validation, domain error mapping and the 201 shape.

import (
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
)

// registerUser creates an authenticated browser (cookie jar) and returns it
// with the user's id.
func registerUser(t *testing.T, base, email, name string) (*http.Client, string) {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	client := &http.Client{Jar: jar}
	var user userResp
	resp, _ := doJSON(t, client, http.MethodPost, base+"/api/auth/register", map[string]any{
		"email":    email,
		"password": "password123",
		"name":     name,
	}, &user)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register %s: status %d", email, resp.StatusCode)
	}
	return client, user.ID
}

// createAuction creates a fresh 1000-cent auction with a 100-cent increment
// running 30 minutes from the (frozen) clock.
func createAuction(t *testing.T, client *http.Client, base string) auctionResp {
	t.Helper()
	var created auctionResp
	resp, _ := doJSON(t, client, http.MethodPost, base+"/api/auctions", map[string]any{
		"title":                "Hot path lot",
		"description":          "for bid endpoint tests",
		"starting_price_cents": 1000,
		"min_increment_cents":  100,
		"duration_minutes":     30,
	}, &created)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create auction: status %d", resp.StatusCode)
	}
	return created
}

func TestPlaceBidEndpoint(t *testing.T) {
	clock := &httpFakeClock{t: httpBaseTime}
	handler, repo := newTestHandler(t, clock)
	ts := newTestServer(t, handler)
	base := ts.URL

	sellerClient, _ := registerUser(t, base, "seller@example.com", "Sue")
	bidderClient, bidderID := registerUser(t, base, "bidder@example.com", "Ada")
	repo.setName(bidderID, "Ada")
	anon := &http.Client{}

	t.Run("unauthenticated bidder gets 401", func(t *testing.T) {
		created := createAuction(t, sellerClient, base)
		var e errResp
		resp, _ := doJSON(t, anon, http.MethodPost, base+"/api/auctions/"+created.ID+"/bids",
			map[string]any{"amount_cents": 1100}, &e)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
		if e.Error.Code != "unauthorized" {
			t.Errorf("code = %q, want unauthorized", e.Error.Code)
		}
	})

	t.Run("seller cannot bid on own auction: 403 own_auction", func(t *testing.T) {
		created := createAuction(t, sellerClient, base)
		var e errResp
		resp, _ := doJSON(t, sellerClient, http.MethodPost, base+"/api/auctions/"+created.ID+"/bids",
			map[string]any{"amount_cents": 1100}, &e)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", resp.StatusCode)
		}
		if e.Error.Code != "own_auction" {
			t.Errorf("code = %q, want own_auction", e.Error.Code)
		}
	})

	t.Run("non-positive amount: 400 validation", func(t *testing.T) {
		created := createAuction(t, sellerClient, base)
		var e errResp
		resp, _ := doJSON(t, bidderClient, http.MethodPost, base+"/api/auctions/"+created.ID+"/bids",
			map[string]any{"amount_cents": 0}, &e)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
		if e.Error.Code != "validation_error" {
			t.Errorf("code = %q, want validation_error", e.Error.Code)
		}
		if !strings.Contains(e.Error.Message, "amount_cents") {
			t.Errorf("message %q should point at amount_cents", e.Error.Message)
		}
	})

	t.Run("too low: 409 bid_too_low mentioning current price and increment", func(t *testing.T) {
		created := createAuction(t, sellerClient, base)
		var e errResp
		resp, _ := doJSON(t, bidderClient, http.MethodPost, base+"/api/auctions/"+created.ID+"/bids",
			map[string]any{"amount_cents": 1050}, &e)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409", resp.StatusCode)
		}
		if e.Error.Code != "bid_too_low" {
			t.Errorf("code = %q, want bid_too_low", e.Error.Code)
		}
		for _, want := range []string{"1100", "1000", "100"} {
			if !strings.Contains(e.Error.Message, want) {
				t.Errorf("message %q must mention %q", e.Error.Message, want)
			}
		}
	})

	t.Run("not active: 409 auction_not_active", func(t *testing.T) {
		created := createAuction(t, sellerClient, base)
		repo.setStatus(created.ID, auction.StatusClosed)
		var e errResp
		resp, _ := doJSON(t, bidderClient, http.MethodPost, base+"/api/auctions/"+created.ID+"/bids",
			map[string]any{"amount_cents": 1100}, &e)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409", resp.StatusCode)
		}
		if e.Error.Code != "auction_not_active" {
			t.Errorf("code = %q, want auction_not_active", e.Error.Code)
		}
	})

	t.Run("ended auction: 409 auction_closed", func(t *testing.T) {
		created := createAuction(t, sellerClient, base)
		clock.Set(created.EndsAt.Add(time.Second))
		defer clock.Set(httpBaseTime)
		var e errResp
		resp, _ := doJSON(t, bidderClient, http.MethodPost, base+"/api/auctions/"+created.ID+"/bids",
			map[string]any{"amount_cents": 1100}, &e)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409", resp.StatusCode)
		}
		if e.Error.Code != "auction_closed" {
			t.Errorf("code = %q, want auction_closed", e.Error.Code)
		}
	})

	t.Run("unknown auction: 404", func(t *testing.T) {
		var e errResp
		resp, _ := doJSON(t, bidderClient, http.MethodPost,
			base+"/api/auctions/00000000-0000-0000-0000-000000000999/bids",
			map[string]any{"amount_cents": 1100}, &e)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
		if e.Error.Code != "not_found" {
			t.Errorf("code = %q, want not_found", e.Error.Code)
		}
	})

	t.Run("success: 201 with the pinned response shape", func(t *testing.T) {
		created := createAuction(t, sellerClient, base)
		var bid struct {
			BidID             string    `json:"bid_id"`
			AuctionID         string    `json:"auction_id"`
			AmountCents       int64     `json:"amount_cents"`
			CurrentPriceCents int64     `json:"current_price_cents"`
			EndsAt            time.Time `json:"ends_at"`
			ServerNow         time.Time `json:"server_now"`
		}
		resp, _ := doJSON(t, bidderClient, http.MethodPost, base+"/api/auctions/"+created.ID+"/bids",
			map[string]any{"amount_cents": 1100}, &bid)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d, want 201", resp.StatusCode)
		}
		if bid.BidID == "" {
			t.Error("bid_id must be set")
		}
		if bid.AuctionID != created.ID {
			t.Errorf("auction_id = %q, want %q", bid.AuctionID, created.ID)
		}
		if bid.AmountCents != 1100 || bid.CurrentPriceCents != 1100 {
			t.Errorf("amount=%d current=%d, want 1100/1100", bid.AmountCents, bid.CurrentPriceCents)
		}
		if !bid.EndsAt.Equal(created.EndsAt) {
			t.Errorf("ends_at = %v, want the unchanged %v", bid.EndsAt, created.EndsAt)
		}
		if !bid.ServerNow.Equal(httpBaseTime) {
			t.Errorf("server_now = %v, want %v", bid.ServerNow, httpBaseTime)
		}
	})
}

func TestPlaceBidInvalidUUID(t *testing.T) {
	clock := &httpFakeClock{t: httpBaseTime}
	handler, _ := newTestHandler(t, clock)
	ts := newTestServer(t, handler)

	bidderClient, _ := registerUser(t, ts.URL, "shape@example.com", "Bo")
	var e errResp
	resp, _ := doJSON(t, bidderClient, http.MethodPost, ts.URL+"/api/auctions/not-a-uuid/bids",
		map[string]any{"amount_cents": 1100}, &e)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if e.Error.Code != "invalid_id" {
		t.Errorf("code = %q, want invalid_id", e.Error.Code)
	}
}
