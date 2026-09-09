package auction

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by Repository implementations when an auction does
// not exist. Adapters map it to 404.
var ErrNotFound = errors.New("auction not found")

// Clock is the domain's time port: the only way the auction rules read the
// current time. The composition root injects the system clock; tests inject a
// fake so soft-close and expiry behavior is deterministic.
type Clock interface {
	Now() time.Time
}

// ListFilter controls the auction list query. Empty Status or Query means
// "no filter on that dimension".
type ListFilter struct {
	Status   Status
	Query    string // case-insensitive title substring match
	Page     int
	PageSize int
}

// Repository persists and reads Auction aggregates. Declared on the consumer
// side (this package); adapters (postgres, in-memory fakes) implement it.
type Repository interface {
	// Create inserts a new auction and fills in database-generated fields
	// (ID, CreatedAt).
	Create(ctx context.Context, a *Auction) error
	// ByID loads one aggregate including its bid history, oldest first.
	// Returns ErrNotFound when the id does not exist.
	ByID(ctx context.Context, id string) (*Auction, error)
	// List returns one page of auction summaries plus the total count
	// matching the filter.
	List(ctx context.Context, f ListFilter) (items []ListItem, total int, err error)
	// ListBids returns one page of bids for one auction, newest first,
	// including bidder names. Paging normalization happens in the service.
	ListBids(ctx context.Context, auctionID string, page, pageSize int) (bids []Bid, total int, err error)
}

// ListItem is a read model for list endpoints: auction fields plus the
// derived values (current price, reserve met, bid count) computed by the
// query. Like the detail representation it never carries the reserve value.
type ListItem struct {
	ID                 string
	SellerID           string
	Title              string
	Description        string
	StartingPriceCents int64
	CurrentPriceCents  int64
	MinIncrementCents  int64
	ReserveMet         bool
	BidCount           int
	Status             Status
	EndsAt             time.Time
	CreatedAt          time.Time
}
