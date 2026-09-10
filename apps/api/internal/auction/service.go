package auction

import (
	"context"
	"time"
)

// Paging defaults and limits, applied by the service before any repository
// call so every adapter sees normalized values.
const (
	DefaultPage     = 1
	DefaultPageSize = 20
	MaxPageSize     = 50
)

// Page is one page of results: the items plus the normalized paging metadata
// and the total count matching the query.
type Page[T any] struct {
	Items    []T
	Page     int
	PageSize int
	Total    int
}

// Service holds the use cases directly on the domain and its ports — no
// extra usecase layer. Bidding (the M2 hot path) lives here too.
type Service struct {
	repo  Repository
	clock Clock
	pub   EventPublisher // may be nil: then nothing is published
}

// NewService wires the repository, clock and event publisher into the
// service. The publisher is the hub in production and a recording fake in
// tests; a nil publisher disables broadcasting (nothing depends on it).
func NewService(repo Repository, clock Clock, pub EventPublisher) *Service {
	return &Service{repo: repo, clock: clock, pub: pub}
}

// PlaceBidInput is the bid command: which auction, which (authenticated)
// bidder, and how much — integer cents, never floats.
type PlaceBidInput struct {
	AuctionID   string
	BidderID    string
	AmountCents int64
}

// PlacedBid is the outcome of an accepted bid: the stored bid, the final
// aggregate state with the bid applied, whether the anti-sniping soft close
// moved ends_at, and the server time stamped when the result was published
// (also carried on every event so clients can compute clock offset).
type PlacedBid struct {
	Bid       Bid
	Auction   *Auction
	Extended  bool
	ServerNow time.Time
}

// CreateAuction validates and persists a new active auction for sellerID.
// Validation errors are *ValidationError; everything else comes from the
// repository.
func (s *Service) CreateAuction(ctx context.Context, sellerID string, in CreateInput) (*Auction, error) {
	a, err := NewAuction(sellerID, in, s.clock.Now())
	if err != nil {
		return nil, err
	}
	if err := s.repo.Create(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// GetAuction loads one aggregate (including bid history) by id.
func (s *Service) GetAuction(ctx context.Context, id string) (*Auction, error) {
	return s.repo.ByID(ctx, id)
}

// ListAuctions returns one page of summaries matching the filter, with
// normalized paging (defaults 1/20, page_size capped at 50).
func (s *Service) ListAuctions(ctx context.Context, f ListFilter) (Page[ListItem], error) {
	f.Page, f.PageSize = normalizePaging(f.Page, f.PageSize)
	items, total, err := s.repo.List(ctx, f)
	if err != nil {
		return Page[ListItem]{}, err
	}
	return Page[ListItem]{Items: items, Page: f.Page, PageSize: f.PageSize, Total: total}, nil
}

// ListBids returns one page of the auction's bids, newest first.
func (s *Service) ListBids(ctx context.Context, auctionID string, page, pageSize int) (Page[Bid], error) {
	page, pageSize = normalizePaging(page, pageSize)
	bids, total, err := s.repo.ListBids(ctx, auctionID, page, pageSize)
	if err != nil {
		return Page[Bid]{}, err
	}
	return Page[Bid]{Items: bids, Page: page, PageSize: pageSize, Total: total}, nil
}

func normalizePaging(page, pageSize int) (int, int) {
	if page < DefaultPage {
		page = DefaultPage
	}
	if pageSize < 1 {
		pageSize = DefaultPageSize
	}
	if pageSize > MaxPageSize {
		pageSize = MaxPageSize
	}
	return page, pageSize
}

// PlaceBid is the bid hot path (the M2 use case). Correctness of concurrent
// bids is owned by the database: the repository serializes per auction with
// SELECT ... FOR UPDATE, loads the aggregate, applies the pure domain rules
// (Auction.PlaceBid) and persists bid + ends_at inside one transaction.
// Events are published only AFTER that transaction has committed — never
// before — so watchers never observe a state that could still roll back.
func (s *Service) PlaceBid(ctx context.Context, in PlaceBidInput) (*PlacedBid, error) {
	if in.AmountCents <= 0 {
		return nil, &ValidationError{Field: "amount_cents", Reason: "must be a positive number of cents"}
	}
	res, err := s.repo.PlaceBid(ctx, in.AuctionID, in.BidderID, in.AmountCents, s.clock.Now())
	if err != nil {
		return nil, err // rolled back: nothing happened, publish nothing
	}
	s.emitBidEvents(res)
	return res, nil
}

// emitBidEvents broadcasts the post-commit events for an accepted bid:
// bid.placed always, plus auction.extended when the soft close moved ends_at.
// server_now is stamped from the domain Clock at emit time.
func (s *Service) emitBidEvents(res *PlacedBid) {
	if s.pub == nil {
		return
	}
	now := s.clock.Now()
	res.ServerNow = now
	s.pub.Publish(Event{
		Type:      EventBidPlaced,
		AuctionID: res.Bid.AuctionID,
		Data: map[string]any{
			"auction_id":   res.Bid.AuctionID,
			"bid_id":       res.Bid.ID,
			"bidder_name":  res.Bid.BidderName,
			"amount_cents": res.Bid.AmountCents,
			"ends_at":      res.Auction.EndsAt,
			"server_now":   now,
		},
	})
	if res.Extended {
		s.pub.Publish(Event{
			Type:      EventAuctionExtended,
			AuctionID: res.Bid.AuctionID,
			Data: map[string]any{
				"auction_id":  res.Bid.AuctionID,
				"new_ends_at": res.Auction.EndsAt,
				"server_now":  now,
			},
		})
	}
}
