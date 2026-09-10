package httpapi

// End-to-end smoke test through the real chi router, middleware, services and
// domain rules, with only the outermost ports (repositories, session store)
// replaced by in-memory fakes. Covers the cookie session flow and the auction
// CRUD surface of M1.

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
	"github.com/FranklinF25/auction-platform/apps/api/internal/auth"
	"github.com/FranklinF25/auction-platform/apps/api/internal/hub"
)

var httpBaseTime = time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

// httpFakeClock is a mutable fake clock so tests can advance time (soft
// close) between requests.
type httpFakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *httpFakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *httpFakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

type userResp struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type auctionResp struct {
	ID                 string    `json:"id"`
	SellerID           string    `json:"seller_id"`
	Title              string    `json:"title"`
	Description        string    `json:"description"`
	StartingPriceCents int64     `json:"starting_price_cents"`
	MinIncrementCents  int64     `json:"min_increment_cents"`
	CurrentPriceCents  int64     `json:"current_price_cents"`
	ReserveMet         bool      `json:"reserve_met"`
	BidCount           int       `json:"bid_count"`
	Status             string    `json:"status"`
	EndsAt             time.Time `json:"ends_at"`
	CreatedAt          time.Time `json:"created_at"`
}

type listResp struct {
	Items    []auctionResp `json:"items"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
	Total    int           `json:"total"`
}

type bidItemResp struct {
	ID          string    `json:"id"`
	BidderName  string    `json:"bidder_name"`
	AmountCents int64     `json:"amount_cents"`
	CreatedAt   time.Time `json:"created_at"`
}

type bidsResp struct {
	Items    []bidItemResp `json:"items"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
	Total    int           `json:"total"`
}

type errResp struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func newTestHandler(t *testing.T, clock *httpFakeClock) (http.Handler, *fakeAuctionRepo) {
	t.Helper()
	authSvc := auth.NewService(
		&fakeUserRepo{},
		&fakeSessionStore{sessions: map[string]auth.Session{}},
		auth.BcryptHasher{Cost: bcrypt.MinCost},
		clock,
	)
	auctionRepo := newFakeAuctionRepo()
	h := hub.New()
	auctionSvc := auction.NewService(auctionRepo, clock, h)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(authSvc, auctionSvc, h, clock.Now, logger, Config{CookieSecure: false}), auctionRepo
}

// doJSON performs a JSON request and decodes the response body into out.
func doJSON(t *testing.T, client *http.Client, method, url string, body, out any) (*http.Response, []byte) {
	t.Helper()
	var raw []byte
	if body != nil {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode request: %v", err)
		}
		raw = buf.Bytes()
		return doRaw(t, client, method, url, raw, out)
	}
	return doRaw(t, client, method, url, nil, out)
}

func doRaw(t *testing.T, client *http.Client, method, url string, body []byte, out any) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("decode response %q: %v", data, err)
		}
	}
	return resp, data
}

func TestAPISmoke(t *testing.T) {
	handler, auctionRepo := newTestHandler(t, &httpFakeClock{t: httpBaseTime})
	ts := httptest.NewServer(handler)
	defer ts.Close()
	base := ts.URL

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	client := &http.Client{Jar: jar} // authenticated browser
	anon := &http.Client{}           // anonymous browser
	_ = anon

	var me userResp
	var created auctionResp
	var auctionID string

	t.Run("register returns 201 and sets an httpOnly session cookie", func(t *testing.T) {
		resp, _ := doJSON(t, client, http.MethodPost, base+"/api/auth/register",
			map[string]any{"email": "ada@example.com", "password": "hunter2ok", "name": "Ada"}, &me)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d, want 201", resp.StatusCode)
		}
		if me.Email != "ada@example.com" || me.Name != "Ada" || me.ID == "" {
			t.Errorf("register body = %+v", me)
		}
		var cookie *http.Cookie
		for _, c := range resp.Cookies() {
			if c.Name == "auction_session" {
				cookie = c
			}
		}
		if cookie == nil {
			t.Fatal("auction_session cookie not set")
		}
		if !cookie.HttpOnly {
			t.Error("cookie must be httpOnly")
		}
		if cookie.Path != "/" {
			t.Errorf("cookie path = %q, want %q", cookie.Path, "/")
		}
	})

	t.Run("me with the cookie returns the current user", func(t *testing.T) {
		var got userResp
		resp, _ := doJSON(t, client, http.MethodGet, base+"/api/me", nil, &got)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if got.ID != me.ID || got.Email != me.Email {
			t.Errorf("me = %+v, want %+v", got, me)
		}
	})

	t.Run("me without a cookie is 401", func(t *testing.T) {
		var e errResp
		resp, _ := doJSON(t, anon, http.MethodGet, base+"/api/me", nil, &e)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
		if e.Error.Code != "unauthorized" {
			t.Errorf("error code = %q, want unauthorized", e.Error.Code)
		}
	})

	t.Run("create auction requires auth", func(t *testing.T) {
		resp, _ := doJSON(t, anon, http.MethodPost, base+"/api/auctions",
			map[string]any{"title": "X", "starting_price_cents": 100, "duration_minutes": 5}, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("create auction returns 201 with derived fields", func(t *testing.T) {
		resp, raw := doJSON(t, client, http.MethodPost, base+"/api/auctions", map[string]any{
			"title":                "Mechanical keyboard",
			"description":          "Tactile switches",
			"starting_price_cents": 5000,
			"duration_minutes":     120,
			"reserve_price_cents":  8000,
		}, &created)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d, want 201 (body: %s)", resp.StatusCode, raw)
		}
		auctionID = created.ID
		if created.MinIncrementCents != 100 {
			t.Errorf("min_increment_cents = %d, want default 100", created.MinIncrementCents)
		}
		if created.CurrentPriceCents != 5000 {
			t.Errorf("current_price_cents = %d, want starting 5000", created.CurrentPriceCents)
		}
		if created.ReserveMet {
			t.Error("reserve_met = true, want false at starting price below 8000 reserve")
		}
		if created.BidCount != 0 {
			t.Errorf("bid_count = %d, want 0", created.BidCount)
		}
		if created.Status != "active" {
			t.Errorf("status = %q, want active", created.Status)
		}
		if want := httpBaseTime.Add(120 * time.Minute); !created.EndsAt.Equal(want) {
			t.Errorf("ends_at = %v, want %v", created.EndsAt, want)
		}
		if strings.Contains(string(raw), "reserve_price_cents") {
			t.Errorf("response leaks the reserve price value: %s", raw)
		}
	})

	t.Run("create auction with invalid input is 400", func(t *testing.T) {
		var e errResp
		resp, _ := doJSON(t, client, http.MethodPost, base+"/api/auctions",
			map[string]any{"title": "Broken", "starting_price_cents": 100, "duration_minutes": 0}, &e)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
		if e.Error.Code != "validation_error" {
			t.Errorf("error code = %q, want validation_error", e.Error.Code)
		}
	})

	t.Run("list returns paging defaults and the created auction", func(t *testing.T) {
		var lr listResp
		resp, _ := doJSON(t, client, http.MethodGet, base+"/api/auctions", nil, &lr)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if lr.Page != 1 || lr.PageSize != 20 || lr.Total != 1 {
			t.Errorf("page/page_size/total = %d/%d/%d, want 1/20/1", lr.Page, lr.PageSize, lr.Total)
		}
		if len(lr.Items) != 1 || lr.Items[0].ID != auctionID {
			t.Fatalf("items = %+v, want the created auction", lr.Items)
		}
		if lr.Items[0].CurrentPriceCents != 5000 {
			t.Errorf("current_price_cents = %d, want 5000", lr.Items[0].CurrentPriceCents)
		}
	})

	t.Run("list search is case-insensitive on title", func(t *testing.T) {
		var hit listResp
		resp, _ := doJSON(t, client, http.MethodGet, base+"/api/auctions?q=KEYBOARD", nil, &hit)
		if resp.StatusCode != http.StatusOK || hit.Total != 1 {
			t.Fatalf("q=KEYBOARD: status=%d total=%d, want 200/1", resp.StatusCode, hit.Total)
		}

		var miss listResp
		doJSON(t, client, http.MethodGet, base+"/api/auctions?q=does-not-exist", nil, &miss)
		if miss.Total != 0 || len(miss.Items) != 0 {
			t.Errorf("q=does-not-exist: total=%d items=%d, want 0/0", miss.Total, len(miss.Items))
		}
	})

	t.Run("list clamps page_size to 50", func(t *testing.T) {
		var lr listResp
		doJSON(t, client, http.MethodGet, base+"/api/auctions?page_size=100", nil, &lr)
		if lr.PageSize != 50 {
			t.Errorf("page_size = %d, want clamp to 50", lr.PageSize)
		}
	})

	t.Run("list rejects an unknown status filter", func(t *testing.T) {
		var e errResp
		resp, _ := doJSON(t, client, http.MethodGet, base+"/api/auctions?status=bogus", nil, &e)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("detail shows derived fields without leaking the reserve", func(t *testing.T) {
		var got auctionResp
		resp, raw := doJSON(t, client, http.MethodGet, base+"/api/auctions/"+auctionID, nil, &got)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if got.CurrentPriceCents != 5000 || got.BidCount != 0 || got.ReserveMet {
			t.Errorf("detail = %+v", got)
		}
		if strings.Contains(string(raw), "reserve_price_cents") {
			t.Errorf("detail leaks the reserve price value: %s", raw)
		}
	})

	t.Run("bids and derived fields update after seeded bids", func(t *testing.T) {
		auctionRepo.addBid(auctionID, "user-2", "Grace", 6000, httpBaseTime.Add(5*time.Minute))
		auctionRepo.addBid(auctionID, "user-3", "Linus", 7500, httpBaseTime.Add(10*time.Minute))

		var got auctionResp
		resp, _ := doJSON(t, client, http.MethodGet, base+"/api/auctions/"+auctionID, nil, &got)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if got.CurrentPriceCents != 7500 {
			t.Errorf("current_price_cents = %d, want 7500", got.CurrentPriceCents)
		}
		if got.BidCount != 2 {
			t.Errorf("bid_count = %d, want 2", got.BidCount)
		}
		if got.ReserveMet {
			t.Error("reserve_met = true, want false (7500 < 8000)")
		}
	})

	t.Run("bid history is paged, newest first, with bidder names", func(t *testing.T) {
		var br bidsResp
		resp, _ := doJSON(t, client, http.MethodGet, base+"/api/auctions/"+auctionID+"/bids", nil, &br)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if br.Total != 2 || br.Page != 1 || br.PageSize != 20 || len(br.Items) != 2 {
			t.Fatalf("bids page = %+v", br)
		}
		if br.Items[0].BidderName != "Linus" || br.Items[0].AmountCents != 7500 {
			t.Errorf("newest bid = %+v, want Linus at 7500", br.Items[0])
		}
		if br.Items[1].BidderName != "Grace" || br.Items[1].AmountCents != 6000 {
			t.Errorf("second bid = %+v, want Grace at 6000", br.Items[1])
		}
	})

	t.Run("missing auction is 404 and malformed id is 400", func(t *testing.T) {
		resp, _ := doJSON(t, client, http.MethodGet, base+"/api/auctions/00000000-0000-0000-0000-000000000000", nil, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("missing id status = %d, want 404", resp.StatusCode)
		}
		resp, _ = doJSON(t, client, http.MethodGet, base+"/api/auctions/not-a-uuid", nil, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("malformed id status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("login issues a fresh cookie and wrong passwords are 401", func(t *testing.T) {
		var e errResp
		resp, _ := doJSON(t, anon, http.MethodPost, base+"/api/auth/login",
			map[string]any{"email": "ada@example.com", "password": "nope-nope"}, &e)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("wrong password status = %d, want 401", resp.StatusCode)
		}

		var got userResp
		resp, _ = doJSON(t, anon, http.MethodPost, base+"/api/auth/login",
			map[string]any{"email": "ada@example.com", "password": "hunter2ok"}, &got)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("login status = %d, want 200", resp.StatusCode)
		}
		if got.Email != "ada@example.com" {
			t.Errorf("login body = %+v", got)
		}
		var cookie *http.Cookie
		for _, c := range resp.Cookies() {
			if c.Name == "auction_session" {
				cookie = c
			}
		}
		if cookie == nil {
			t.Fatal("login did not set the session cookie")
		}
	})

	t.Run("logout clears the session server-side and client-side", func(t *testing.T) {
		resp, _ := doJSON(t, client, http.MethodPost, base+"/api/auth/logout", nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("logout status = %d, want 200", resp.StatusCode)
		}
		cleared := false
		for _, c := range resp.Cookies() {
			if c.Name == "auction_session" && c.MaxAge < 0 {
				cleared = true
			}
		}
		if !cleared {
			t.Error("logout must clear the cookie with a negative MaxAge")
		}

		// The jar may still send the old cookie; the server must reject it.
		resp2, _ := doJSON(t, client, http.MethodGet, base+"/api/me", nil, nil)
		if resp2.StatusCode != http.StatusUnauthorized {
			t.Errorf("me after logout = %d, want 401", resp2.StatusCode)
		}
	})

	t.Run("healthz responds 200", func(t *testing.T) {
		resp, _ := doRaw(t, client, http.MethodGet, base+"/healthz", nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("healthz = %d, want 200", resp.StatusCode)
		}
	})
}
