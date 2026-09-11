package auction_test

// Use-case tests for Service.PlaceBid with a fake repository and a fake
// publisher. The fake repo exposes a committed flag that flips only when its
// transaction "commits" (state written back), so the fake publisher can flag
// any event published before commit — the invariant "NEVER publish before
// commit" is asserted, not assumed.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
)

// mutableClock is a fake Clock the test can advance deterministically.
type mutableClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *mutableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *mutableClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// placeBidRepo is an in-memory Repository. PlaceBid mirrors the production
// adapter's contract: load, run the pure domain rules, assign ID + bidder
// name, write back (commit), report whether ends_at moved.
type placeBidRepo struct {
	mu        sync.Mutex
	stored    map[string]*auction.Auction
	names     map[string]string
	nextID    int
	committed bool
}

func newPlaceBidRepo(a *auction.Auction) *placeBidRepo {
	return &placeBidRepo{
		stored: map[string]*auction.Auction{a.ID: a},
		names:  map[string]string{"bidder-1": "Ada", "seller-1": "Sue"},
	}
}

func (f *placeBidRepo) isCommitted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.committed
}

func (f *placeBidRepo) Create(_ context.Context, a *auction.Auction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	a.ID = fmt.Sprintf("auction-%d", f.nextID)
	f.stored[a.ID] = a
	return nil
}

func (f *placeBidRepo) ByID(_ context.Context, id string) (*auction.Auction, error) {
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

func (f *placeBidRepo) List(_ context.Context, _ auction.ListFilter) ([]auction.ListItem, int, error) {
	return nil, 0, nil
}

func (f *placeBidRepo) ListBids(_ context.Context, _ string, _, _ int) ([]auction.Bid, int, error) {
	return nil, 0, nil
}

func (f *placeBidRepo) CloseDue(_ context.Context, _ time.Time) ([]auction.ClosedAuction, error) {
	return nil, nil
}

func (f *placeBidRepo) ListWon(_ context.Context, _ string, _, _ int) ([]auction.ListItem, int, error) {
	return nil, 0, nil
}

func (f *placeBidRepo) PlaceBid(_ context.Context, auctionID, bidderID string, amountCents int64, now time.Time) (*auction.PlacedBid, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.committed = false // BEGIN: nothing durable yet

	a, ok := f.stored[auctionID]
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

	f.nextID++
	bid.ID = fmt.Sprintf("bid-%d", f.nextID)
	bid.BidderName = f.names[bidderID]

	// COMMIT: the mutated aggregate becomes visible only here.
	bids := cp.Bids()
	bids[len(bids)-1] = bid
	cp.LoadBids(bids)
	f.stored[auctionID] = &cp
	f.committed = true

	return &auction.PlacedBid{Bid: bid, Auction: &cp, Extended: cp.EndsAt.After(prevEndsAt)}, nil
}

// recordingPublisher records every event and flags publishes that happened
// while the repo transaction was not yet committed.
type recordingPublisher struct {
	mu        sync.Mutex
	repo      *placeBidRepo
	events    []auction.Event
	preCommit int
}

func (p *recordingPublisher) Publish(evt auction.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.repo.isCommitted() {
		p.preCommit++
	}
	p.events = append(p.events, evt)
}

func (p *recordingPublisher) snapshot() (events []auction.Event, preCommit int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]auction.Event{}, p.events...), p.preCommit
}

var bidBaseTime = time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)

// seedAuction returns an active auction: starts at 1000 cents, +100 increment,
// running 30 minutes from bidBaseTime.
func seedAuction(modify func(*auction.Auction)) (*auction.Auction, *mutableClock) {
	clock := &mutableClock{t: bidBaseTime}
	a, err := auction.NewAuction("seller-1", auction.CreateInput{
		Title:              "Test lot",
		StartingPriceCents: 1000,
		MinIncrementCents:  100,
		DurationMinutes:    30,
	}, clock.Now())
	if err != nil {
		panic(err)
	}
	a.ID = "auction-1"
	if modify != nil {
		modify(a)
	}
	return a, clock
}

func TestPlaceBidSuccessPersistsAndPublishesAfterCommit(t *testing.T) {
	a, clock := seedAuction(nil)
	repo := newPlaceBidRepo(a)
	pub := &recordingPublisher{repo: repo}
	svc := auction.NewService(repo, clock, pub)
	endsAt := a.EndsAt

	res, err := svc.PlaceBid(context.Background(), auction.PlaceBidInput{
		AuctionID: "auction-1", BidderID: "bidder-1", AmountCents: 1100,
	})
	if err != nil {
		t.Fatalf("PlaceBid: %v", err)
	}

	if res.Bid.ID != "bid-1" {
		t.Errorf("bid ID = %q, want bid-1", res.Bid.ID)
	}
	if res.Bid.BidderName != "Ada" {
		t.Errorf("bidder name = %q, want Ada", res.Bid.BidderName)
	}
	if got := res.Auction.CurrentPrice(); got != 1100 {
		t.Errorf("current price = %d, want 1100", got)
	}
	if res.Extended {
		t.Error("first bid 30 minutes out must not extend ends_at")
	}
	if !res.ServerNow.Equal(bidBaseTime) {
		t.Errorf("server_now = %v, want %v", res.ServerNow, bidBaseTime)
	}

	// Persistence: the stored aggregate carries the bid with its ID and name.
	stored, err := repo.ByID(context.Background(), "auction-1")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.BidCount() != 1 {
		t.Fatalf("stored bid count = %d, want 1", stored.BidCount())
	}
	bids := stored.Bids()
	if bids[0].ID != "bid-1" || bids[0].BidderName != "Ada" || bids[0].AmountCents != 1100 {
		t.Errorf("stored bid = %+v", bids[0])
	}

	// Exactly one event, published after commit, with the pinned payload.
	events, preCommit := pub.snapshot()
	if preCommit != 0 {
		t.Errorf("%d events were published before the transaction committed", preCommit)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(events), events)
	}
	evt := events[0]
	if evt.Type != auction.EventBidPlaced {
		t.Errorf("event type = %q, want %q", evt.Type, auction.EventBidPlaced)
	}
	if evt.AuctionID != "auction-1" {
		t.Errorf("event AuctionID = %q, want auction-1", evt.AuctionID)
	}
	want := map[string]any{
		"auction_id":   "auction-1",
		"bid_id":       "bid-1",
		"bidder_name":  "Ada",
		"amount_cents": int64(1100),
	}
	for k, v := range want {
		if evt.Data[k] != v {
			t.Errorf("event data[%q] = %v, want %v", k, evt.Data[k], v)
		}
	}
	if got, ok := evt.Data["ends_at"].(time.Time); !ok || !got.Equal(endsAt) {
		t.Errorf("event data[ends_at] = %v, want %v", evt.Data["ends_at"], endsAt)
	}
	if got, ok := evt.Data["server_now"].(time.Time); !ok || !got.Equal(bidBaseTime) {
		t.Errorf("event data[server_now] = %v, want %v", evt.Data["server_now"], bidBaseTime)
	}
}

func TestPlaceBidSoftCloseExtendsAndPublishesExtended(t *testing.T) {
	a, clock := seedAuction(nil)
	repo := newPlaceBidRepo(a)
	pub := &recordingPublisher{repo: repo}
	svc := auction.NewService(repo, clock, pub)

	// A bid 30 seconds before the end: inside the final-60s soft close window.
	late := a.EndsAt.Add(-30 * time.Second)
	clock.Set(late)

	res, err := svc.PlaceBid(context.Background(), auction.PlaceBidInput{
		AuctionID: "auction-1", BidderID: "bidder-1", AmountCents: 1500,
	})
	if err != nil {
		t.Fatalf("PlaceBid: %v", err)
	}

	wantEnds := late.Add(60 * time.Second)
	if !res.Extended {
		t.Error("bid inside the soft-close window must report Extended")
	}
	if !res.Auction.EndsAt.Equal(wantEnds) {
		t.Errorf("ends_at = %v, want %v", res.Auction.EndsAt, wantEnds)
	}
	stored, _ := repo.ByID(context.Background(), "auction-1")
	if !stored.EndsAt.Equal(wantEnds) {
		t.Errorf("stored ends_at = %v, want %v", stored.EndsAt, wantEnds)
	}

	events, preCommit := pub.snapshot()
	if preCommit != 0 {
		t.Errorf("%d events published before commit", preCommit)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (bid.placed + auction.extended): %+v", len(events), events)
	}
	if events[0].Type != auction.EventBidPlaced || events[1].Type != auction.EventAuctionExtended {
		t.Fatalf("event order = [%s, %s], want [bid.placed, auction.extended]",
			events[0].Type, events[1].Type)
	}
	ext := events[1]
	if ext.AuctionID != "auction-1" {
		t.Errorf("extended AuctionID = %q, want auction-1", ext.AuctionID)
	}
	if got, ok := ext.Data["new_ends_at"].(time.Time); !ok || !got.Equal(wantEnds) {
		t.Errorf("extended data[new_ends_at] = %v, want %v", ext.Data["new_ends_at"], wantEnds)
	}
	if got, ok := ext.Data["server_now"].(time.Time); !ok || !got.Equal(late) {
		t.Errorf("extended data[server_now] = %v, want %v", ext.Data["server_now"], late)
	}
	// bid.placed must carry the (already extended) ends_at.
	if got, ok := events[0].Data["ends_at"].(time.Time); !ok || !got.Equal(wantEnds) {
		t.Errorf("bid.placed data[ends_at] = %v, want extended %v", events[0].Data["ends_at"], wantEnds)
	}
}

func TestPlaceBidRejectsNonPositiveAmount(t *testing.T) {
	for _, amount := range []int64{0, -100} {
		a, clock := seedAuction(nil)
		repo := newPlaceBidRepo(a)
		pub := &recordingPublisher{repo: repo}
		svc := auction.NewService(repo, clock, pub)

		_, err := svc.PlaceBid(context.Background(), auction.PlaceBidInput{
			AuctionID: "auction-1", BidderID: "bidder-1", AmountCents: amount,
		})
		var valErr *auction.ValidationError
		if !errors.As(err, &valErr) {
			t.Fatalf("amount %d: want ValidationError, got %v", amount, err)
		}
		if valErr.Field != "amount_cents" {
			t.Errorf("amount %d: field = %q, want amount_cents", amount, valErr.Field)
		}
		events, _ := pub.snapshot()
		if len(events) != 0 {
			t.Errorf("amount %d: %d events published on a rejected bid", amount, len(events))
		}
		if repo.isCommitted() {
			t.Errorf("amount %d: repo reported committed", amount)
		}
	}
}

func TestPlaceBidErrorPaths(t *testing.T) {
	cases := []struct {
		name     string
		modify   func(*auction.Auction)
		setClock func(a *auction.Auction, c *mutableClock)
		bidder   string
		amount   int64
		wantErr  error
	}{
		{
			name:    "not active",
			modify:  func(a *auction.Auction) { a.Status = auction.StatusCancelled },
			bidder:  "bidder-1",
			amount:  1100,
			wantErr: auction.ErrNotActive,
		},
		{
			// Post-close: the status flip alone must surface the honest close reason.
			name:    "closed status",
			modify:  func(a *auction.Auction) { a.Status = auction.StatusClosed },
			bidder:  "bidder-1",
			amount:  1100,
			wantErr: auction.ErrClosed,
		},
		{
			name:     "ended",
			setClock: func(a *auction.Auction, c *mutableClock) { c.Set(a.EndsAt.Add(time.Second)) },
			bidder:   "bidder-1",
			amount:   1100,
			wantErr:  auction.ErrClosed,
		},
		{
			name:    "own auction",
			bidder:  "seller-1",
			amount:  1100,
			wantErr: auction.ErrOwnAuction,
		},
		{
			name:    "too low",
			bidder:  "bidder-1",
			amount:  1099,
			wantErr: auction.ErrBidTooLow,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, clock := seedAuction(tc.modify)
			if tc.setClock != nil {
				tc.setClock(a, clock)
			}
			repo := newPlaceBidRepo(a)
			pub := &recordingPublisher{repo: repo}
			svc := auction.NewService(repo, clock, pub)

			_, err := svc.PlaceBid(context.Background(), auction.PlaceBidInput{
				AuctionID: "auction-1", BidderID: tc.bidder, AmountCents: tc.amount,
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
			if tc.name == "too low" {
				msg := err.Error()
				for _, want := range []string{"1100", "1000", "100"} {
					if !strings.Contains(msg, want) {
						t.Errorf("error message %q must mention %q (current price + increment)", msg, want)
					}
				}
			}
			events, _ := pub.snapshot()
			if len(events) != 0 {
				t.Errorf("%d events published on a rejected bid", len(events))
			}
			stored, _ := repo.ByID(context.Background(), "auction-1")
			if stored.BidCount() != 0 {
				t.Errorf("rejected bid was persisted: %d bids stored", stored.BidCount())
			}
			if repo.isCommitted() {
				t.Error("repo reported committed on a rejected bid")
			}
		})
	}
}

func TestPlaceBidUnknownAuction(t *testing.T) {
	a, clock := seedAuction(nil)
	repo := newPlaceBidRepo(a)
	pub := &recordingPublisher{repo: repo}
	svc := auction.NewService(repo, clock, pub)

	_, err := svc.PlaceBid(context.Background(), auction.PlaceBidInput{
		AuctionID: "ghost", BidderID: "bidder-1", AmountCents: 1100,
	})
	if !errors.Is(err, auction.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	events, _ := pub.snapshot()
	if len(events) != 0 {
		t.Errorf("%d events published for unknown auction", len(events))
	}
}
