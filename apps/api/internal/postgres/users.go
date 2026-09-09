package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auth"
)

// UserRepository implements auth.UserRepository on PostgreSQL.
type UserRepository struct {
	pool *pgxpool.Pool
}

// NewUserRepository returns a UserRepository backed by pool.
func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
	return &UserRepository{pool: pool}
}

// Create inserts a user and returns it with the database-generated id and
// created_at. A unique-email violation maps to auth.ErrEmailTaken.
func (r *UserRepository) Create(ctx context.Context, u auth.User) (auth.User, error) {
	err := r.pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, name)
		VALUES ($1, $2, $3)
		RETURNING id, created_at`,
		u.Email, u.PasswordHash, u.Name,
	).Scan(&u.ID, &u.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return auth.User{}, auth.ErrEmailTaken
		}
		return auth.User{}, err
	}
	return u, nil
}

// ByEmail finds a user by exact email (the service normalizes to lower case
// before calling; the index on lower(email) makes this a direct hit).
func (r *UserRepository) ByEmail(ctx context.Context, email string) (auth.User, error) {
	u := auth.User{}
	err := r.pool.QueryRow(ctx, `
		SELECT id, email, password_hash, name, created_at
		FROM users
		WHERE email = lower($1)`, email,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Name, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.User{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.User{}, err
	}
	return u, nil
}

// ByID finds a user by id.
func (r *UserRepository) ByID(ctx context.Context, id string) (auth.User, error) {
	u := auth.User{}
	err := r.pool.QueryRow(ctx, `
		SELECT id, email, password_hash, name, created_at
		FROM users
		WHERE id = $1::uuid`, id,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Name, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.User{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.User{}, err
	}
	return u, nil
}

// isUniqueViolation reports whether err is a PostgreSQL 23505 error.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
