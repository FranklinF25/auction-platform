package auth_test

import (
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auth"
)

// The bcrypt adapter is the one file in auth that may import x/crypto; this
// test verifies the PasswordHasher port round-trips through the real bcrypt.
func TestBcryptHasherRoundTrip(t *testing.T) {
	h := auth.BcryptHasher{Cost: bcrypt.MinCost}

	hash, err := h.Hash("s3cret-passphrase")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if hash == "s3cret-passphrase" || len(hash) < 20 {
		t.Errorf("Hash produced %q, want an opaque bcrypt hash", hash)
	}
	if err := h.Compare(hash, "s3cret-passphrase"); err != nil {
		t.Errorf("Compare with matching password: %v", err)
	}
	if err := h.Compare(hash, "wrong"); err == nil {
		t.Error("Compare with wrong password must fail")
	}
}

func TestBcryptHasherDefaultCostWhenZero(t *testing.T) {
	h := auth.BcryptHasher{Cost: 0}
	hash, err := h.Hash("password123")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	// bcrypt hashes start with $2a$/$2b$ followed by the two-digit cost.
	if got := hash[4:6]; got != "10" {
		t.Errorf("cost = %q, want default cost 10", got)
	}
}
