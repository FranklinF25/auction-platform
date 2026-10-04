// Checkout domain: the Transaction a winner owes after winning an auction,
// its simulated payment state machine and the sentinel errors adapters map.
// Like Bid, a Transaction is part of the auction lifecycle and lives in this
// package — no separate package. Payment is simulated: the point of M4 is the
// checkout flow (pending -> completed/failed/expired), not card processing.

package auction

import (
	"errors"
	"strings"
	"time"
)

// TransactionExpiryDuration is how long a winner has to pay after the auction
// closes: the closing sweep stamps expires_at = close time + 48h, and the
// expiry sweeper moves unpaid (pending or failed) transactions to expired
// once that instant passes.
const TransactionExpiryDuration = 48 * time.Hour

// DeclinedCardNumber is the demo decline card: paying with exactly this card
// number marks the transaction failed so the retry flow can be exercised.
// This is a simulation constant — there is no real card validation beyond the
// digit count, and this exact number is the only "declined" one.
const DeclinedCardNumber = "4000000000000002"

// Transaction statuses. They reuse the Status string type (uniform SQL/JSON
// mapping with auction statuses) but form a disjoint lifecycle: pending is the
// state the closing sweep creates, failed means a declined payment attempt
// (retryable until expiry), completed means paid, expired means the payment
// window closed unpaid.
const (
	TransactionPending   Status = "pending"
	TransactionCompleted Status = "completed"
	TransactionFailed    Status = "failed"
	TransactionExpired   Status = "expired"
)

// Sentinel errors for the payment flow. Adapters map them to transport
// statuses: ErrTransactionCompleted -> 409, ErrTransactionExpired -> 409.
var (
	// ErrTransactionCompleted: the transaction was already paid; paying again
	// is rejected (a completed sale is terminal).
	ErrTransactionCompleted = errors.New("transaction is already completed")
	// ErrTransactionExpired: the 48-hour payment window has closed, so the
	// transaction can no longer be paid.
	ErrTransactionExpired = errors.New("payment window has expired")
)

// Transaction is the payment a winner owes for a won auction. Exactly one
// exists per sold auction (UNIQUE auction_id in storage); the closing sweep
// creates it as pending with amount = final price at close. PaidAt is nil
// until the payment completes.
type Transaction struct {
	ID          string
	AuctionID   string
	WinnerID    string
	AmountCents int64
	Status      Status
	CreatedAt   time.Time
	ExpiresAt   time.Time
	PaidAt      *time.Time
}

// Pay applies one simulated payment attempt to the transaction, mutating its
// status in place. Pure — no IO: repositories run it inside their payment
// transaction (SELECT ... FOR UPDATE; Pay; UPDATE; COMMIT).
//
// State machine: only pending and failed are payable (a failed attempt is
// retryable until the window closes); completed is terminal
// (ErrTransactionCompleted); anything else is expired. now must be before
// ExpiresAt or the attempt fails with ErrTransactionExpired (this also covers
// transactions the expiry sweeper has not flipped yet). The card must be 12
// to 19 digits after stripping spaces and dashes, else *ValidationError on
// field card_number. Paying with DeclinedCardNumber marks the transaction
// failed (demo decline, documented above) and returns nil — a decline is an
// outcome, not an error. Any other valid card completes the payment and
// stamps PaidAt = now.
func (t *Transaction) Pay(cardNumber string, now time.Time) error {
	if t.Status == TransactionCompleted {
		return ErrTransactionCompleted
	}
	if t.Status != TransactionPending && t.Status != TransactionFailed {
		// The only other stored status is expired; the sweeper already closed
		// the payment window.
		return ErrTransactionExpired
	}
	if !now.Before(t.ExpiresAt) {
		return ErrTransactionExpired
	}

	digits := normalizeCard(cardNumber)
	if len(digits) < 12 || len(digits) > 19 || !isAllDigits(digits) {
		return &ValidationError{Field: "card_number", Reason: "must contain 12 to 19 digits"}
	}

	if digits == DeclinedCardNumber {
		t.Status = TransactionFailed
		return nil // declined: retryable while the window is open
	}
	t.Status = TransactionCompleted
	paidAt := now
	t.PaidAt = &paidAt
	return nil
}

// normalizeCard strips the separators people type between digit groups.
func normalizeCard(cardNumber string) string {
	cardNumber = strings.ReplaceAll(cardNumber, " ", "")
	return strings.ReplaceAll(cardNumber, "-", "")
}

// isAllDigits reports whether s consists solely of ASCII digits.
func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
