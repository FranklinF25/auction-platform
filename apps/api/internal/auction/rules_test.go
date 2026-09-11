package auction_test

import (
	"errors"
	"testing"
	"time"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
)

const (
	sellerID = "seller-1"
	bidderA  = "bidder-a"
	bidderB  = "bidder-b"
)

// newTestAuction builds a 60-minute auction starting at baseTime:
// starting price 1000 cents, default increment 100 cents, no reserve.
// It ends at baseTime + 60m.
func newTestAuction(t *testing.T) *auction.Auction {
	t.Helper()
	a, err := auction.NewAuction(sellerID, auction.CreateInput{
		Title:              "Test auction",
		StartingPriceCents: 1000,
		DurationMinutes:    60,
	}, baseTime)
	if err != nil {
		t.Fatalf("newTestAuction: %v", err)
	}
	return a
}

func TestPlaceBidAcceptsValidFirstBid(t *testing.T) {
	a := newTestAuction(t)

	bid, err := a.PlaceBid(bidderA, 1100, baseTime.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("PlaceBid returned error: %v", err)
	}
	if bid.AmountCents != 1100 {
		t.Errorf("bid.AmountCents = %d, want 1100", bid.AmountCents)
	}
	if got := a.BidCount(); got != 1 {
		t.Errorf("BidCount() = %d, want 1", got)
	}
	if got := a.CurrentPrice(); got != 1100 {
		t.Errorf("CurrentPrice() = %d, want 1100", got)
	}
}

func TestPlaceBidRejectsTooLowBids(t *testing.T) {
	t.Run("first bid equal to starting price misses the increment", func(t *testing.T) {
		a := newTestAuction(t)
		_, err := a.PlaceBid(bidderA, 1000, baseTime)
		if !errors.Is(err, auction.ErrBidTooLow) {
			t.Fatalf("err = %v, want ErrBidTooLow", err)
		}
		if a.BidCount() != 0 {
			t.Error("rejected bid must not be appended")
		}
	})

	t.Run("first bid below starting price", func(t *testing.T) {
		a := newTestAuction(t)
		_, err := a.PlaceBid(bidderA, 999, baseTime)
		if !errors.Is(err, auction.ErrBidTooLow) {
			t.Fatalf("err = %v, want ErrBidTooLow", err)
		}
	})

	t.Run("second bid one cent below current plus increment", func(t *testing.T) {
		a := newTestAuction(t)
		if _, err := a.PlaceBid(bidderA, 1100, baseTime); err != nil {
			t.Fatalf("first bid failed: %v", err)
		}
		if _, err := a.PlaceBid(bidderB, 1199, baseTime.Add(time.Minute)); !errors.Is(err, auction.ErrBidTooLow) {
			t.Fatalf("err = %v, want ErrBidTooLow", err)
		}
	})

	t.Run("second bid exactly at current plus increment is accepted", func(t *testing.T) {
		a := newTestAuction(t)
		if _, err := a.PlaceBid(bidderA, 1100, baseTime); err != nil {
			t.Fatalf("first bid failed: %v", err)
		}
		if _, err := a.PlaceBid(bidderB, 1200, baseTime.Add(time.Minute)); err != nil {
			t.Fatalf("second bid failed: %v", err)
		}
		if got := a.CurrentPrice(); got != 1200 {
			t.Errorf("CurrentPrice() = %d, want 1200", got)
		}
	})
}

func TestPlaceBidRejectsSellerSelfBid(t *testing.T) {
	a := newTestAuction(t)
	if _, err := a.PlaceBid(sellerID, 1100, baseTime); !errors.Is(err, auction.ErrOwnAuction) {
		t.Fatalf("err = %v, want ErrOwnAuction", err)
	}
	if a.BidCount() != 0 {
		t.Error("rejected self-bid must not be appended")
	}
}

func TestPlaceBidRejectsNonActiveStatus(t *testing.T) {
	t.Run("cancelled keeps ErrNotActive", func(t *testing.T) {
		a := newTestAuction(t)
		a.Status = auction.StatusCancelled
		if _, err := a.PlaceBid(bidderA, 1100, baseTime); !errors.Is(err, auction.ErrNotActive) {
			t.Fatalf("err = %v, want ErrNotActive", err)
		}
	})

	// Post-close contract: once the closing worker flips the status, the honest
	// rejection reason is "closed", so the API answers auction_closed (409),
	// not auction_not_active.
	t.Run("closed reports ErrClosed", func(t *testing.T) {
		a := newTestAuction(t)
		a.Status = auction.StatusClosed
		if _, err := a.PlaceBid(bidderA, 1100, baseTime); !errors.Is(err, auction.ErrClosed) {
			t.Fatalf("err = %v, want ErrClosed", err)
		}
	})
}

func TestPlaceBidRejectsAfterEndTime(t *testing.T) {
	a := newTestAuction(t)
	if _, err := a.PlaceBid(bidderA, 5000, a.EndsAt.Add(time.Second)); !errors.Is(err, auction.ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
	// Exactly at ends_at the auction is over too.
	if _, err := a.PlaceBid(bidderA, 5000, a.EndsAt); !errors.Is(err, auction.ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
}

func TestPlaceBidStoresBidTimestamp(t *testing.T) {
	a := newTestAuction(t)
	at := baseTime.Add(3 * time.Minute)
	bid, err := a.PlaceBid(bidderA, 1100, at)
	if err != nil {
		t.Fatalf("PlaceBid: %v", err)
	}
	if !bid.CreatedAt.Equal(at) {
		t.Errorf("bid.CreatedAt = %v, want %v", bid.CreatedAt, at)
	}
	bids := a.Bids()
	if len(bids) != 1 || bids[0].BidderID != bidderA {
		t.Fatalf("Bids() = %+v, want one bid by %s", bids, bidderA)
	}
}

func TestSoftCloseExtendsEndsAt(t *testing.T) {
	t.Run("bid inside the final 60 seconds extends the end", func(t *testing.T) {
		a := newTestAuction(t) // ends at baseTime + 60m
		at := baseTime.Add(59*time.Minute + 30*time.Second)
		if _, err := a.PlaceBid(bidderA, 1100, at); err != nil {
			t.Fatalf("PlaceBid: %v", err)
		}
		want := at.Add(60 * time.Second)
		if !a.EndsAt.Equal(want) {
			t.Errorf("EndsAt = %v, want %v (soft close)", a.EndsAt, want)
		}
	})

	t.Run("consecutive late bids extend again", func(t *testing.T) {
		a := newTestAuction(t)
		first := baseTime.Add(59 * time.Minute) // 60s before end -> no-op extension
		if _, err := a.PlaceBid(bidderA, 1100, first); err != nil {
			t.Fatalf("first bid: %v", err)
		}
		if !a.EndsAt.Equal(baseTime.Add(60 * time.Minute)) {
			t.Fatalf("EndsAt = %v, want unchanged", a.EndsAt)
		}
		second := baseTime.Add(60*time.Minute - 10*time.Second)
		if _, err := a.PlaceBid(bidderB, 1200, second); err != nil {
			t.Fatalf("second bid: %v", err)
		}
		want := second.Add(60 * time.Second)
		if !a.EndsAt.Equal(want) {
			t.Errorf("EndsAt = %v, want %v (double soft close)", a.EndsAt, want)
		}
	})

	t.Run("bid outside the final window does not extend", func(t *testing.T) {
		a := newTestAuction(t)
		originalEnd := a.EndsAt
		if _, err := a.PlaceBid(bidderA, 1100, baseTime.Add(30*time.Minute)); err != nil {
			t.Fatalf("PlaceBid: %v", err)
		}
		if !a.EndsAt.Equal(originalEnd) {
			t.Errorf("EndsAt = %v, want unchanged %v", a.EndsAt, originalEnd)
		}
	})
}

func TestClose(t *testing.T) {
	t.Run("rejects close before end time", func(t *testing.T) {
		a := newTestAuction(t)
		_, _, err := a.Close(baseTime.Add(59 * time.Minute))
		if !errors.Is(err, auction.ErrNotClosable) {
			t.Fatalf("err = %v, want ErrNotClosable", err)
		}
		if a.Status != auction.StatusActive {
			t.Errorf("status = %q, want still active", a.Status)
		}
		if a.ClosedAt != nil {
			t.Error("ClosedAt must stay nil before close")
		}
	})

	t.Run("rejects close on non-active auction", func(t *testing.T) {
		a := newTestAuction(t)
		a.Status = auction.StatusCancelled
		if _, _, err := a.Close(a.EndsAt); !errors.Is(err, auction.ErrNotActive) {
			t.Fatalf("err = %v, want ErrNotActive", err)
		}
	})

	t.Run("closes at exactly ends_at with no winner when no bids", func(t *testing.T) {
		a := newTestAuction(t)
		winner, won, err := a.Close(a.EndsAt)
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
		if won {
			t.Error("won = true, want false with zero bids")
		}
		if winner != (auction.Bid{}) {
			t.Errorf("winner = %+v, want zero bid", winner)
		}
		if a.Status != auction.StatusClosed {
			t.Errorf("status = %q, want closed", a.Status)
		}
		if a.ClosedAt == nil || !a.ClosedAt.Equal(a.EndsAt) {
			t.Errorf("ClosedAt = %v, want %v", a.ClosedAt, a.EndsAt)
		}
	})

	t.Run("highest bidder wins without reserve", func(t *testing.T) {
		a := newTestAuction(t)
		if _, err := a.PlaceBid(bidderA, 1100, baseTime.Add(5*time.Minute)); err != nil {
			t.Fatalf("bid a: %v", err)
		}
		if _, err := a.PlaceBid(bidderB, 1200, baseTime.Add(10*time.Minute)); err != nil {
			t.Fatalf("bid b: %v", err)
		}
		winner, won, err := a.Close(a.EndsAt)
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
		if !won || winner.BidderID != bidderB || winner.AmountCents != 1200 {
			t.Errorf("winner = %+v won=%t, want bidder-b at 1200", winner, won)
		}
	})

	t.Run("reserve unmet closes unsold even with bids", func(t *testing.T) {
		reserve := int64(5000)
		a, err := auction.NewAuction(sellerID, auction.CreateInput{
			Title:              "Reserved auction",
			StartingPriceCents: 1000,
			ReservePriceCents:  &reserve,
			DurationMinutes:    60,
		}, baseTime)
		if err != nil {
			t.Fatalf("NewAuction: %v", err)
		}
		if _, err := a.PlaceBid(bidderA, 2000, baseTime); err != nil {
			t.Fatalf("bid: %v", err)
		}
		if a.ReserveMet() {
			t.Fatal("ReserveMet() = true, want false at 2000 < 5000")
		}
		_, won, err := a.Close(a.EndsAt)
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
		if won {
			t.Error("won = true, want false when reserve is unmet")
		}
		if got := a.CurrentPrice(); got != 2000 {
			t.Errorf("CurrentPrice() = %d, want 2000 (bid history stays readable)", got)
		}
	})

	t.Run("reserve met exactly still wins", func(t *testing.T) {
		reserve := int64(2000)
		a, err := auction.NewAuction(sellerID, auction.CreateInput{
			Title:              "Reserved auction",
			StartingPriceCents: 1000,
			ReservePriceCents:  &reserve,
			DurationMinutes:    60,
		}, baseTime)
		if err != nil {
			t.Fatalf("NewAuction: %v", err)
		}
		if _, err := a.PlaceBid(bidderA, 2000, baseTime); err != nil {
			t.Fatalf("bid: %v", err)
		}
		_, won, err := a.Close(a.EndsAt)
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
		if !won {
			t.Error("won = false, want true when current price equals reserve")
		}
	})

	t.Run("closing twice is rejected", func(t *testing.T) {
		a := newTestAuction(t)
		if _, _, err := a.Close(a.EndsAt); err != nil {
			t.Fatalf("first close: %v", err)
		}
		if _, _, err := a.Close(a.EndsAt.Add(time.Minute)); !errors.Is(err, auction.ErrNotActive) {
			t.Fatalf("second close err = %v, want ErrNotActive", err)
		}
	})
}

func TestSoftCloseKeepsAuctionBiddableUntilNewEnd(t *testing.T) {
	a := newTestAuction(t)
	late := baseTime.Add(59*time.Minute + 30*time.Second)
	if _, err := a.PlaceBid(bidderA, 1100, late); err != nil {
		t.Fatalf("late bid: %v", err)
	}
	// The original end (base+60m) has passed, but the extended end (late+60s) not yet.
	inBetween := baseTime.Add(60 * time.Minute)
	if _, err := a.PlaceBid(bidderB, 1200, inBetween); err != nil {
		t.Fatalf("bid inside extended window should be accepted: %v", err)
	}
}
