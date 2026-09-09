// Package auction holds the pure auction domain: the Auction aggregate with
// its Bid list, the bidding rules (increments, anti-sniping soft close, close
// and winner determination) and the ports its services consume.
//
// Hexagonal rule: this package imports the Go standard library only. All
// infrastructure (HTTP, PostgreSQL, WebSockets) lives in adapters that import
// this package, never the other way around.
package auction

import (
	"fmt"
	"strings"
	"time"
)

// Status is the lifecycle state of an auction.
type Status string

const (
	StatusActive    Status = "active"
	StatusClosed    Status = "closed"
	StatusCancelled Status = "cancelled"
)

// Creation bounds and defaults.
const (
	MinTitleLength       = 1
	MaxTitleLength       = 200
	MaxDescriptionLength = 5000
	MinDurationMinutes   = 1
	MaxDurationMinutes   = 20160 // 14 days
	// DefaultMinIncrementCents is applied when the creator omits the increment.
	DefaultMinIncrementCents int64 = 100
)

// Bid is a single offer inside an Auction aggregate. A bid never exists
// outside its auction, so there is no separate bids package. ID and
// BidderName are filled in by the persistence layer; the domain only governs
// amounts, ordering and timing.
type Bid struct {
	ID          string
	AuctionID   string
	BidderID    string
	BidderName  string
	AmountCents int64
	CreatedAt   time.Time
}

// Auction is a timed English-style (ascending) auction. All money is integer
// cents; the server clock (injected via the Clock port) is the only time
// authority. The reserve price is private to the seller: representations must
// never leak its value, only the ReserveMet boolean.
type Auction struct {
	ID                 string
	SellerID           string
	Title              string
	Description        string
	StartingPriceCents int64
	MinIncrementCents  int64
	// ReservePriceCents is nil when no reserve was set.
	ReservePriceCents *int64
	Status            Status
	EndsAt            time.Time
	CreatedAt         time.Time
	ClosedAt          *time.Time

	bids []Bid
}

// ValidationError reports an invalid creation input. Adapters map it to a 400
// response; the Field tells the client which input was wrong.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Reason }

func invalid(field, format string, args ...any) error {
	return &ValidationError{Field: field, Reason: fmt.Sprintf(format, args...)}
}

// CreateInput is the data needed to create an auction. A zero
// MinIncrementCents means "use the default"; a nil ReservePriceCents means
// "no reserve".
type CreateInput struct {
	Title              string
	Description        string
	StartingPriceCents int64
	MinIncrementCents  int64
	ReservePriceCents  *int64
	DurationMinutes    int
}

// NewAuction builds an active auction, validating every creation rule.
// ends_at is computed from the injected now timestamp plus the duration, so
// tests (and the service) control time explicitly.
func NewAuction(sellerID string, in CreateInput, now time.Time) (*Auction, error) {
	title := strings.TrimSpace(in.Title)
	description := strings.TrimSpace(in.Description)

	if sellerID == "" {
		return nil, invalid("seller_id", "is required")
	}
	if len(title) < MinTitleLength || len(title) > MaxTitleLength {
		return nil, invalid("title", "must be between %d and %d characters", MinTitleLength, MaxTitleLength)
	}
	if len(description) > MaxDescriptionLength {
		return nil, invalid("description", "must be at most %d characters", MaxDescriptionLength)
	}
	if in.StartingPriceCents < 1 {
		return nil, invalid("starting_price_cents", "must be at least 1")
	}
	minIncrement := in.MinIncrementCents
	if minIncrement == 0 {
		minIncrement = DefaultMinIncrementCents
	}
	if minIncrement < 1 {
		return nil, invalid("min_increment_cents", "must be at least 1")
	}
	if in.ReservePriceCents != nil && *in.ReservePriceCents < in.StartingPriceCents {
		return nil, invalid("reserve_price_cents", "must be greater than or equal to the starting price")
	}
	if in.DurationMinutes < MinDurationMinutes || in.DurationMinutes > MaxDurationMinutes {
		return nil, invalid("duration_minutes", "must be between %d and %d minutes", MinDurationMinutes, MaxDurationMinutes)
	}

	return &Auction{
		SellerID:           sellerID,
		Title:              title,
		Description:        description,
		StartingPriceCents: in.StartingPriceCents,
		MinIncrementCents:  minIncrement,
		ReservePriceCents:  in.ReservePriceCents,
		Status:             StatusActive,
		EndsAt:             now.Add(time.Duration(in.DurationMinutes) * time.Minute),
		CreatedAt:          now,
	}, nil
}

// Bids returns a copy of the bids held by the aggregate, oldest first.
func (a *Auction) Bids() []Bid {
	out := make([]Bid, len(a.bids))
	copy(out, a.bids)
	return out
}

// LoadBids rehydrates the aggregate's bid history. Used by repositories when
// loading a persisted auction; PlaceBid is the only way to add bids.
func (a *Auction) LoadBids(bids []Bid) {
	a.bids = bids
}

// CurrentPrice returns the highest bid amount, or the starting price when no
// bid has been placed yet.
func (a *Auction) CurrentPrice() int64 {
	price := a.StartingPriceCents
	for _, b := range a.bids {
		if b.AmountCents > price {
			price = b.AmountCents
		}
	}
	return price
}

// HighestBid returns the current highest bid, if any bid exists.
func (a *Auction) HighestBid() (Bid, bool) {
	var best Bid
	found := false
	for _, b := range a.bids {
		if !found || b.AmountCents > best.AmountCents {
			best = b
			found = true
		}
	}
	return best, found
}

// ReserveMet reports whether the hidden reserve, if any, has been reached.
// An auction without reserve is always "met".
func (a *Auction) ReserveMet() bool {
	return a.ReservePriceCents == nil || a.CurrentPrice() >= *a.ReservePriceCents
}

// BidCount returns how many bids the auction has received.
func (a *Auction) BidCount() int { return len(a.bids) }
