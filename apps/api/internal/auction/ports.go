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
//
// M4 checkout: when Won is true, Transaction carries the pending payment the
// closer creates inside the same per-auction transaction (amount = final
// price, expires 48h later); it is nil for an unsold close. The auction.closed
// event payload is unchanged — the transaction is read-model state, not a
// broadcast fact.
type ClosedAuction struct {
	Auction     *Auction
	Winner      Bid
	Won         bool
	Transaction *Transaction
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
	//
	// M4: when a close has a winner, the same per-auction transaction also
	// inserts the pending payment Transaction (amount = final price at close,
	// expires_at = now + TransactionExpiryDuration, enforced UNIQUE on
	// auction_id) and reports it on the ClosedAuction result.
	CloseDue(ctx context.Context, now time.Time) ([]ClosedAuction, error)
	// ListWon returns one page of auctions the user has won — closed, sold
	// (reserve met) and userID is the highest bidder — ordered by closed_at
	// DESC, plus the total count. Paging normalization happens in the service.
	ListWon(ctx context.Context, userID string, page, pageSize int) (items []ListItem, total int, err error)

	// --- M4 checkout: payments, expiry, purchase and sales read models ---

	// CreateTransaction inserts t and fills in its database-generated ID.
	// The caller owns the timestamps: CreatedAt, ExpiresAt, Status and PaidAt
	// are stored verbatim (the closing sweep and the demo seed both construct
	// full domain objects). The UNIQUE index on auction_id is the concurrency
	// authority: at most one transaction per auction.
	CreateTransaction(ctx context.Context, t *Transaction) error
	// TransactionByID loads one transaction; ErrNotFound when absent.
	TransactionByID(ctx context.Context, id string) (*Transaction, error)
	// PayTransaction executes one payment attempt atomically, adapter-owned
	// transaction like PlaceBid/CloseDue: BEGIN; SELECT ... FOR UPDATE the
	// transaction row; call the pure Transaction.Pay; persist status and
	// paid_at; COMMIT. It returns the post-payment transaction. A decline is
	// not an error: Pay sets status failed (persisted, retryable) and the
	// method returns the failed transaction with a nil error. Sentinel errors
	// (ErrTransactionCompleted, ErrTransactionExpired) and *ValidationError
	// surface unchanged and roll back.
	PayTransaction(ctx context.Context, id string, cardNumber string, now time.Time) (*Transaction, error)
	// ExpireDueTransactions flips every pending or failed transaction whose
	// expires_at has passed as of now to expired in a single UPDATE and
	// returns how many rows moved.
	ExpireDueTransactions(ctx context.Context, now time.Time) (int, error)
	// ListMyTransactions returns one page of the user's purchases — won
	// auctions joined with their transaction, ordered by closed_at DESC — plus
	// the total count. Paging normalization happens in the service.
	ListMyTransactions(ctx context.Context, userID string, page, pageSize int) (items []PurchaseItem, total int, err error)
	// SellerSales aggregates one seller's sales: how many of their sold
	// auctions' transactions are completed, how many are still pending, and
	// the total revenue (sum of completed amount_cents).
	SellerSales(ctx context.Context, sellerID string) (completed int, pending int, revenueCents int64, err error)
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

// PurchaseItem is the buyer dashboard read model (M4): one won auction (the
// existing ListItem summary) plus the checkout Transaction created at close,
// so the purchases view can show amount, payment status and the remaining
// payment window alongside the lot.
type PurchaseItem struct {
	ListItem
	Transaction Transaction
}
