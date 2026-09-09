package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auth"
)

var authBaseTime = time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

type fakeHasher struct{}

func (fakeHasher) Hash(password string) (string, error) { return "hashed:" + password, nil }

func (fakeHasher) Compare(hash, password string) error {
	if hash != "hashed:"+password {
		return errors.New("password mismatch")
	}
	return nil
}

type fakeUserRepo struct {
	mu     sync.Mutex
	users  []auth.User
	nextID int
}

func (f *fakeUserRepo) Create(_ context.Context, u auth.User) (auth.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.users {
		if existing.Email == u.Email {
			return auth.User{}, auth.ErrEmailTaken
		}
	}
	f.nextID++
	u.ID = "user-" + itoa(f.nextID)
	u.CreatedAt = authBaseTime
	f.users = append(f.users, u)
	return u, nil
}

func (f *fakeUserRepo) ByEmail(_ context.Context, email string) (auth.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u.Email == email {
			return u, nil
		}
	}
	return auth.User{}, auth.ErrNotFound
}

func (f *fakeUserRepo) ByID(_ context.Context, id string) (auth.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u.ID == id {
			return u, nil
		}
	}
	return auth.User{}, auth.ErrNotFound
}

type fakeSessionStore struct {
	mu       sync.Mutex
	sessions map[string]auth.Session
}

func (f *fakeSessionStore) Create(_ context.Context, s auth.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[s.TokenHash] = s
	return nil
}

func (f *fakeSessionStore) ByTokenHash(_ context.Context, hash string) (auth.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[hash]
	if !ok {
		return auth.Session{}, auth.ErrNotFound
	}
	return s, nil
}

func (f *fakeSessionStore) Delete(_ context.Context, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, hash)
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func newAuthService(t *testing.T) (*auth.Service, *fakeUserRepo, *fakeSessionStore, *fakeClock) {
	t.Helper()
	users := &fakeUserRepo{}
	sessions := &fakeSessionStore{sessions: map[string]auth.Session{}}
	clock := &fakeClock{t: authBaseTime}
	svc := auth.NewService(users, sessions, fakeHasher{}, clock)
	return svc, users, sessions, clock
}

func TestRegisterHappyPath(t *testing.T) {
	svc, users, sessions, _ := newAuthService(t)

	user, token, err := svc.Register(context.Background(), "  Ada@Example.COM ", "correct horse", "  Ada Lovelace  ")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if user.Email != "ada@example.com" {
		t.Errorf("user.Email = %q, want lowercased %q", user.Email, "ada@example.com")
	}
	if user.Name != "Ada Lovelace" {
		t.Errorf("user.Name = %q, want trimmed %q", user.Name, "Ada Lovelace")
	}
	if user.ID == "" {
		t.Error("user.ID is empty; repository must assign it")
	}

	// The raw token is 64 hex chars (32 random bytes) and never stored.
	if len(token) != 64 {
		t.Fatalf("token length = %d, want 64 hex chars", len(token))
	}
	if _, err := hex.DecodeString(token); err != nil {
		t.Fatalf("token is not hex: %v", err)
	}
	sum := sha256.Sum256([]byte(token))
	storedHash := hex.EncodeToString(sum[:])
	if storedHash == token {
		t.Fatal("token hash must differ from the raw token")
	}

	got, err := sessions.ByTokenHash(context.Background(), storedHash)
	if err != nil {
		t.Fatalf("session not stored under sha256 hash: %v", err)
	}
	if got.UserID != user.ID {
		t.Errorf("session.UserID = %q, want %q", got.UserID, user.ID)
	}
	if want := authBaseTime.Add(30 * 24 * time.Hour); !got.ExpiresAt.Equal(want) {
		t.Errorf("session.ExpiresAt = %v, want %v", got.ExpiresAt, want)
	}

	stored, err := users.ByEmail(context.Background(), "ada@example.com")
	if err != nil {
		t.Fatalf("stored user not found: %v", err)
	}
	if stored.PasswordHash == "correct horse" || stored.PasswordHash == "" {
		t.Errorf("PasswordHash = %q, want a hashed value", stored.PasswordHash)
	}
}

func TestRegisterValidation(t *testing.T) {
	tests := []struct {
		name     string
		email    string
		password string
		userName string
		field    string
	}{
		{"email without at", "userexample.com", "password123", "Ada", "email"},
		{"email without local part", "@example.com", "password123", "Ada", "email"},
		{"email without domain", "user@", "password123", "Ada", "email"},
		{"email with two ats", "a@b@c", "password123", "Ada", "email"},
		{"email with whitespace", "a b@example.com", "password123", "Ada", "email"},
		{"empty email", "", "password123", "Ada", "email"},
		{"password too short", "ada@example.com", "1234567", "Ada", "password"},
		{"password too long for bcrypt", "ada@example.com", strings.Repeat("x", 73), "Ada", "password"},
		{"empty name", "ada@example.com", "password123", "", "name"},
		{"name over 100 chars", "ada@example.com", "password123", strings.Repeat("n", 101), "name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, sessions, _ := newAuthService(t)
			_, token, err := svc.Register(context.Background(), tt.email, tt.password, tt.userName)
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			var verr *auth.ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("expected *auth.ValidationError, got %T: %v", err, err)
			}
			if verr.Field != tt.field {
				t.Errorf("error field = %q, want %q", verr.Field, tt.field)
			}
			if token != "" {
				t.Error("no session token may be issued on validation failure")
			}
			if n := len(sessions.sessions); n != 0 {
				t.Errorf("%d sessions created on validation failure, want 0", n)
			}
		})
	}
}

func TestRegisterDuplicateEmail(t *testing.T) {
	svc, _, _, _ := newAuthService(t)
	if _, _, err := svc.Register(context.Background(), "ada@example.com", "password123", "Ada"); err != nil {
		t.Fatalf("first register: %v", err)
	}
	_, _, err := svc.Register(context.Background(), "ada@example.com", "password456", "Impostor")
	if !errors.Is(err, auth.ErrEmailTaken) {
		t.Fatalf("err = %v, want ErrEmailTaken", err)
	}
}

func TestLoginHappyPath(t *testing.T) {
	svc, _, sessions, _ := newAuthService(t)
	if _, _, err := svc.Register(context.Background(), "ada@example.com", "password123", "Ada"); err != nil {
		t.Fatalf("register: %v", err)
	}
	before := len(sessions.sessions)

	user, token, err := svc.Login(context.Background(), "ADA@example.com", "password123")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if user.Email != "ada@example.com" {
		t.Errorf("user.Email = %q, want %q", user.Email, "ada@example.com")
	}
	if len(token) != 64 {
		t.Errorf("token length = %d, want 64", len(token))
	}
	if got := len(sessions.sessions); got != before+1 {
		t.Errorf("sessions = %d, want %d (login creates a session)", got, before+1)
	}
}

func TestLoginFailures(t *testing.T) {
	t.Run("unknown email", func(t *testing.T) {
		svc, _, _, _ := newAuthService(t)
		_, _, err := svc.Login(context.Background(), "ghost@example.com", "password123")
		if !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("err = %v, want ErrInvalidCredentials", err)
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		svc, _, _, _ := newAuthService(t)
		if _, _, err := svc.Register(context.Background(), "ada@example.com", "password123", "Ada"); err != nil {
			t.Fatalf("register: %v", err)
		}
		_, _, err := svc.Login(context.Background(), "ada@example.com", "wrong-password")
		if !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("err = %v, want ErrInvalidCredentials", err)
		}
	})
}

func TestLogoutDeletesSession(t *testing.T) {
	svc, _, sessions, _ := newAuthService(t)
	_, token, err := svc.Register(context.Background(), "ada@example.com", "password123", "Ada")
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := svc.Logout(context.Background(), token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if got := len(sessions.sessions); got != 0 {
		t.Errorf("sessions after logout = %d, want 0", got)
	}
	// Logout of an already-removed token is idempotent.
	if err := svc.Logout(context.Background(), token); err != nil {
		t.Fatalf("second Logout: %v", err)
	}
}

func TestCurrentUser(t *testing.T) {
	t.Run("valid token resolves to user", func(t *testing.T) {
		svc, _, _, _ := newAuthService(t)
		registered, token, err := svc.Register(context.Background(), "ada@example.com", "password123", "Ada")
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		user, err := svc.CurrentUser(context.Background(), token)
		if err != nil {
			t.Fatalf("CurrentUser: %v", err)
		}
		if user.ID != registered.ID || user.Email != registered.Email {
			t.Errorf("CurrentUser = %+v, want %+v", user, registered)
		}
	})

	t.Run("unknown token is rejected", func(t *testing.T) {
		svc, _, _, _ := newAuthService(t)
		if _, err := svc.CurrentUser(context.Background(), strings.Repeat("ab", 32)); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("expired session is rejected", func(t *testing.T) {
		svc, _, _, clock := newAuthService(t)
		_, token, err := svc.Register(context.Background(), "ada@example.com", "password123", "Ada")
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		clock.t = authBaseTime.Add(31 * 24 * time.Hour)
		if _, err := svc.CurrentUser(context.Background(), token); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound for expired session", err)
		}
	})
}
