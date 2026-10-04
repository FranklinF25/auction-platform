package auction_test

// The pure Transaction.Pay state machine: payable states, the expiry gate,
// card validation (digit count only — the simulation is the point), the demo
// decline card and the terminal completed state.

import (
	"errors"
	"testing"
	"time"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
)

var txBaseTime = time.Date(2025, 8, 1, 12, 0, 0, 0, time.UTC)

// newTransaction returns a pending transaction created at txBaseTime with a
// 48-hour payment window, so every test shares one honest starting state.
func newTransaction(status auction.Status) *auction.Transaction {
	paidAt := txBaseTime.Add(5 * time.Minute)
	t := &auction.Transaction{
		ID:          "tx-1",
		AuctionID:   "auction-1",
		WinnerID:    "bidder-1",
		AmountCents: 5000,
		Status:      status,
		CreatedAt:   txBaseTime,
		ExpiresAt:   txBaseTime.Add(auction.TransactionExpiryDuration),
	}
	if status == auction.TransactionCompleted {
		t.PaidAt = &paidAt
	}
	return t
}

func TestPayHappyPathCompletes(t *testing.T) {
	txn := newTransaction(auction.TransactionPending)
	now := txBaseTime.Add(time.Hour)

	if err := txn.Pay("4111111111111111", now); err != nil {
		t.Fatalf("Pay: %v", err)
	}
	if txn.Status != auction.TransactionCompleted {
		t.Errorf("status = %q, want completed", txn.Status)
	}
	if txn.PaidAt == nil || !txn.PaidAt.Equal(now) {
		t.Errorf("paid_at = %v, want %v", txn.PaidAt, now)
	}
}

func TestPayAcceptsFormattedCardNumbers(t *testing.T) {
	// Spaces and dashes between digit groups are stripped before counting.
	for _, card := range []string{"4111 1111 1111 1111", "4111-1111-1111-1111"} {
		txn := newTransaction(auction.TransactionPending)
		if err := txn.Pay(card, txBaseTime); err != nil {
			t.Fatalf("Pay(%q): %v", card, err)
		}
		if txn.Status != auction.TransactionCompleted {
			t.Errorf("Pay(%q): status = %q, want completed", card, txn.Status)
		}
	}
}

func TestPayDeclineCardMarksFailed(t *testing.T) {
	txn := newTransaction(auction.TransactionPending)

	if err := txn.Pay(auction.DeclinedCardNumber, txBaseTime); err != nil {
		t.Fatalf("a decline is an outcome, not an error: %v", err)
	}
	if txn.Status != auction.TransactionFailed {
		t.Errorf("status = %q, want failed", txn.Status)
	}
	if txn.PaidAt != nil {
		t.Errorf("paid_at = %v, want nil on a decline", txn.PaidAt)
	}
}

func TestPayRetryAfterFailedSucceeds(t *testing.T) {
	txn := newTransaction(auction.TransactionPending)
	if err := txn.Pay(auction.DeclinedCardNumber, txBaseTime); err != nil {
		t.Fatalf("first attempt: %v", err)
	}

	// Failed is retryable while the window is open (PRD E3).
	now := txBaseTime.Add(time.Hour)
	if err := txn.Pay("5500005555555559", now); err != nil {
		t.Fatalf("retry after decline: %v", err)
	}
	if txn.Status != auction.TransactionCompleted {
		t.Errorf("status = %q, want completed after a successful retry", txn.Status)
	}
	if txn.PaidAt == nil || !txn.PaidAt.Equal(now) {
		t.Errorf("paid_at = %v, want the retry instant %v", txn.PaidAt, now)
	}
}

func TestPayExpiredWindowIsRejected(t *testing.T) {
	cases := []struct {
		name   string
		at     time.Time
		status auction.Status
	}{
		{"pending past expires_at", txBaseTime.Add(auction.TransactionExpiryDuration), auction.TransactionPending},
		{"pending exactly at expires_at", newTransaction(auction.TransactionPending).ExpiresAt, auction.TransactionPending},
		{"already expired by the sweeper", txBaseTime.Add(time.Minute), auction.TransactionExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			txn := newTransaction(tc.status)
			txn.ExpiresAt = txBaseTime.Add(auction.TransactionExpiryDuration)
			err := txn.Pay("4111111111111111", tc.at)
			if !errors.Is(err, auction.ErrTransactionExpired) {
				t.Fatalf("want ErrTransactionExpired, got %v", err)
			}
			if txn.Status != tc.status {
				t.Errorf("a rejected attempt must not change status: got %q, want %q", txn.Status, tc.status)
			}
			if txn.PaidAt != nil {
				t.Error("a rejected attempt must not stamp paid_at")
			}
		})
	}
}

func TestPayAlreadyCompletedIsRejected(t *testing.T) {
	txn := newTransaction(auction.TransactionCompleted)

	err := txn.Pay("4111111111111111", txBaseTime.Add(time.Minute))
	if !errors.Is(err, auction.ErrTransactionCompleted) {
		t.Fatalf("want ErrTransactionCompleted, got %v", err)
	}
	if txn.Status != auction.TransactionCompleted {
		t.Errorf("status = %q, want unchanged completed", txn.Status)
	}
}

func TestPayMalformedCardIsValidationError(t *testing.T) {
	cases := []struct {
		name string
		card string
	}{
		{"too short", "41111111111"},         // 11 digits
		{"too long", "41111111111111111190"}, // 20 digits
		{"letters", "4111abcd11111111"},      // non-digits
		{"empty", ""},
		{"separators only", "  --  "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			txn := newTransaction(auction.TransactionPending)
			err := txn.Pay(tc.card, txBaseTime)
			var valErr *auction.ValidationError
			if !errors.As(err, &valErr) {
				t.Fatalf("want *ValidationError, got %v", err)
			}
			if valErr.Field != "card_number" {
				t.Errorf("field = %q, want card_number", valErr.Field)
			}
			if txn.Status != auction.TransactionPending {
				t.Errorf("status = %q, want unchanged pending", txn.Status)
			}
		})
	}

	// Boundary lengths are valid: 12 and 19 digits pay.
	for _, card := range []string{"123456789012", "1234567890123456789"} {
		txn := newTransaction(auction.TransactionPending)
		if err := txn.Pay(card, txBaseTime); err != nil {
			t.Errorf("Pay(%q) with %d digits: %v", card, len(card), err)
		}
	}
}
