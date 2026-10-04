// Package seed creates the demo landscape for the reviewer-facing stack:
// three demo accounts and five auctions by the demo seller (two live bid
// wars, one zero-bid lot about to close, one closed-and-paid sale for the
// seller dashboard, one closed unsold lot below its reserve). It is wired
// only from the composition root, gated by SEED_DEMO, and is idempotent:
// once the demo seller exists it logs and skips.
//
// Honesty rules: users go through the auth ports (bcrypt hashing included —
// never bypassed), auctions are built by auction.NewAuction (all creation
// rules) and closed by the pure Auction.Close, the seeded bid history is
// validated against the domain bidding rules before insert, and the completed
// payment is stored through the same CreateTransaction port the closing sweep
// uses. The one extra capability beyond the normal lifecycle is
// AuctionStore.SeedAuction (implemented by the postgres adapter), which
// persists auctions with explicit history timestamps — finished lots the
// create/place-bid/close flow cannot express.
package seed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
	"github.com/FranklinF25/auction-platform/apps/api/internal/auth"
)

// Demo accounts (documented for reviewers): one seller, two bidders, one
// shared password.
const (
	sellerEmail  = "demo-seller@auction.dev"
	bidderEmail  = "demo-bidder@auction.dev"
	rivalEmail   = "demo-rival@auction.dev"
	demoPassword = "demo-password"
)

// UserStore is the auth slice the seed needs: persist users (password already
// hashed through PasswordHasher — the seed never bypasses hashing) and probe
// for the idempotency check. *postgres.UserRepository satisfies it.
type UserStore interface {
	Create(ctx context.Context, u auth.User) (auth.User, error)
	ByEmail(ctx context.Context, email string) (auth.User, error)
}

// AuctionStore is the auction slice the seed needs: auctions with explicit
// history (finished lots the normal lifecycle cannot express) and the
// transactions port the closing sweep also uses. *postgres.AuctionRepository
// satisfies it.
type AuctionStore interface {
	SeedAuction(ctx context.Context, a *auction.Auction, bids []auction.Bid) error
	CreateTransaction(ctx context.Context, t *auction.Transaction) error
}

// Deps carries the ports Run works through.
type Deps struct {
	Users    UserStore
	Hasher   auth.PasswordHasher
	Auctions AuctionStore
}

// Run seeds the demo landscape if it is not present yet. now is the seed
// instant the demo timeline hangs off (the composition root passes the system
// clock). It returns nil without touching anything when the demo seller
// already exists.
func Run(ctx context.Context, deps Deps, logger *slog.Logger, now time.Time) error {
	if logger == nil {
		logger = slog.Default()
	}

	// Idempotency gate: the demo seller existing means the landscape is in.
	if _, err := deps.Users.ByEmail(ctx, sellerEmail); err == nil {
		logger.Info("seed: already applied")
		return nil
	} else if !errors.Is(err, auth.ErrNotFound) {
		return fmt.Errorf("seed probe: %w", err)
	}

	hash, err := deps.Hasher.Hash(demoPassword)
	if err != nil {
		return fmt.Errorf("hash demo password: %w", err)
	}
	user := func(email, name string) (auth.User, error) {
		return deps.Users.Create(ctx, auth.User{Email: email, PasswordHash: hash, Name: name})
	}
	seller, err := user(sellerEmail, "Demo Seller")
	if err != nil {
		return fmt.Errorf("create demo seller: %w", err)
	}
	bidder, err := user(bidderEmail, "Demo Bidder")
	if err != nil {
		return fmt.Errorf("create demo bidder: %w", err)
	}
	rival, err := user(rivalEmail, "Demo Rival")
	if err != nil {
		return fmt.Errorf("create demo rival: %w", err)
	}

	for _, lot := range landscape(seller.ID, bidder.ID, rival.ID, now) {
		if err := seedLot(ctx, deps.Auctions, lot); err != nil {
			return fmt.Errorf("seed %q: %w", lot.auction.Title, err)
		}
	}

	logger.Info("seed: demo data created",
		"users", 3, "auctions", 5,
		"logins", sellerEmail+" / "+demoPassword)
	return nil
}

// lot is one demo auction: the domain aggregate plus its seeded bid history
// and, for a sold close, the already-completed payment (so the seller
// dashboard shows revenue immediately). Auction is fully built (active or
// closed) before seedLot persists it.
type lot struct {
	auction     *auction.Auction
	bids        []auction.Bid
	transaction *auction.Transaction
}

// landscape builds the five demo lots around now:
//
//	vinyl     active, four-bid war between the bidders, ends in ~4 minutes
//	camera    active, three-bid war, ends in ~15 minutes
//	espresso  active, zero bids, ends in ~2 minutes
//	keyboard  closed sold two hours ago, completed payment (seller revenue)
//	bike      closed unsold three hours ago (bids below the reserve)
//
// Every offset is a whole number of minutes so NewAuction's duration math
// lands exactly on the intended ends_at.
func landscape(sellerID, bidderID, rivalID string, now time.Time) []lot {
	reserve := func(v int64) *int64 { return &v }
	bid := func(bidder string, amount int64, at time.Time) auction.Bid {
		return auction.Bid{BidderID: bidder, AmountCents: amount, CreatedAt: at}
	}
	// mustAuction builds a creation-valid auction spanning created..ends. A
	// failure here is a bug in the demo data, not user input.
	mustAuction := func(title, description string, starting, increment int64, res *int64, created, ends time.Time) *auction.Auction {
		a, err := auction.NewAuction(sellerID, auction.CreateInput{
			Title:              title,
			Description:        description,
			StartingPriceCents: starting,
			MinIncrementCents:  increment,
			ReservePriceCents:  res,
			DurationMinutes:    int(ends.Sub(created).Minutes()),
		}, created)
		if err != nil {
			panic(err)
		}
		return a
	}
	m := time.Minute

	// (a1) Vinyl: live bid war ending ~4 minutes after seed.
	vinyl := mustAuction(
		"Miles Davis — Kind of Blue, original 1959 Columbia mono pressing",
		"First-state 6-eye labels with deep grooves on both sides. Vinyl plays EX "+
			"with a touch of surface noise under the horn passages; sleeves show "+
			"light ring wear. Original inner sleeve included.",
		2000, 100, nil, now.Add(-24*m), now.Add(4*m))
	vinylBids := []auction.Bid{
		bid(bidderID, 2500, now.Add(-20*m)),
		bid(rivalID, 2700, now.Add(-16*m)),
		bid(bidderID, 3000, now.Add(-12*m)),
		bid(rivalID, 3300, now.Add(-8*m)),
	}

	// (a2) Camera: live bid war ending ~15 minutes after seed.
	camera := mustAuction(
		"Canon AE-1 35mm film camera with FD 50mm f/1.8 lens",
		"Fully serviced last month: new light seals, accurate meter, all shutter "+
			"speeds verified on a tester. Lens glass is clean and clear, no fungus "+
			"or haze; focus is smooth. Includes a fresh battery and a body cap.",
		8000, 250, nil, now.Add(-45*m), now.Add(15*m))
	cameraBids := []auction.Bid{
		bid(bidderID, 8500, now.Add(-40*m)),
		bid(rivalID, 9000, now.Add(-25*m)),
		bid(bidderID, 10500, now.Add(-10*m)),
	}

	// (b) Espresso machine: live, zero bids, ending ~2 minutes after seed.
	espresso := mustAuction(
		"Rancilio Silvia espresso machine, descaled and barely used",
		"Single owner, pulled two shots a day for a year. Descaled last week; "+
			"comes with the original portafilter, a double basket and a tamper. "+
			"Ready to dial in on day one.",
		15000, 250, nil, now.Add(-10*m), now.Add(2*m))

	// (c) Keyboard: closed sold two hours ago, payment already completed so
	// the seller dashboard shows revenue immediately.
	keyboard := mustAuction(
		"Custom mechanical keyboard — lubed Holy Pandas, brass plate",
		"Hand-built 65%: lubed Holy Panda switches on a brass plate, Durock "+
			"stabilizers, frosted acrylic case with a coconut-themed keycap set. "+
			"Types like butter; sounds even better. USB-C, QMK-compatible.",
		4000, 100, nil, now.Add(-180*m), now.Add(-120*m))
	keyboardBids := []auction.Bid{
		bid(rivalID, 4500, now.Add(-150*m)),
		bid(bidderID, 5000, now.Add(-130*m)),
	}
	keyboard.LoadBids(keyboardBids)
	winner, won, err := keyboard.Close(keyboard.EndsAt) // honest domain close
	if err != nil || !won {
		panic("demo keyboard lot must close sold")
	}
	closedAt := *keyboard.ClosedAt
	paidAt := closedAt.Add(3 * time.Minute)
	keyboardTx := &auction.Transaction{
		WinnerID:    winner.BidderID,
		AmountCents: keyboard.CurrentPrice(), // final price at close
		Status:      auction.TransactionCompleted,
		CreatedAt:   closedAt,
		ExpiresAt:   closedAt.Add(auction.TransactionExpiryDuration),
		PaidAt:      &paidAt,
	}

	// (d) Bike: closed unsold three hours ago — bids below the hidden reserve.
	bike := mustAuction(
		"Trek Marlin mountain bike, size M, freshly serviced",
		"21-speed hardtail in honest used condition: new chain, fresh brake pads "+
			"and wheels trued this week. Frame has scratches from trail use, "+
			"nothing structural. The reserve reflects the service bill.",
		10000, 250, reserve(20000), now.Add(-240*m), now.Add(-180*m))
	bikeBids := []auction.Bid{
		bid(bidderID, 10500, now.Add(-210*m)),
		bid(rivalID, 11000, now.Add(-195*m)),
	}
	bike.LoadBids(bikeBids)
	if _, won, err := bike.Close(bike.EndsAt); err != nil || won {
		panic("demo bike lot must close unsold below its reserve")
	}

	return []lot{
		{auction: vinyl, bids: vinylBids},
		{auction: camera, bids: cameraBids},
		{auction: espresso},
		{auction: keyboard, bids: keyboardBids, transaction: keyboardTx},
		{auction: bike, bids: bikeBids},
	}
}

// seedLot validates the bid history against the domain rules and persists the
// lot: auction + bids in one SeedAuction call, then the completed payment
// through CreateTransaction when the lot sold.
func seedLot(ctx context.Context, store AuctionStore, l lot) error {
	if err := validateBids(l.auction, l.bids); err != nil {
		return err
	}
	if err := store.SeedAuction(ctx, l.auction, l.bids); err != nil {
		return err
	}
	if l.transaction != nil {
		l.transaction.AuctionID = l.auction.ID
		return store.CreateTransaction(ctx, l.transaction)
	}
	return nil
}

// validateBids checks the seeded history respects the domain bidding rules
// the live path enforces in Auction.PlaceBid: the seller never bids, every
// bid lands before ends_at, and each bid is at least the current price plus
// the minimum increment. The soft-close extension is the one rule not
// replayed: the seed pins ends_at explicitly so demo lots end on schedule.
func validateBids(a *auction.Auction, bids []auction.Bid) error {
	price := a.StartingPriceCents
	for i, b := range bids {
		if b.BidderID == a.SellerID {
			return fmt.Errorf("bid %d: the seller cannot bid on their own auction", i)
		}
		if !b.CreatedAt.Before(a.EndsAt) {
			return fmt.Errorf("bid %d: placed at or after ends_at", i)
		}
		if b.AmountCents < price+a.MinIncrementCents {
			return fmt.Errorf("bid %d (%d cents): below the minimum %d (price %d + increment %d)",
				i, b.AmountCents, price+a.MinIncrementCents, price, a.MinIncrementCents)
		}
		price = b.AmountCents
	}
	return nil
}
