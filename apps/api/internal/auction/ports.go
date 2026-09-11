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

// Event type names on the wire. The full event contract — snake_case JSON
// field names inside "data", RFC3339 timestamps, a "server_now" field on
// every state-carrying event so clients can compute their clock offset — is
// pinned in docs/PRD.md ("Real-time design"); the hub marshals Type and Data
// verbatim into {"type": ..., "data": ...}.
const (
	// EventAuctionState is the full snapshot sent on room join (httpapi builds it).
	EventAuctionState = "auction.state"
	// EventBidPlaced is published after a bid transaction commits.
	EventBidPlaced = "bid.placed"
	// EventAuctionExtended is published when the soft close moves ends_at.
	EventAuctionExtended = "auction.extended"
	// EventAuctionClosed is published by the closing worker (M3).
	EventAuctionClosed = "auction.closed"
	// EventPresenceUpdate reports the connected watcher count (hub).
	EventPresenceUpdate = "presence.update"
)

// Event is one domain fact broadcast to everyone watching an auction.
// AuctionID is the routing key (which room the event belongs to); it is
// mirrored inside Data as "auction_id" on the wire. Data holds the payload
// per the pinned event contract: snake_case keys, integer cents, time.Time
// values (RFC3339 on the wire).
type Event struct {
	Type      string
	AuctionID string
	Data      map[string]any
}

// EventPublisher fans events out to watchers. Implementations must be safe
// for concurrent use, must never block the caller (slow consumers are
// dropped, not waited for) and must tolerate having no watchers. The service
// stamps Data["server_now"] from the Clock at emit time.
type EventPublisher interface {
	Publish(evt Event)
}

// ListFilter controls the auction list query. Empty Status, Query or
// SellerID means "no filter on that dimension"; the seller dashboard sets
// SellerID to scope the list to one seller's own auctions.
type ListFilter struct {
	Status   Status
	Query    string // case-insensitive title substring match
	SellerID string
	Page     int
	PageSize int
}

// ClosedAuction is the outcome of closing one auction: the post-close
// aggregate (status closed, closed_at set) plus the domain-determined winner.
// Won is false when the auction closed unsold (no bids, or reserve not met),
// in which case Winner is the zero Bid. When Won is true the persistence
// layer fills Winner.BidderName so close events need no extra lookup.
type ClosedAuction struct {
	Auction *Auction
	Winner  Bid
	Won     bool
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
	// PlaceBid executes the bid hot path atomically under the database's
	// authority: BEGIN; SELECT ... FOR UPDATE the auction row; load the
	// aggregate (auction + bids); apply the domain rules via the pure
	// Auction.PlaceBid; insert the bid (filling ID and BidderName) and update
	// ends_at when the soft close extended it; COMMIT. The transaction is
	// committed before PlaceBid returns, so the service may publish events as
	// soon as it succeeds — never before.
	//
	// Error contract: returns ErrNotFound, or any Auction.PlaceBid sentinel
	// (ErrNotActive for a cancelled auction, ErrClosed for one already closed
	// or past ends_at, ErrOwnAuction, ErrBidTooLow). Implementations
	// wrap ErrBidTooLow with the concrete numbers (minimum acceptable amount,
	// current price, increment) so the transport-level message is actionable.
	PlaceBid(ctx context.Context, auctionID, bidderID string, amountCents int64, now time.Time) (*PlacedBid, error)
	// List returns one page of auction summaries plus the total count
	// matching the filter.
	List(ctx context.Context, f ListFilter) (items []ListItem, total int, err error)
	// ListBids returns one page of bids for one auction, newest first,
	// including bidder names. Paging normalization happens in the service.
	ListBids(ctx context.Context, auctionID string, page, pageSize int) (bids []Bid, total int, err error)
	// CloseDue closes every active auction whose ends_at has passed as of now,
	// one transaction per auction (same locked pattern as PlaceBid: BEGIN; SELECT
	// ... FOR UPDATE; load aggregate; pure Auction.Close; persist status and
	// closed_at; COMMIT), so one failing auction never rolls back the others —
	// implementations log a failed auction and continue. Each transaction is
	// committed before CloseDue returns, so the service may publish close events
	// as soon as it succeeds — never before. Auctions that turn out not to be
	// closable anymore (soft-close extension moved ends_at past now, or the
	// auction is no longer active) are skipped without being reported as errors.
	CloseDue(ctx context.Context, now time.Time) ([]ClosedAuction, error)
	// ListWon returns one page of auctions the user has won — closed, sold
	// (reserve met) and userID is the highest bidder — ordered by closed_at
	// DESC, plus the total count. Paging normalization happens in the service.
	ListWon(ctx context.Context, userID string, page, pageSize int) (items []ListItem, total int, err error)
}

// ListItem is a read model for list endpoints: auction fields plus the
// derived values (current price, reserve met, bid count) computed by the
// query. Like the detail representation it never carries the reserve value.
// ClosedAt is the close timestamp; the zero value means "not closed" (still
// active or cancelled), so representations can render the actual close time
// instead of deriving it from EndsAt.
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
	ClosedAt           time.Time
}
