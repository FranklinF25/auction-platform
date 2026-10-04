package auction_test

// Use-case tests for the M4 checkout service methods: PayTransaction's
// ownership rule (another user's transaction is indistinguishable from a
// missing one), the decline/retry flow through the repository, purchases
// carrying their transaction, and the expiry wrapper.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
)

// transactionRepo is an in-memory Repository for the checkout tests. The
// transaction surface is honest (locked-load/pay/persist is simulated by the
// mutex); the auction surface is the minimum the port requires.
type transactionRepo struct {
	mu     sync.Mutex
	stored map[string]*auction.Auction
	txs    map[string]*auction.Transaction
	nextID int
}

func newTransactionRepo() *transactionRepo {
	return &transactionRepo{
		stored: map[string]*auction.Auction{},
		txs:    map[string]*auction.Transaction{},
	}
}

func (f *transactionRepo) Create(_ context.Context, a *auction.Auction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a.ID == "" { // pre-seeded lots keep their explicit id
		f.nextID++
		a.ID = fmt.Sprintf("auction-%d", f.nextID)
	}
	f.stored[a.ID] = a
	return nil
}

func (f *transactionRepo) ByID(_ context.Context, id string) (*auction.Auction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.stored[id]
	if !ok {
		return nil, auction.ErrNotFound
	}
	cp := *a
	cp.LoadBids(a.Bids())
	return &cp, nil
}

func (f *transactionRepo) List(_ context.Context, _ auction.ListFilter) ([]auction.ListItem, int, error) {
	return nil, 0, nil
}

func (f *transactionRepo) ListBids(_ context.Context, _ string, _, _ int) ([]auction.Bid, int, error) {
	return nil, 0, nil
}

func (f *transactionRepo) PlaceBid(_ context.Context, _, _ string, _ int64, _ time.Time) (*auction.PlacedBid, error) {
	return nil, nil
}

func (f *transactionRepo) CloseDue(_ context.Context, _ time.Time) ([]auction.ClosedAuction, error) {
	return nil, nil
}

func (f *transactionRepo) ListWon(_ context.Context, _ string, _, _ int) ([]auction.ListItem, int, error) {
	return nil, 0, nil
}

func (f *transactionRepo) CreateTransaction(_ context.Context, t *auction.Transaction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	if t.ID == "" {
		t.ID = fmt.Sprintf("tx-%d", f.nextID)
	}
	f.txs[t.ID] = t
	return nil
}

func (f *transactionRepo) TransactionByID(_ context.Context, id string) (*auction.Transaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.txs[id]
	if !ok {
		return nil, auction.ErrNotFound
	}
	cp := *t
	return &cp, nil
}

func (f *transactionRepo) PayTransaction(_ context.Context, id, cardNumber string, now time.Time) (*auction.Transaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.txs[id]
	if !ok {
		return nil, auction.ErrNotFound
	}
	cp := *t
	if err := cp.Pay(cardNumber, now); err != nil {
		return nil, err // rolled back: nothing persisted
	}
	f.txs[id] = &cp // COMMIT
	return &cp, nil
}

func (f *transactionRepo) ExpireDueTransactions(_ context.Context, now time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, t := range f.txs {
		if (t.Status == auction.TransactionPending || t.Status == auction.TransactionFailed) &&
			!now.Before(t.ExpiresAt) {
			t.Status = auction.TransactionExpired
			count++
		}
	}
	return count, nil
}

func (f *transactionRepo) ListMyTransactions(_ context.Context, userID string, page, pageSize int) ([]auction.PurchaseItem, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ids := make([]string, 0, len(f.stored))
	for id := range f.stored {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := f.stored[ids[i]], f.stored[ids[j]]
		ca, cb := closedAtValue(a), closedAtValue(b)
		if ca.Equal(cb) {
			return a.ID > b.ID
		}
		return ca.After(cb)
	})

	all := make([]auction.PurchaseItem, 0, len(ids))
	for _, id := range ids {
		a := f.stored[id]
		if a.Status != auction.StatusClosed {
			continue
		}
		highest, ok := a.HighestBid()
		if !ok || highest.BidderID != userID || !a.ReserveMet() {
			continue
		}
		var txn *auction.Transaction
		for _, t := range f.txs {
			if t.AuctionID == id {
				txn = t
				break
			}
		}
		if txn == nil {
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
				ReserveMet:         true,
				BidCount:           a.BidCount(),
				Status:             a.Status,
				EndsAt:             a.EndsAt,
				CreatedAt:          a.CreatedAt,
				ClosedAt:           closedAtValue(a),
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

func (f *transactionRepo) SellerSales(_ context.Context, _ string) (int, int, int64, error) {
	return 0, 0, 0, nil
}

func closedAtValue(a *auction.Auction) time.Time {
	if a.ClosedAt == nil {
		return time.Time{}
	}
	return *a.ClosedAt
}

// soldLot returns a closed, sold auction (highest bid wins, no reserve) won by
// winnerID, closed one hour after txBaseTime.
func soldLot(id, winnerID string, topBid int64) *auction.Auction {
	a, err := auction.NewAuction("seller-1", auction.CreateInput{
		Title:              "Checkout lot " + id,
		StartingPriceCents: 1000,
		MinIncrementCents:  100,
		DurationMinutes:    30,
	}, txBaseTime)
	if err != nil {
		panic(err)
	}
	a.ID = id
	a.LoadBids([]auction.Bid{
		{BidderID: "bidder-1", AmountCents: topBid - 200, CreatedAt: txBaseTime.Add(time.Minute)},
		{BidderID: winnerID, AmountCents: topBid, CreatedAt: txBaseTime.Add(2 * time.Minute)},
	})
	// The lot is already closed: replay the pure close at a past instant.
	if _, _, err := a.Close(txBaseTime.Add(time.Hour)); err != nil {
		panic(err)
	}
	return a
}

func TestPayTransactionOwnershipAndFlow(t *testing.T) {
	repo := newTransactionRepo()
	lot := soldLot("auction-1", "bidder-2", 2000)
	if err := repo.Create(context.Background(), lot); err != nil {
		t.Fatalf("seed lot: %v", err)
	}
	txn := &auction.Transaction{
		AuctionID:   "auction-1",
		WinnerID:    "bidder-2",
		AmountCents: 2000,
		Status:      auction.TransactionPending,
		CreatedAt:   txBaseTime.Add(time.Hour),
		ExpiresAt:   txBaseTime.Add(time.Hour + auction.TransactionExpiryDuration),
	}
	if err := repo.CreateTransaction(context.Background(), txn); err != nil {
		t.Fatalf("seed transaction: %v", err)
	}

	clock := &mutableClock{t: txBaseTime.Add(2 * time.Hour)}
	svc := auction.NewService(repo, clock, nil)

	t.Run("the winner pays successfully", func(t *testing.T) {
		got, err := svc.PayTransaction(context.Background(), "bidder-2", txn.ID, "4111111111111111")
		if err != nil {
			t.Fatalf("PayTransaction: %v", err)
		}
		if got.Status != auction.TransactionCompleted {
			t.Errorf("status = %q, want completed", got.Status)
		}
		if got.PaidAt == nil || !got.PaidAt.Equal(clock.Now()) {
			t.Errorf("paid_at = %v, want the service clock %v", got.PaidAt, clock.Now())
		}
	})

	t.Run("another user gets ErrNotFound, with no state change", func(t *testing.T) {
		repo2 := newTransactionRepo()
		lot2 := soldLot("auction-2", "bidder-2", 3000)
		if err := repo2.Create(context.Background(), lot2); err != nil {
			t.Fatalf("seed lot: %v", err)
		}
		txn2 := &auction.Transaction{
			AuctionID: "auction-2", WinnerID: "bidder-2", AmountCents: 3000,
			Status: auction.TransactionPending, CreatedAt: txBaseTime, ExpiresAt: txBaseTime.Add(48 * time.Hour),
		}
		if err := repo2.CreateTransaction(context.Background(), txn2); err != nil {
			t.Fatalf("seed transaction: %v", err)
		}
		svc2 := auction.NewService(repo2, clock, nil)

		_, err := svc2.PayTransaction(context.Background(), "bidder-1", txn2.ID, "4111111111111111")
		if !errors.Is(err, auction.ErrNotFound) {
			t.Fatalf("want ErrNotFound for a foreign transaction, got %v", err)
		}
		stored, _ := repo2.TransactionByID(context.Background(), txn2.ID)
		if stored.Status != auction.TransactionPending {
			t.Errorf("a foreign attempt must not pay: status = %q", stored.Status)
		}
	})

	t.Run("a missing transaction is ErrNotFound", func(t *testing.T) {
		_, err := svc.PayTransaction(context.Background(), "bidder-2", "ghost", "4111111111111111")
		if !errors.Is(err, auction.ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
	})
}

func TestPayTransactionDeclineThenRetry(t *testing.T) {
	repo := newTransactionRepo()
	if err := repo.Create(context.Background(), soldLot("auction-1", "bidder-2", 2000)); err != nil {
		t.Fatalf("seed lot: %v", err)
	}
	txn := &auction.Transaction{
		AuctionID: "auction-1", WinnerID: "bidder-2", AmountCents: 2000,
		Status: auction.TransactionPending, CreatedAt: txBaseTime, ExpiresAt: txBaseTime.Add(48 * time.Hour),
	}
	if err := repo.CreateTransaction(context.Background(), txn); err != nil {
		t.Fatalf("seed transaction: %v", err)
	}
	clock := &mutableClock{t: txBaseTime.Add(time.Hour)}
	svc := auction.NewService(repo, clock, nil)

	// Declined: an outcome, not an error — persisted as failed.
	got, err := svc.PayTransaction(context.Background(), "bidder-2", txn.ID, auction.DeclinedCardNumber)
	if err != nil {
		t.Fatalf("declined attempt: %v", err)
	}
	if got.Status != auction.TransactionFailed {
		t.Fatalf("decline status = %q, want failed", got.Status)
	}
	stored, _ := repo.TransactionByID(context.Background(), txn.ID)
	if stored.Status != auction.TransactionFailed {
		t.Fatalf("stored status = %q, want the decline persisted", stored.Status)
	}

	// Retry with a good card completes the same transaction.
	got, err = svc.PayTransaction(context.Background(), "bidder-2", txn.ID, "4111111111111111")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got.Status != auction.TransactionCompleted {
		t.Errorf("retry status = %q, want completed", got.Status)
	}
}

func TestListPurchasesCarriesTransactions(t *testing.T) {
	repo := newTransactionRepo()
	won := soldLot("auction-won", "bidder-2", 2600)
	if err := repo.Create(context.Background(), won); err != nil {
		t.Fatalf("seed won lot: %v", err)
	}
	lost := soldLot("auction-lost", "bidder-1", 1300) // bidder-2 did not win this one
	if err := repo.Create(context.Background(), lost); err != nil {
		t.Fatalf("seed lost lot: %v", err)
	}
	txn := &auction.Transaction{
		AuctionID: "auction-won", WinnerID: "bidder-2", AmountCents: 2600,
		Status: auction.TransactionPending, CreatedAt: txBaseTime, ExpiresAt: txBaseTime.Add(48 * time.Hour),
	}
	if err := repo.CreateTransaction(context.Background(), txn); err != nil {
		t.Fatalf("seed transaction: %v", err)
	}
	svc := auction.NewService(repo, &mutableClock{t: txBaseTime}, nil)

	page, err := svc.ListPurchases(context.Background(), "bidder-2", 1, 20)
	if err != nil {
		t.Fatalf("ListPurchases: %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("total=%d items=%d, want only the won lot", page.Total, len(page.Items))
	}
	item := page.Items[0]
	if item.ID != "auction-won" {
		t.Fatalf("item = %s, want auction-won", item.ID)
	}
	if item.Transaction.ID != txn.ID {
		t.Errorf("transaction id = %q, want %q", item.Transaction.ID, txn.ID)
	}
	if item.Transaction.Status != auction.TransactionPending {
		t.Errorf("transaction status = %q, want pending", item.Transaction.Status)
	}
	if item.Transaction.AmountCents != 2600 {
		t.Errorf("transaction amount = %d, want the final price 2600", item.Transaction.AmountCents)
	}
	if item.Transaction.PaidAt != nil {
		t.Errorf("transaction paid_at = %v, want nil", item.Transaction.PaidAt)
	}
}

func TestExpireDueTransactionsSweep(t *testing.T) {
	repo := newTransactionRepo()
	mk := func(id string, status auction.Status, expiresIn time.Duration) *auction.Transaction {
		return &auction.Transaction{
			ID: id, AuctionID: "auction-1", WinnerID: "bidder-1", AmountCents: 1000,
			Status: status, CreatedAt: txBaseTime, ExpiresAt: txBaseTime.Add(expiresIn),
		}
	}
	for _, txn := range []*auction.Transaction{
		mk("tx-pending-due", auction.TransactionPending, time.Hour),
		mk("tx-failed-due", auction.TransactionFailed, time.Hour),
		mk("tx-pending-open", auction.TransactionPending, 48*time.Hour),
		mk("tx-completed", auction.TransactionCompleted, time.Hour), // terminal: never moved
	} {
		if err := repo.CreateTransaction(context.Background(), txn); err != nil {
			t.Fatalf("seed %s: %v", txn.ID, err)
		}
	}
	clock := &mutableClock{t: txBaseTime.Add(2 * time.Hour)}
	svc := auction.NewService(repo, clock, nil)

	count, err := svc.ExpireDueTransactions(context.Background())
	if err != nil {
		t.Fatalf("ExpireDueTransactions: %v", err)
	}
	if count != 2 {
		t.Errorf("expired count = %d, want 2 (pending-due + failed-due)", count)
	}
	for id, want := range map[string]auction.Status{
		"tx-pending-due":  auction.TransactionExpired,
		"tx-failed-due":   auction.TransactionExpired,
		"tx-pending-open": auction.TransactionPending,
		"tx-completed":    auction.TransactionCompleted,
	} {
		got, err := repo.TransactionByID(context.Background(), id)
		if err != nil {
			t.Fatalf("load %s: %v", id, err)
		}
		if got.Status != want {
			t.Errorf("%s status = %q, want %q", id, got.Status, want)
		}
	}
}
