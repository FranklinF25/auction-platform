package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auth"
)

// SessionRepository implements auth.SessionStore on PostgreSQL.
type SessionRepository struct {
	pool *pgxpool.Pool
}

// NewSessionRepository returns a SessionRepository backed by pool.
func NewSessionRepository(pool *pgxpool.Pool) *SessionRepository {
	return &SessionRepository{pool: pool}
}

// Create inserts a session keyed by token hash.
func (r *SessionRepository) Create(ctx context.Context, s auth.Session) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO sessions (token_hash, user_id, expires_at)
		VALUES ($1, $2::uuid, $3)`,
		s.TokenHash, s.UserID, s.ExpiresAt)
	return err
}

// ByTokenHash loads a session; missing rows map to auth.ErrNotFound.
func (r *SessionRepository) ByTokenHash(ctx context.Context, tokenHash string) (auth.Session, error) {
	s := auth.Session{}
	err := r.pool.QueryRow(ctx, `
		SELECT token_hash, user_id, expires_at, created_at
		FROM sessions
		WHERE token_hash = $1`, tokenHash,
	).Scan(&s.TokenHash, &s.UserID, &s.ExpiresAt, &s.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Session{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.Session{}, err
	}
	return s, nil
}

// Delete removes a session; deleting an absent session is not an error.
func (r *SessionRepository) Delete(ctx context.Context, tokenHash string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}
