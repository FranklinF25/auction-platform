package auth

import (
	"context"
	"time"
)

// Clock is the time port consumed by this package (session expiry). Declared
// consumer-side, mirroring auction.Clock; a single implementation satisfies
// both.
type Clock interface {
	Now() time.Time
}

// PasswordHasher hashes and verifies passwords. BcryptHasher (bcrypt.go) is
// the production implementation; tests use fakes for speed.
type PasswordHasher interface {
	Hash(password string) (string, error)
	Compare(hash, password string) error
}

// UserRepository persists users.
type UserRepository interface {
	// Create inserts u and returns the stored user with ID and CreatedAt set.
	// It returns ErrEmailTaken when the email is already registered.
	Create(ctx context.Context, u User) (User, error)
	// ByEmail looks a user up by exact (already lowercased) email; returns
	// ErrNotFound when absent.
	ByEmail(ctx context.Context, email string) (User, error)
	// ByID looks a user up by id; returns ErrNotFound when absent.
	ByID(ctx context.Context, id string) (User, error)
}

// SessionStore persists login sessions keyed by token hash.
type SessionStore interface {
	Create(ctx context.Context, s Session) error
	// ByTokenHash returns ErrNotFound when the session does not exist.
	ByTokenHash(ctx context.Context, tokenHash string) (Session, error)
	// Delete removes a session; deleting an absent session is not an error.
	Delete(ctx context.Context, tokenHash string) error
}
