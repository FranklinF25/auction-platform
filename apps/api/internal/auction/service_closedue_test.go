package auction_test

// Use-case tests for Service.CloseDue (the M3 close sweep): the service issues
// the repository command and publishes auction.closed only after CloseDue has
// returned — i.e. after every per-auction transaction has committed. The fake
// publisher checks the stored state at publish time, so the "never publish
// before commit" invariant is asserted, not assumed.

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
)

// closeDueRepo is an in-memory Repository whose CloseDue mirrors the
// production adapter: run the pure domain Close on every active, due auction
// and write the post-close state back before returning. fail forces a
// repository-level error to exercise the service's error path.
type closeDueRepo struct {
	mu     sync.Mutex
	stored map[string]*auction.Auction
	fail   bool
}

func newCloseDueRepo(auctions ...*auction.Auction) *closeDueRepo {
	r := &closeDueRepo{stored: map[string]*auction.Auction{}}
	for _, a := range auctions {
		r.stored[a.ID] = a
	}
	return r
}

func (f *closeDueRepo) Create(_ context.Context, a *auction.Auction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stored[a.ID] = a
	return nil
}

func (f *closeDueRepo) ByID(_ context.Context, id string) (*auction.Auction, error) {
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

func (f *closeDueRepo) List(_ context.Context, _ auction.ListFilter) ([]auction.ListItem, int, error) {
	return nil, 0, nil
}

func (f *closeDueRepo) ListBids(_ context.Context, _ string, _, _ int) ([]auction.Bid, int, error) {
	return nil, 0, nil
}

func (f *closeDueRepo) PlaceBid(_ context.Context, _, _ string, _ int64, _ time.Time) (*auction.PlacedBid, error) {
	return nil, nil
}

func (f *closeDueRepo) ListWon(_ context.Context, _ string, _, _ int) ([]auction.ListItem, int, error) {
	return nil, 0, nil
}

func (f *closeDueRepo) CloseDue(_ context.Context, now time.Time) ([]auction.ClosedAuction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, errors.New("repository exploded")
	}

	ids := make([]string, 0, len(f.stored))
	for id := range f.stored {
		ids = append(ids, id)
	}
	// Deterministic order matching the pgx sweep: ends_at, then id.
	sort.Slice(ids, func(i, j int) bool {
		a, b := f.stored[ids[i]], f.stored[ids[j]]
		if a.EndsAt.Equal(b.EndsAt) {
			return a.ID < b.ID
		}
		return a.EndsAt.Before(b.EndsAt)
	})

	results := []auction.ClosedAuction{}
	for _, id := range ids {
		a := f.stored[id]
		cp := *a
		cp.LoadBids(a.Bids())
		winner, won, err := cp.Close(now)
		if err != nil {
			continue // not active or not due: skip, not fail
		}
		f.stored[id] = &cp // COMMIT: post-close state is durable from here on
		results = append(results, auction.ClosedAuction{Auction: &cp, Winner: winner, Won: won})
	}
	return results, nil
}

// closeEventPublisher records every event and, at publish time, verifies the
// referenced auction is already closed in the repository (post-commit proof).
type closeEventPublisher struct {
	mu       sync.Mutex
	repo     *closeDueRepo
	events   []auction.Event
	openAtTX int // events published while the auction was not yet stored closed
}

func (p *closeEventPublisher) Publish(evt auction.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	id, _ := evt.Data["auction_id"].(string)
	if a := p.repo.stored[id]; a == nil || a.Status != auction.StatusClosed {
		p.openAtTX++
	}
	p.events = append(p.events, evt)
}

func (p *closeEventPublisher) snapshot() (events []auction.Event, openAtTX int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]auction.Event{}, p.events...), p.openAtTX
}

var closeBaseTime = time.Date(2025, 7, 1, 12, 0, 0, 0, time.UTC)

// seedLot returns an active auction ending 10 minutes after closeBaseTime,
// pre-seeded with the given bids.
func seedLot(id string, reserve *int64, bids ...auction.Bid) *auction.Auction {
	a, err := auction.NewAuction("seller-1", auction.CreateInput{
		Title:              "Close lot " + id,
		StartingPriceCents: 1000,
		MinIncrementCents:  100,
		ReservePriceCents:  reserve,
		DurationMinutes:    10,
	}, closeBaseTime)
	if err != nil {
		panic(err)
	}
	a.ID = id
	a.LoadBids(bids)
	return a
}

func bidOf(bidder, name string, amount int64, at time.Time) auction.Bid {
	return auction.Bid{BidderID: bidder, BidderName: name, AmountCents: amount, CreatedAt: at}
}

func int64Ptr(v int64) *int64 { return &v }

func TestCloseDuePublishesClosedEventsAfterCommit(t *testing.T) {
	sold := seedLot("auction-sold", nil,
		bidOf("bidder-1", "Ada", 1100, closeBaseTime.Add(time.Minute)),
		bidOf("bidder-2", "Grace", 2000, closeBaseTime.Add(2*time.Minute)))
	unsoldReserve := seedLot("auction-unsold-reserve", int64Ptr(5000),
		bidOf("bidder-1", "Ada", 3000, closeBaseTime.Add(time.Minute)))
	unsoldNoBids := seedLot("auction-unsold-nobids", nil)
	stillRunning := seedLot("auction-running", nil)
	// Ends far in the future, so the sweep must leave it active.
	stillRunning.EndsAt = closeBaseTime.Add(time.Hour)
	alreadyClosed := seedLot("auction-preclosed", nil)
	alreadyClosed.Status = auction.StatusClosed

	repo := newCloseDueRepo(sold, unsoldReserve, unsoldNoBids, stillRunning, alreadyClosed)
	pub := &closeEventPublisher{repo: repo}
	clock := &mutableClock{t: closeBaseTime.Add(15 * time.Minute)} // past ends_at
	svc := auction.NewService(repo, clock, pub)

	if err := svc.CloseDue(context.Background()); err != nil {
		t.Fatalf("CloseDue: %v", err)
	}

	// Persistence: due auctions closed, the running one untouched.
	for _, id := range []string{"auction-sold", "auction-unsold-reserve", "auction-unsold-nobids"} {
		stored, err := repo.ByID(context.Background(), id)
		if err != nil {
			t.Fatalf("reload %s: %v", id, err)
		}
		if stored.Status != auction.StatusClosed || stored.ClosedAt == nil {
			t.Errorf("%s: status=%q closed_at=%v, want closed with closed_at set", id, stored.Status, stored.ClosedAt)
		}
	}
	running, _ := repo.ByID(context.Background(), "auction-running")
	if running.Status != auction.StatusActive {
		t.Errorf("running auction was closed: status=%q", running.Status)
	}

	events, openAtTX := pub.snapshot()
	if openAtTX != 0 {
		t.Errorf("%d events were published before the close state was committed", openAtTX)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3 (sold + unsold-reserve + unsold-nobids): %+v", len(events), events)
	}

	byID := map[string]auction.Event{}
	for _, evt := range events {
		if evt.Type != auction.EventAuctionClosed {
			t.Errorf("event type = %q, want %q", evt.Type, auction.EventAuctionClosed)
		}
		if evt.AuctionID != evt.Data["auction_id"] {
			t.Errorf("routing key %q does not match data.auction_id %v", evt.AuctionID, evt.Data["auction_id"])
		}
		byID[evt.AuctionID] = evt
	}

	soldEvt := byID["auction-sold"]
	if got, ok := soldEvt.Data["winner_name"].(string); !ok || got != "Grace" {
		t.Errorf("sold winner_name = %v, want Grace", soldEvt.Data["winner_name"])
	}
	if soldEvt.Data["sold"] != true {
		t.Errorf("sold = %v, want true", soldEvt.Data["sold"])
	}
	if soldEvt.Data["final_price_cents"] != int64(2000) {
		t.Errorf("final_price_cents = %v, want 2000", soldEvt.Data["final_price_cents"])
	}
	if soldEvt.Data["status"] != "closed" {
		t.Errorf("status = %v, want closed", soldEvt.Data["status"])
	}
	if got, ok := soldEvt.Data["server_now"].(time.Time); !ok || !got.Equal(clock.Now()) {
		t.Errorf("server_now = %v, want %v", soldEvt.Data["server_now"], clock.Now())
	}

	for _, id := range []string{"auction-unsold-reserve", "auction-unsold-nobids"} {
		evt := byID[id]
		if evt.Data["winner_name"] != nil {
			t.Errorf("%s winner_name = %v, want nil", id, evt.Data["winner_name"])
		}
		if evt.Data["sold"] != false {
			t.Errorf("%s sold = %v, want false", id, evt.Data["sold"])
		}
	}
	// The unsold-reserve final price is the (below-reserve) top bid; the
	// zero-bid one falls back to the starting price.
	if byID["auction-unsold-reserve"].Data["final_price_cents"] != int64(3000) {
		t.Errorf("unsold-reserve final_price_cents = %v, want 3000", byID["auction-unsold-reserve"].Data["final_price_cents"])
	}
	if byID["auction-unsold-nobids"].Data["final_price_cents"] != int64(1000) {
		t.Errorf("unsold-nobids final_price_cents = %v, want 1000", byID["auction-unsold-nobids"].Data["final_price_cents"])
	}
}

func TestCloseDueWithNilPublisherIsSilent(t *testing.T) {
	due := seedLot("auction-sold", nil, bidOf("bidder-1", "Ada", 1100, closeBaseTime.Add(time.Minute)))
	repo := newCloseDueRepo(due)
	clock := &mutableClock{t: closeBaseTime.Add(15 * time.Minute)}
	svc := auction.NewService(repo, clock, nil) // nil publisher disables events

	if err := svc.CloseDue(context.Background()); err != nil {
		t.Fatalf("CloseDue with nil publisher: %v", err)
	}
	stored, _ := repo.ByID(context.Background(), "auction-sold")
	if stored.Status != auction.StatusClosed {
		t.Errorf("status = %q, want closed (repo command still runs)", stored.Status)
	}
}

func TestCloseDueRepositoryErrorSurfacesAndPublishesNothing(t *testing.T) {
	repo := newCloseDueRepo(seedLot("auction-sold", nil))
	repo.fail = true
	pub := &closeEventPublisher{repo: repo}
	clock := &mutableClock{t: closeBaseTime.Add(15 * time.Minute)}
	svc := auction.NewService(repo, clock, pub)

	if err := svc.CloseDue(context.Background()); err == nil {
		t.Fatal("repository error must surface to the caller")
	}
	events, _ := pub.snapshot()
	if len(events) != 0 {
		t.Errorf("%d events published on a failed sweep, want 0", len(events))
	}
}

func TestCloseDueEmptySweepIsNoop(t *testing.T) {
	repo := newCloseDueRepo()
	pub := &closeEventPublisher{repo: repo}
	clock := &mutableClock{t: closeBaseTime}
	svc := auction.NewService(repo, clock, pub)

	if err := svc.CloseDue(context.Background()); err != nil {
		t.Fatalf("CloseDue on empty repo: %v", err)
	}
	events, _ := pub.snapshot()
	if len(events) != 0 {
		t.Errorf("got %d events on an empty sweep, want 0", len(events))
	}
}
