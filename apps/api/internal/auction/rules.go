package auction

import (
	"errors"
	"time"
)

// Sentinel errors returned by PlaceBid and Close. Adapters map them to
// transport-level statuses; the domain never depends on transport.
var (
	// ErrNotActive: the auction status is not active (cancelled, or any other
	// non-active, non-closed lifecycle state).
	ErrNotActive = errors.New("auction is not active")
	// ErrClosed: the auction has ended — its end time has passed, or it has
	// already been closed — so no more bids are accepted.
	ErrClosed = errors.New("auction has ended")
	// ErrOwnAuction: the seller cannot bid on their own auction.
	ErrOwnAuction = errors.New("sellers cannot bid on their own auction")
	// ErrBidTooLow: the amount is below current price plus minimum increment.
	ErrBidTooLow = errors.New("bid is below the minimum acceptable amount")
	// ErrNotClosable: the auction is still active and its end time has not passed.
	ErrNotClosable = errors.New("auction has not ended yet")
)

// softCloseWindow is the anti-sniping rule: any bid placed in the final 60
// seconds before ends_at extends ends_at to 60 seconds after that bid.
const softCloseWindow = 60 * time.Second

// PlaceBid validates bidderID's amount under the full domain rules and, on
// acceptance, appends the bid to the aggregate. A valid bid must be at least
// CurrentPrice + MinIncrementCents (the starting price stands in for the
// current price while there are no bids). The seller can never bid on their
// own auction, bids are rejected once the auction has ended, and a late bid
// triggers the soft-close extension. Rejection reasons are kept honest per
// lifecycle state: a closed auction reports ErrClosed (the close is the
// reason), a cancelled one reports ErrNotActive, and an active auction past
// ends_at reports ErrClosed.
//
// The returned Bid has no ID yet: persistence assigns it when the bid is
// stored.
func (a *Auction) PlaceBid(bidderID string, amountCents int64, now time.Time) (Bid, error) {
	if a.Status == StatusClosed {
		return Bid{}, ErrClosed
	}
	if a.Status != StatusActive {
		return Bid{}, ErrNotActive
	}
	if !now.Before(a.EndsAt) {
		return Bid{}, ErrClosed
	}
	if bidderID == a.SellerID {
		return Bid{}, ErrOwnAuction
	}
	if amountCents < a.CurrentPrice()+a.MinIncrementCents {
		return Bid{}, ErrBidTooLow
	}

	bid := Bid{
		AuctionID:   a.ID,
		BidderID:    bidderID,
		AmountCents: amountCents,
		CreatedAt:   now,
	}
	a.bids = append(a.bids, bid)

	// Anti-sniping soft close: a bid inside the final 60 seconds pushes the
	// end 60 seconds past that bid, giving other bidders time to react.
	if a.EndsAt.Sub(now) <= softCloseWindow {
		a.EndsAt = now.Add(softCloseWindow)
	}
	return bid, nil
}

// Close transitions an active auction to closed once now has reached ends_at,
// sets closed_at, and determines the winner: the highest bidder, but only if
// at least one bid exists and the reserve (if any) was met. Otherwise the
// auction closes unsold. It returns ErrNotClosable while the auction is still
// running and ErrNotActive when it is no longer active.
func (a *Auction) Close(now time.Time) (winner Bid, won bool, err error) {
	if a.Status != StatusActive {
		return Bid{}, false, ErrNotActive
	}
	if now.Before(a.EndsAt) {
		return Bid{}, false, ErrNotClosable
	}
	a.Status = StatusClosed
	closedAt := now
	a.ClosedAt = &closedAt

	if highest, ok := a.HighestBid(); ok && a.ReserveMet() {
		return highest, true, nil
	}
	return Bid{}, false, nil
}
