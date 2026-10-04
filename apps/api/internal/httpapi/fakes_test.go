package httpapi

// In-memory fakes implementing the auction and auth ports, used by the
// httptest smoke test to exercise the full HTTP stack without PostgreSQL.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
	"github.com/FranklinF25/auction-platform/apps/api/internal/auth"
)

type fakeAuctionRepo struct {
	mu           sync.Mutex
	auctions     map[string]*auction.Auction
	names        map[string]string // bidder_id -> display name, for PlaceBid payloads
	nextID       int
	nextBid      int
	nextTx       int
	transactions map[string]*auction.Transaction
}

func newFakeAuctionRepo() *fakeAuctionRepo {
	return &fakeAuctionRepo{
		auctions:     map[string]*auction.Auction{},
		names:        map[string]string{},
		transactions: map[string]*auction.Transaction{},
	}
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
		if flt.SellerID != "" && a.SellerID != flt.SellerID {
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
			ClosedAt:           closedAtOf(a),
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

// CloseDue mirrors the production adapter: for every stored auction that is
// active and due, run the pure domain Close, write the post-close state back
// (the mutex plays the row lock), and on a sold close insert the pending
// checkout transaction in the same critical section — same shape as the pgx
// per-auction transaction. Not-closable auctions are skipped.
func (f *fakeAuctionRepo) CloseDue(_ context.Context, now time.Time) ([]auction.ClosedAuction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Deterministic order, matching the pgx sweep (ends_at, then id).
	ids := make([]string, 0, len(f.auctions))
	for id := range f.auctions {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := f.auctions[ids[i]], f.auctions[ids[j]]
		if a.EndsAt.Equal(b.EndsAt) {
			return a.ID < b.ID
		}
		return a.EndsAt.Before(b.EndsAt)
	})

	results := []auction.ClosedAuction{}
	for _, id := range ids {
		a := f.auctions[id]
		if a.Status != auction.StatusActive || now.Before(a.EndsAt) {
			continue
		}
		cp := *a
		cp.LoadBids(a.Bids())
		winner, won, err := cp.Close(now)
		if err != nil {
			continue // not closable under the lock: skip, not fail
		}
		f.auctions[id] = &cp
		res := auction.ClosedAuction{Auction: &cp, Winner: winner, Won: won}
		if won {
			f.nextTx++
			txn := &auction.Transaction{
				ID:          fmt.Sprintf("00000000-0000-0000-0000-%012d", f.nextTx),
				AuctionID:   id,
				WinnerID:    winner.BidderID,
				AmountCents: cp.CurrentPrice(),
				Status:      auction.TransactionPending,
				CreatedAt:   now,
				ExpiresAt:   now.Add(auction.TransactionExpiryDuration),
			}
			f.transactions[txn.ID] = txn
			res.Transaction = txn
		}
		results = append(results, res)
	}
	return results, nil
}

// ListWon mirrors the production adapter: closed, sold auctions where userID
// is the highest bidder, ordered by closed_at DESC (a never-closed fake auction
// counts as the zero time, so it sorts last and never appears anyway).
func (f *fakeAuctionRepo) ListWon(_ context.Context, userID string, page, pageSize int) ([]auction.ListItem, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ids := make([]string, 0, len(f.auctions))
	for id := range f.auctions {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := f.auctions[ids[i]], f.auctions[ids[j]]
		ca, cb := closedAtOf(a), closedAtOf(b)
		if ca.Equal(cb) {
			return a.ID > b.ID
		}
		return ca.After(cb)
	})

	all := make([]auction.ListItem, 0, len(ids))
	for _, id := range ids {
		a := f.auctions[id]
		if a.Status != auction.StatusClosed {
			continue
		}
		highest, ok := a.HighestBid()
		if !ok || highest.BidderID != userID || !a.ReserveMet() {
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
			ReserveMet:         true, // a won auction is sold by definition
			BidCount:           a.BidCount(),
			Status:             a.Status,
			EndsAt:             a.EndsAt,
			CreatedAt:          a.CreatedAt,
			ClosedAt:           closedAtOf(a),
		})
	}

	total := len(all)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return all[start:end], total, nil
}

// transactionByAuction returns the stored transaction for an auction id.
func (f *fakeAuctionRepo) transactionByAuction(auctionID string) (*auction.Transaction, bool) {
	for _, t := range f.transactions {
		if t.AuctionID == auctionID {
			return t, true
		}
	}
	return nil, false
}

// CreateTransaction stores t and assigns a test id (mirrors the port).
func (f *fakeAuctionRepo) CreateTransaction(_ context.Context, t *auction.Transaction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextTx++
	t.ID = fmt.Sprintf("00000000-0000-0000-0000-%012d", f.nextTx)
	f.transactions[t.ID] = t
	return nil
}

// TransactionByID mirrors the port: ErrNotFound when absent.
func (f *fakeAuctionRepo) TransactionByID(_ context.Context, id string) (*auction.Transaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.transactions[id]
	if !ok {
		return nil, auction.ErrNotFound
	}
	cp := *t
	return &cp, nil
}

// PayTransaction mirrors the production adapter: run the pure Transaction.Pay
// on the stored row (the mutex plays the row lock) and persist the outcome,
// including a decline's failed status.
func (f *fakeAuctionRepo) PayTransaction(_ context.Context, id, cardNumber string, now time.Time) (*auction.Transaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.transactions[id]
	if !ok {
		return nil, auction.ErrNotFound
	}
	cp := *t
	if err := cp.Pay(cardNumber, now); err != nil {
		return nil, err
	}
	f.transactions[id] = &cp
	return &cp, nil
}

// ExpireDueTransactions mirrors the single-UPDATE sweep over the fake store.
func (f *fakeAuctionRepo) ExpireDueTransactions(_ context.Context, now time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, t := range f.transactions {
		if (t.Status == auction.TransactionPending || t.Status == auction.TransactionFailed) &&
			!now.Before(t.ExpiresAt) {
			t.Status = auction.TransactionExpired
			count++
		}
	}
	return count, nil
}

// ListMyTransactions mirrors the production read model: won auctions (closed,
// sold, highest bidder) joined with their transaction, closed_at DESC.
func (f *fakeAuctionRepo) ListMyTransactions(_ context.Context, userID string, page, pageSize int) ([]auction.PurchaseItem, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ids := make([]string, 0, len(f.auctions))
	for id := range f.auctions {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := f.auctions[ids[i]], f.auctions[ids[j]]
		ca, cb := closedAtOf(a), closedAtOf(b)
		if ca.Equal(cb) {
			return a.ID > b.ID
		}
		return ca.After(cb)
	})

	all := make([]auction.PurchaseItem, 0, len(ids))
	for _, id := range ids {
		a := f.auctions[id]
		if a.Status != auction.StatusClosed {
			continue
		}
		highest, ok := a.HighestBid()
		if !ok || highest.BidderID != userID || !a.ReserveMet() {
			continue
		}
		txn, ok := f.transactionByAuction(id)
		if !ok {
			continue
		}
		all = append(all, auction.PurchaseItem{
			ListItem: auction.ListItem{
				ID:                 a.ID,
				SellerID:           a.SellerID,
				Title:              a.Title,
				Description:        a.Description,
				StartingPriceCents: a.StartingPriceCents,
				CurrentPriceCents:  a.CurrentPrice(),
				MinIncrementCents:  a.MinIncrementCents,
				ReserveMet:         true, // a won auction is sold by definition
				BidCount:           a.BidCount(),
				Status:             a.Status,
				EndsAt:             a.EndsAt,
				CreatedAt:          a.CreatedAt,
				ClosedAt:           closedAtOf(a),
			},
			Transaction: *txn,
		})
	}

	total := len(all)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return all[start:end], total, nil
}

// SellerSales mirrors the production aggregate over the fake store.
func (f *fakeAuctionRepo) SellerSales(_ context.Context, sellerID string) (int, int, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	completed, pending := 0, 0
	var revenue int64
	for _, t := range f.transactions {
		a, ok := f.auctions[t.AuctionID]
		if !ok || a.SellerID != sellerID {
			continue
		}
		switch t.Status {
		case auction.TransactionCompleted:
			completed++
			revenue += t.AmountCents
		case auction.TransactionPending:
			pending++
		}
	}
	return completed, pending, revenue, nil
}

func closedAtOf(a *auction.Auction) time.Time {
	if a.ClosedAt == nil {
		return time.Time{}
	}
	return *a.ClosedAt
}

// PlaceBid mirrors the production adapter's contract without the row lock
// (the fake's mutex plays the serializer): run the pure domain rules, assign
// ID + bidder name, write back, report whether the soft close moved ends_at.
// ErrBidTooLow is wrapped with the concrete numbers like the pgx adapter does.
func (f *fakeAuctionRepo) PlaceBid(_ context.Context, auctionID, bidderID string, amountCents int64, now time.Time) (*auction.PlacedBid, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	a, ok := f.auctions[auctionID]
	if !ok {
		return nil, auction.ErrNotFound
	}
	cp := *a
	cp.LoadBids(a.Bids())
	prevEndsAt := cp.EndsAt

	bid, err := cp.PlaceBid(bidderID, amountCents, now)
	if err != nil {
		if errors.Is(err, auction.ErrBidTooLow) {
			min := cp.CurrentPrice() + cp.MinIncrementCents
			return nil, fmt.Errorf(
				"%w: bid must be at least %d cents (current price %d cents plus minimum increment %d cents)",
				err, min, cp.CurrentPrice(), cp.MinIncrementCents)
		}
		return nil, err
	}

	f.nextBid++
	bid.ID = fmt.Sprintf("bid-%d", f.nextBid)
	bid.BidderName = f.names[bidderID]

	bids := cp.Bids()
	bids[len(bids)-1] = bid
	cp.LoadBids(bids)
	f.auctions[auctionID] = &cp
	return &auction.PlacedBid{Bid: bid, Auction: &cp, Extended: cp.EndsAt.After(prevEndsAt)}, nil
}

// setName registers a bidder's display name for PlaceBid payloads.
func (f *fakeAuctionRepo) setName(bidderID, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.names[bidderID] = name
}

// setStatus flips a stored auction's status (test shortcut for closed states).
func (f *fakeAuctionRepo) setStatus(auctionID string, status auction.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auctions[auctionID].Status = status
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
