package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// SessionTTL is how long a login session lasts (30 days).
const SessionTTL = 30 * 24 * time.Hour

// sessionTokenBytes: raw session tokens carry 32 random bytes, hex-encoded.
const sessionTokenBytes = 32

// Service implements the auth use cases directly on the domain and ports —
// no extra usecase layer.
type Service struct {
	users    UserRepository
	sessions SessionStore
	hasher   PasswordHasher
	clock    Clock
}

// NewService wires the ports into the service.
func NewService(users UserRepository, sessions SessionStore, hasher PasswordHasher, clock Clock) *Service {
	return &Service{users: users, sessions: sessions, hasher: hasher, clock: clock}
}

// Register validates the input, hashes the password, persists the user and
// immediately starts a session. It returns the stored user and the raw
// session token; transports put the token in an httpOnly cookie and never
// persist it.
func (s *Service) Register(ctx context.Context, email, password, name string) (User, string, error) {
	email, name, err := validateRegistration(email, password, name)
	if err != nil {
		return User{}, "", err
	}

	// Friendly duplicate check; the database unique index remains the
	// authority under concurrency.
	if _, err := s.users.ByEmail(ctx, email); err == nil {
		return User{}, "", ErrEmailTaken
	} else if !errors.Is(err, ErrNotFound) {
		return User{}, "", err
	}

	hash, err := s.hasher.Hash(password)
	if err != nil {
		return User{}, "", err
	}
	user, err := s.users.Create(ctx, User{Email: email, PasswordHash: hash, Name: name})
	if err != nil {
		return User{}, "", err
	}

	token, err := s.startSession(ctx, user.ID)
	if err != nil {
		return User{}, "", err
	}
	return user, token, nil
}

// Login verifies credentials and starts a new session. Unknown email and
// wrong password both return ErrInvalidCredentials so login cannot be used
// to enumerate accounts.
func (s *Service) Login(ctx context.Context, email, password string) (User, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	user, err := s.users.ByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return User{}, "", ErrInvalidCredentials
		}
		return User{}, "", err
	}
	if err := s.hasher.Compare(user.PasswordHash, password); err != nil {
		return User{}, "", ErrInvalidCredentials
	}

	token, err := s.startSession(ctx, user.ID)
	if err != nil {
		return User{}, "", err
	}
	return user, token, nil
}

// Logout invalidates the session behind the raw token. Idempotent.
func (s *Service) Logout(ctx context.Context, rawToken string) error {
	return s.sessions.Delete(ctx, hashToken(rawToken))
}

// CurrentUser resolves a raw session token to its user, rejecting unknown
// and expired sessions with ErrNotFound.
func (s *Service) CurrentUser(ctx context.Context, rawToken string) (User, error) {
	session, err := s.sessions.ByTokenHash(ctx, hashToken(rawToken))
	if err != nil {
		return User{}, err
	}
	if !s.clock.Now().Before(session.ExpiresAt) {
		return User{}, ErrNotFound
	}
	user, err := s.users.ByID(ctx, session.UserID)
	if err != nil {
		return User{}, err
	}
	return user, nil
}

// startSession creates and stores a session, returning the raw token.
func (s *Service) startSession(ctx context.Context, userID string) (string, error) {
	raw, hash, err := newSessionToken()
	if err != nil {
		return "", err
	}
	err = s.sessions.Create(ctx, Session{
		TokenHash: hash,
		UserID:    userID,
		ExpiresAt: s.clock.Now().Add(SessionTTL),
	})
	if err != nil {
		return "", err
	}
	return raw, nil
}

// newSessionToken returns the raw hex token and its SHA-256 hex hash.
func newSessionToken() (raw, hash string, err error) {
	buf := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = hex.EncodeToString(buf)
	return raw, hashToken(raw), nil
}

// hashToken converts a raw token into the value stored in the sessions table.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
