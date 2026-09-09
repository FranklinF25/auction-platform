package postgres

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	ipostgres "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/FranklinF25/auction-platform/apps/api/migrations"
)

// RunMigrations applies all pending embedded migrations. Idempotent: an
// up-to-date database is a no-op (ErrNoChange is treated as success).
func RunMigrations(databaseURL string) error {
	src, err := iofs.New(migrations.FS, ".") // embed.go mounts the SQL files at the FS root
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open migrations connection: %w", err)
	}
	defer db.Close()

	driver, err := ipostgres.WithInstance(db, &ipostgres.Config{})
	if err != nil {
		return fmt.Errorf("migrations driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
