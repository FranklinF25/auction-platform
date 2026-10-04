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

// ListPurchases returns one page of the user's purchases — won auctions
// joined with their checkout transaction, newest close first — backing the
// purchases dashboard. Losing bids and unsold (reserve-not-met) auctions
// never appear; the embedded Transaction carries payment status and window.
func (s *Service) ListPurchases(ctx context.Context, userID string, page, pageSize int) (Page[PurchaseItem], error) {
	page, pageSize = normalizePaging(page, pageSize)
	items, total, err := s.repo.ListMyTransactions(ctx, userID, page, pageSize)
	if err != nil {
		return Page[PurchaseItem]{}, err
	}
	return Page[PurchaseItem]{Items: items, Page: page, PageSize: pageSize, Total: total}, nil
}

// SellerSales aggregates the authenticated seller's sales for the seller
// dashboard: completed count, still-pending count and completed revenue.
func (s *Service) SellerSales(ctx context.Context, sellerID string) (int, int, int64, error) {
	return s.repo.SellerSales(ctx, sellerID)
}

// PayTransaction simulates one payment attempt by the authenticated user on
// their own transaction. Ownership first: a transaction that exists but
// belongs to another winner reports ErrNotFound — no existence leak. The
// payment itself is owned by the repository (locked row, pure Transaction.Pay,
// persist, commit) exactly like PlaceBid; nothing is published (the PRD's
// payment flow has no realtime requirement). A declined card returns the
// failed transaction with a nil error.
func (s *Service) PayTransaction(ctx context.Context, userID, transactionID, cardNumber string) (*Transaction, error) {
	t, err := s.repo.TransactionByID(ctx, transactionID)
	if err != nil {
		return nil, err
	}
	if t.WinnerID != userID {
		return nil, ErrNotFound // another user's transaction is indistinguishable from a missing one
	}
	return s.repo.PayTransaction(ctx, transactionID, cardNumber, s.clock.Now())
}

// ExpireDueTransactions is the M4 expiry use case driven by the expiry
// worker: move every pending/failed transaction whose payment window has
// passed to expired. It returns how many rows the sweep moved.
func (s *Service) ExpireDueTransactions(ctx context.Context) (int, error) {
	return s.repo.ExpireDueTransactions(ctx, s.clock.Now())
}

// CloseDue is the M3 use case driven by the closer worker: close every
// auction whose end time has passed. Correctness is owned by the repository
// exactly like PlaceBid — each auction closes inside its own committed
// transaction — so this method only issues the command. Events are published
// only AFTER CloseDue has returned, i.e. strictly after every per-auction
// transaction has committed; a repository error means nothing was published
// for the failed pass (per-auction failures are absorbed by the repository,
// which logs and continues).
func (s *Service) CloseDue(ctx context.Context) error {
	results, err := s.repo.CloseDue(ctx, s.clock.Now())
	if err != nil {
		return err // repository-level failure: nothing published
	}
	emitCloseEvents(s.pub, s.clock, results)
	return nil
}

// emitCloseEvents broadcasts auction.closed for every closed auction, with
// server_now stamped from the domain Clock at emit time. A nil publisher
// disables broadcasting (nothing depends on it).
func emitCloseEvents(pub EventPublisher, clock Clock, results []ClosedAuction) {
	if pub == nil || len(results) == 0 {
		return
	}
	now := clock.Now()
	for _, res := range results {
		// winner_name is null unless the auction sold; the payload never
		// carries user ids, only the winner's display name.
		var winnerName any
		if res.Won {
			winnerName = res.Winner.BidderName
		}
		pub.Publish(Event{
			Type:      EventAuctionClosed,
			AuctionID: res.Auction.ID,
			Data: map[string]any{
				"auction_id":        res.Auction.ID,
				"status":            string(res.Auction.Status),
				"winner_name":       winnerName,
				"sold":              res.Won,
				"final_price_cents": res.Auction.CurrentPrice(),
				"server_now":        now,
			},
		})
	}
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
