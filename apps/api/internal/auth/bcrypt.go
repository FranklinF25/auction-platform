package auth

// NOTE: this file is the single documented exception to the "standard library
// only" rule of the auth package. It adapts golang.org/x/crypto/bcrypt to the
// PasswordHasher port declared in ports.go; the pure domain files (user.go,
// service.go, ports.go) never import it, so they stay stdlib-only and the
// compiler keeps that guarantee for the domain.

import "golang.org/x/crypto/bcrypt"

// BcryptHasher implements PasswordHasher with bcrypt. A zero Cost means
// bcrypt.DefaultCost; tests may lower it (bcrypt.MinCost) for speed.
type BcryptHasher struct {
	Cost int
}

// Hash returns the bcrypt hash of password.
func (h BcryptHasher) Hash(password string) (string, error) {
	cost := h.Cost
	if cost == 0 {
		cost = bcrypt.DefaultCost
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// Compare reports whether password matches hash.
func (h BcryptHasher) Compare(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}
