package seed_test

// The demo seed: idempotency (the second run is a no-op once the demo seller
// exists), the user landscape (hashed passwords through the port), and the
// auction landscape (two live bid wars, one zero-bid lot, one closed sold lot
// with a completed payment, one closed unsold lot below its reserve).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
	"github.com/FranklinF25/auction-platform/apps/api/internal/auth"
	"github.com/FranklinF25/auction-platform/apps/api/internal/seed"
)

var seedBaseTime = time.Date(2025, 9, 1, 12, 0, 0, 0, time.UTC)

// fakeHasher stands in for bcrypt so the test stays fast; the port is the
// point — the seed must never see a raw password reach storage.
type fakeHasher struct{}

func (fakeHasher) Hash(p string) (string, error) { return "fake-hash(" + p + ")", nil }

func (fakeHasher) Compare(hash, p string) error {
	if hash == "fake-hash("+p+")" {
		return nil
	}
	return errors.New("password mismatch")
}

type fakeUsers struct {
	users  []auth.User
	nextID int
}

func (f *fakeUsers) Create(_ context.Context, u auth.User) (auth.User, error) {
	for _, e := range f.users {
		if e.Email == u.Email {
			return auth.User{}, auth.ErrEmailTaken
		}
	}
	f.nextID++
	u.ID = fmt.Sprintf("user-%d", f.nextID)
	u.CreatedAt = seedBaseTime
	f.users = append(f.users, u)
	return u, nil
}

func (f *fakeUsers) ByEmail(_ context.Context, email string) (auth.User, error) {
	for _, u := range f.users {
		if u.Email == email {
			return u, nil
		}
	}
	return auth.User{}, auth.ErrNotFound
}

type fakeAuctions struct {
	auctions      []*auction.Auction
	transactions  []*auction.Transaction
	nextAuctionID int
	nextBidID     int
	nextTxID      int
}

func (f *fakeAuctions) SeedAuction(_ context.Context, a *auction.Auction, bids []auction.Bid) error {
	f.nextAuctionID++
	a.ID = fmt.Sprintf("auction-%d", f.nextAuctionID)
	for i := range bids {
		bids[i].AuctionID = a.ID
		f.nextBidID++
		bids[i].ID = fmt.Sprintf("bid-%d", f.nextBidID)
	}
	a.LoadBids(bids)
	f.auctions = append(f.auctions, a)
	return nil
}

func (f *fakeAuctions) CreateTransaction(_ context.Context, t *auction.Transaction) error {
	f.nextTxID++
	if t.ID == "" {
		t.ID = fmt.Sprintf("tx-%d", f.nextTxID)
	}
	f.transactions = append(f.transactions, t)
	return nil
}

func runSeed(t *testing.T, users *fakeUsers, auctions *fakeAuctions) {
	t.Helper()
	deps := seed.Deps{Users: users, Hasher: fakeHasher{}, Auctions: auctions}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := seed.Run(context.Background(), deps, logger, seedBaseTime); err != nil {
		t.Fatalf("seed.Run: %v", err)
	}
}

func TestSeedCreatesDemoLandscape(t *testing.T) {
	users := &fakeUsers{}
	auctions := &fakeAuctions{}
	runSeed(t, users, auctions)

	if got := len(users.users); got != 3 {
		t.Fatalf("users = %d, want 3 (seller, bidder, rival)", got)
	}
	emails := map[string]bool{}
	for _, u := range users.users {
		emails[u.Email] = true
		if u.PasswordHash == "demo-password" || u.PasswordHash == "" {
			t.Errorf("%s: password stored without hashing (%q)", u.Email, u.PasswordHash)
		}
		if !strings.HasPrefix(u.PasswordHash, "fake-hash(") {
			t.Errorf("%s: password hash did not come from the hasher port", u.Email)
		}
		if u.ID == "" {
			t.Errorf("%s: no id assigned", u.Email)
		}
	}
	for _, want := range []string{"demo-seller@auction.dev", "demo-bidder@auction.dev", "demo-rival@auction.dev"} {
		if !emails[want] {
			t.Errorf("demo user %s missing", want)
		}
	}

	if got := len(auctions.auctions); got != 5 {
		t.Fatalf("auctions = %d, want 5", got)
	}

	byTitle := map[string]*auction.Auction{}
	for _, a := range auctions.auctions {
		byTitle[a.Title] = a
		if a.SellerID != users.users[0].ID {
			t.Errorf("%s: seller = %q, want the demo seller", a.Title, a.SellerID)
		}
	}

	t.Run("two live bid wars and one zero-bid lot", func(t *testing.T) {
		vinyl := byTitle["Miles Davis — Kind of Blue, original 1959 Columbia mono pressing"]
		if vinyl == nil || vinyl.Status != auction.StatusActive {
			t.Fatal("vinyl lot missing or not active")
		}
		if !vinyl.EndsAt.Equal(seedBaseTime.Add(4 * time.Minute)) {
			t.Errorf("vinyl ends_at = %v, want ~4 minutes after seed", vinyl.EndsAt)
		}
		if got := vinyl.BidCount(); got != 4 {
			t.Errorf("vinyl bids = %d, want a 4-bid war", got)
		}
		bids := vinyl.Bids()
		for i := 1; i < len(bids); i++ {
			if !bids[i].CreatedAt.After(bids[i-1].CreatedAt) {
				t.Errorf("vinyl bid %d not placed after bid %d (history must spread)", i, i-1)
			}
		}
		if !bids[0].CreatedAt.Before(seedBaseTime) || !bids[len(bids)-1].CreatedAt.Before(seedBaseTime) {
			t.Error("vinyl bids must be spread in the past")
		}
		if highest, _ := vinyl.HighestBid(); highest.AmountCents != 3300 {
			t.Errorf("vinyl leader = %d, want 3300", highest.AmountCents)
		}

		camera := byTitle["Canon AE-1 35mm film camera with FD 50mm f/1.8 lens"]
		if camera == nil || camera.Status != auction.StatusActive {
			t.Fatal("camera lot missing or not active")
		}
		if !camera.EndsAt.Equal(seedBaseTime.Add(15 * time.Minute)) {
			t.Errorf("camera ends_at = %v, want ~15 minutes after seed", camera.EndsAt)
		}
		if got := camera.BidCount(); got != 3 {
			t.Errorf("camera bids = %d, want 3", got)
		}

		espresso := byTitle["Rancilio Silvia espresso machine, descaled and barely used"]
		if espresso == nil || espresso.Status != auction.StatusActive {
			t.Fatal("espresso lot missing or not active")
		}
		if !espresso.EndsAt.Equal(seedBaseTime.Add(2 * time.Minute)) {
			t.Errorf("espresso ends_at = %v, want ~2 minutes after seed", espresso.EndsAt)
		}
		if got := espresso.BidCount(); got != 0 {
			t.Errorf("espresso bids = %d, want the zero-bid lot", got)
		}
	})

	t.Run("closed sold lot carries a completed payment", func(t *testing.T) {
		keyboard := byTitle["Custom mechanical keyboard — lubed Holy Pandas, brass plate"]
		if keyboard == nil || keyboard.Status != auction.StatusClosed {
			t.Fatal("keyboard lot missing or not closed")
		}
		if keyboard.ClosedAt == nil || !keyboard.ClosedAt.Equal(seedBaseTime.Add(-120*time.Minute)) {
			t.Errorf("keyboard closed_at = %v, want two hours before seed", keyboard.ClosedAt)
		}
		highest, ok := keyboard.HighestBid()
		if !ok || highest.AmountCents != 5000 {
			t.Fatalf("keyboard winner bid = %v, want 5000", highest)
		}

		if got := len(auctions.transactions); got != 1 {
			t.Fatalf("transactions = %d, want exactly 1 (the sold lot)", got)
		}
		txn := auctions.transactions[0]
		if txn.AuctionID != keyboard.ID {
			t.Errorf("transaction auction = %q, want the keyboard lot", txn.AuctionID)
		}
		if txn.WinnerID != highest.BidderID {
			t.Errorf("transaction winner = %q, want the highest bidder", txn.WinnerID)
		}
		if txn.AmountCents != 5000 {
			t.Errorf("transaction amount = %d, want the final price 5000", txn.AmountCents)
		}
		if txn.Status != auction.TransactionCompleted || txn.PaidAt == nil {
			t.Errorf("transaction = %+v, want completed with paid_at", txn)
		}
		if want := keyboard.ClosedAt.Add(auction.TransactionExpiryDuration); !txn.ExpiresAt.Equal(want) {
			t.Errorf("transaction expires_at = %v, want close + 48h (%v)", txn.ExpiresAt, want)
		}
		if !txn.PaidAt.After(*keyboard.ClosedAt) {
			t.Errorf("paid_at %v must follow the close %v", *txn.PaidAt, *keyboard.ClosedAt)
		}
	})

	t.Run("closed unsold lot stays below its reserve", func(t *testing.T) {
		bike := byTitle["Trek Marlin mountain bike, size M, freshly serviced"]
		if bike == nil || bike.Status != auction.StatusClosed {
			t.Fatal("bike lot missing or not closed")
		}
		if bike.ReserveMet() {
			t.Error("bike reserve met, want the top bid below the hidden reserve")
		}
		if got := bike.BidCount(); got != 2 {
			t.Errorf("bike bids = %d, want 2", got)
		}
	})
}

func TestSeedIsIdempotent(t *testing.T) {
	users := &fakeUsers{}
	auctions := &fakeAuctions{}
	runSeed(t, users, auctions)

	// Second run: the demo seller exists, so nothing new is created.
	runSeed(t, users, auctions)

	if got := len(users.users); got != 3 {
		t.Errorf("users after reseed = %d, want still 3", got)
	}
	if got := len(auctions.auctions); got != 5 {
		t.Errorf("auctions after reseed = %d, want still 5", got)
	}
	if got := len(auctions.transactions); got != 1 {
		t.Errorf("transactions after reseed = %d, want still 1", got)
	}
}
