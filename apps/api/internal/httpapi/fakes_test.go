package httpapi

// In-memory fakes implementing the auction and auth ports, used by the
// httptest smoke test to exercise the full HTTP stack without PostgreSQL.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
	"github.com/FranklinF25/auction-platform/apps/api/internal/auth"
)

type fakeAuctionRepo struct {
	mu       sync.Mutex
	auctions map[string]*auction.Auction
	nextID   int
	nextBid  int
}

func newFakeAuctionRepo() *fakeAuctionRepo {
	return &fakeAuctionRepo{auctions: map[string]*auction.Auction{}}
}

func (f *fakeAuctionRepo) Create(_ context.Context, a *auction.Auction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	// UUID-shaped ids, matching what Postgres generates in production.
	a.ID = fmt.Sprintf("00000000-0000-0000-0000-%012d", f.nextID)
	f.auctions[a.ID] = a
	return nil
}

func (f *fakeAuctionRepo) ByID(_ context.Context, id string) (*auction.Auction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.auctions[id]
	if !ok {
		return nil, auction.ErrNotFound
	}
	cp := *a
	cp.LoadBids(a.Bids()) // Bids() already returns a copy
	return &cp, nil
}

func (f *fakeAuctionRepo) List(_ context.Context, flt auction.ListFilter) ([]auction.ListItem, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ids := make([]string, 0, len(f.auctions))
	for id := range f.auctions {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := f.auctions[ids[i]], f.auctions[ids[j]]
		if a.CreatedAt.Equal(b.CreatedAt) {
			return a.ID < b.ID
		}
		return a.CreatedAt.After(b.CreatedAt)
	})

	all := make([]auction.ListItem, 0, len(ids))
	for _, id := range ids {
		a := f.auctions[id]
		if flt.Status != "" && a.Status != flt.Status {
			continue
		}
		if flt.Query != "" &&
			!strings.Contains(strings.ToLower(a.Title), strings.ToLower(flt.Query)) {
			continue
		}
		all = append(all, auction.ListItem{
			ID:                 a.ID,
			SellerID:           a.SellerID,
			Title:              a.Title,
			Description:        a.Description,
			StartingPriceCents: a.StartingPriceCents,
			CurrentPriceCents:  a.CurrentPrice(),
			MinIncrementCents:  a.MinIncrementCents,
			ReserveMet:         a.ReserveMet(),
			BidCount:           a.BidCount(),
			Status:             a.Status,
			EndsAt:             a.EndsAt,
			CreatedAt:          a.CreatedAt,
		})
	}

	total := len(all)
	start := (flt.Page - 1) * flt.PageSize
	if start > total {
		start = total
	}
	end := start + flt.PageSize
	if end > total {
		end = total
	}
	return all[start:end], total, nil
}

func (f *fakeAuctionRepo) ListBids(_ context.Context, auctionID string, page, pageSize int) ([]auction.Bid, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	a, ok := f.auctions[auctionID]
	if !ok {
		return nil, 0, auction.ErrNotFound
	}
	bids := a.Bids()
	sort.Slice(bids, func(i, j int) bool {
		if bids[i].CreatedAt.Equal(bids[j].CreatedAt) {
			return bids[i].ID < bids[j].ID
		}
		return bids[i].CreatedAt.After(bids[j].CreatedAt)
	})

	total := len(bids)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return bids[start:end], total, nil
}

// addBid seeds a bid directly into the stored aggregate (test helper standing
// in for the M2 bid endpoint).
func (f *fakeAuctionRepo) addBid(auctionID, bidderID, bidderName string, amountCents int64, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.auctions[auctionID]
	f.nextBid++
	bids := a.Bids()
	bids = append(bids, auction.Bid{
		ID:          fmt.Sprintf("bid-%d", f.nextBid),
		AuctionID:   auctionID,
		BidderID:    bidderID,
		BidderName:  bidderName,
		AmountCents: amountCents,
		CreatedAt:   at,
	})
	a.LoadBids(bids)
}

type fakeUserRepo struct {
	mu     sync.Mutex
	users  []auth.User
	nextID int
}

func (f *fakeUserRepo) Create(_ context.Context, u auth.User) (auth.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.users {
		if existing.Email == u.Email {
			return auth.User{}, auth.ErrEmailTaken
		}
	}
	f.nextID++
	u.ID = fmt.Sprintf("user-%d", f.nextID)
	u.CreatedAt = time.Now()
	f.users = append(f.users, u)
	return u, nil
}

func (f *fakeUserRepo) ByEmail(_ context.Context, email string) (auth.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u.Email == email {
			return u, nil
		}
	}
	return auth.User{}, auth.ErrNotFound
}

func (f *fakeUserRepo) ByID(_ context.Context, id string) (auth.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u.ID == id {
			return u, nil
		}
	}
	return auth.User{}, auth.ErrNotFound
}

type fakeSessionStore struct {
	mu       sync.Mutex
	sessions map[string]auth.Session
}

func (f *fakeSessionStore) Create(_ context.Context, s auth.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[s.TokenHash] = s
	return nil
}

func (f *fakeSessionStore) ByTokenHash(_ context.Context, hash string) (auth.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[hash]
	if !ok {
		return auth.Session{}, auth.ErrNotFound
	}
	return s, nil
}

func (f *fakeSessionStore) Delete(_ context.Context, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, hash)
	return nil
}
