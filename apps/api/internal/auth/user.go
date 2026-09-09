// Package auth holds the pure authentication domain: users and sessions, the
// ports they consume (UserRepository, SessionStore, PasswordHasher, Clock)
// and the service implementing register, login, logout and current-user.
//
// Hexagonal rule: files in this package import the Go standard library only,
// with one documented exception — bcrypt.go, which adapts golang.org/x/crypto
// to the PasswordHasher port declared here so the pure domain files stay
// stdlib-only.
package auth

import (
	"errors"
	"strings"
	"time"
)

// User is a registered account. Email is normalized to lower case and unique
// case-insensitively; PasswordHash is opaque (bcrypt).
type User struct {
	ID           string
	Email        string
	PasswordHash string
	Name         string
	CreatedAt    time.Time
}

// Session is a persisted login session. Only TokenHash (SHA-256 hex of the
// raw cookie token) is stored; the raw token never touches storage.
type Session struct {
	TokenHash string
	UserID    string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// Sentinel errors mapped by adapters to transport statuses.
var (
	// ErrEmailTaken: register attempted with an already-registered email (409).
	ErrEmailTaken = errors.New("email is already registered")
	// ErrInvalidCredentials: login with a wrong email or password (401).
	ErrInvalidCredentials = errors.New("invalid email or password")
	// ErrNotFound: the requested user or session does not exist (404/401).
	ErrNotFound = errors.New("not found")
)

// ValidationError reports invalid register/login input. Kept as a distinct
// type per domain package (no shared kernel) so adapters can map it to 400.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Reason }

func invalid(field, reason string) error {
	return &ValidationError{Field: field, Reason: reason}
}

// Registration limits.
const (
	maxEmailLength    = 254
	minPasswordLength = 8
	maxPasswordLength = 72 // bcrypt silently truncates beyond 72 bytes
	maxNameLength     = 100
)

// validateRegistration normalizes email and name and enforces the input
// rules: a reasonable email shape (exactly one @, non-empty parts, no
// whitespace, sane length), an 8..72 character password, and a 1..100
// character name. It returns the normalized email and name.
func validateRegistration(email, password, name string) (string, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	name = strings.TrimSpace(name)

	if email == "" || len(email) > maxEmailLength ||
		strings.Count(email, "@") != 1 || strings.ContainsAny(email, " \t") {
		return "", "", invalid("email", "must be a valid email address")
	}
	local, domain, _ := strings.Cut(email, "@")
	if local == "" || domain == "" {
		return "", "", invalid("email", "must be a valid email address")
	}
	if len(password) < minPasswordLength || len(password) > maxPasswordLength {
		return "", "", invalid("password", "must be between 8 and 72 characters")
	}
	if len(name) < 1 || len(name) > maxNameLength {
		return "", "", invalid("name", "must be between 1 and 100 characters")
	}
	return email, name, nil
}
