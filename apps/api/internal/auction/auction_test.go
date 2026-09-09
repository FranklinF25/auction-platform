package auction_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
)

// baseTime is the fixed "now" every test uses; the Clock port makes this the
// only notion of time in the domain.
var baseTime = time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

func validInput() auction.CreateInput {
	return auction.CreateInput{
		Title:              "Vintage camera",
		Description:        "A nice camera",
		StartingPriceCents: 1000,
		DurationMinutes:    60,
	}
}

func mustNew(t *testing.T, in auction.CreateInput) *auction.Auction {
	t.Helper()
	a, err := auction.NewAuction("seller-1", in, baseTime)
	if err != nil {
		t.Fatalf("NewAuction returned unexpected error: %v", err)
	}
	return a
}

func TestNewAuctionValid(t *testing.T) {
	t.Run("minimal input applies defaults", func(t *testing.T) {
		a := mustNew(t, validInput())
		if a.Status != auction.StatusActive {
			t.Errorf("status = %q, want %q", a.Status, auction.StatusActive)
		}
		if a.MinIncrementCents != 100 {
			t.Errorf("MinIncrementCents = %d, want default 100", a.MinIncrementCents)
		}
		if a.ReservePriceCents != nil {
			t.Errorf("ReservePriceCents = %v, want nil", *a.ReservePriceCents)
		}
		if !a.EndsAt.Equal(baseTime.Add(60 * time.Minute)) {
			t.Errorf("EndsAt = %v, want %v", a.EndsAt, baseTime.Add(60*time.Minute))
		}
		if !a.CreatedAt.Equal(baseTime) {
			t.Errorf("CreatedAt = %v, want %v", a.CreatedAt, baseTime)
		}
	})

	t.Run("explicit min increment is kept", func(t *testing.T) {
		in := validInput()
		in.MinIncrementCents = 250
		a := mustNew(t, in)
		if a.MinIncrementCents != 250 {
			t.Errorf("MinIncrementCents = %d, want 250", a.MinIncrementCents)
		}
	})

	t.Run("reserve equal to starting price is allowed", func(t *testing.T) {
		in := validInput()
		reserve := int64(1000)
		in.ReservePriceCents = &reserve
		a := mustNew(t, in)
		if a.ReservePriceCents == nil || *a.ReservePriceCents != 1000 {
			t.Errorf("ReservePriceCents = %v, want 1000", a.ReservePriceCents)
		}
	})

	t.Run("duration boundaries 1 minute and 14 days", func(t *testing.T) {
		for _, minutes := range []int{1, 20160} {
			in := validInput()
			in.DurationMinutes = minutes
			a, err := auction.NewAuction("seller-1", in, baseTime)
			if err != nil {
				t.Fatalf("duration %d: unexpected error %v", minutes, err)
			}
			want := baseTime.Add(time.Duration(minutes) * time.Minute)
			if !a.EndsAt.Equal(want) {
				t.Errorf("duration %d: EndsAt = %v, want %v", minutes, a.EndsAt, want)
			}
		}
	})

	t.Run("title of 200 chars and description of 5000 chars are accepted", func(t *testing.T) {
		in := validInput()
		in.Title = strings.Repeat("t", 200)
		in.Description = strings.Repeat("d", 5000)
		if _, err := auction.NewAuction("seller-1", in, baseTime); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("title and description are trimmed", func(t *testing.T) {
		in := validInput()
		in.Title = "  Camera  "
		in.Description = " Nice "
		a := mustNew(t, in)
		if a.Title != "Camera" {
			t.Errorf("Title = %q, want %q", a.Title, "Camera")
		}
		if a.Description != "Nice" {
			t.Errorf("Description = %q, want %q", a.Description, "Nice")
		}
	})
}

func TestNewAuctionValidation(t *testing.T) {
	reserveBelow := int64(999)

	tests := []struct {
		name      string
		mutate    func(*auction.CreateInput)
		wantField string
	}{
		{"empty title", func(in *auction.CreateInput) { in.Title = "" }, "title"},
		{"whitespace-only title", func(in *auction.CreateInput) { in.Title = "   " }, "title"},
		{"title over 200 chars", func(in *auction.CreateInput) { in.Title = strings.Repeat("t", 201) }, "title"},
		{"description over 5000 chars", func(in *auction.CreateInput) { in.Description = strings.Repeat("d", 5001) }, "description"},
		{"starting price of zero", func(in *auction.CreateInput) { in.StartingPriceCents = 0 }, "starting_price_cents"},
		{"negative starting price", func(in *auction.CreateInput) { in.StartingPriceCents = -100 }, "starting_price_cents"},
		{"negative min increment", func(in *auction.CreateInput) { in.MinIncrementCents = -1 }, "min_increment_cents"},
		{"reserve below starting price", func(in *auction.CreateInput) { in.ReservePriceCents = &reserveBelow }, "reserve_price_cents"},
		{"duration of zero minutes", func(in *auction.CreateInput) { in.DurationMinutes = 0 }, "duration_minutes"},
		{"negative duration", func(in *auction.CreateInput) { in.DurationMinutes = -5 }, "duration_minutes"},
		{"duration over 14 days", func(in *auction.CreateInput) { in.DurationMinutes = 20161 }, "duration_minutes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validInput()
			tt.mutate(&in)
			_, err := auction.NewAuction("seller-1", in, baseTime)
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			var verr *auction.ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("expected *auction.ValidationError, got %T: %v", err, err)
			}
			if verr.Field != tt.wantField {
				t.Errorf("error field = %q, want %q (message: %q)", verr.Field, tt.wantField, verr.Reason)
			}
		})
	}
}

func TestNewAuctionRequiresSeller(t *testing.T) {
	if _, err := auction.NewAuction("", validInput(), baseTime); err == nil {
		t.Fatal("expected error for empty seller id, got nil")
	}
}

func TestDerivedValuesWithoutBids(t *testing.T) {
	a := mustNew(t, validInput())
	if got := a.CurrentPrice(); got != 1000 {
		t.Errorf("CurrentPrice() = %d, want 1000 (starting price)", got)
	}
	if !a.ReserveMet() {
		t.Error("ReserveMet() = false, want true when no reserve is set")
	}
	if got := a.BidCount(); got != 0 {
		t.Errorf("BidCount() = %d, want 0", got)
	}
}
