// Command server is the composition root: it is the only file that sees both
// sides of the hexagon, wiring adapters (postgres, httpapi) into the domain
// services. All behavior lives in the domain and adapters; this file only
// wires and runs.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
	"github.com/FranklinF25/auction-platform/apps/api/internal/auth"
	"github.com/FranklinF25/auction-platform/apps/api/internal/httpapi"
	"github.com/FranklinF25/auction-platform/apps/api/internal/postgres"
)

// config is read from plain environment variables — no config library.
type config struct {
	DatabaseURL   string
	Port          string
	CookieSecure  bool
	RunMigrations bool
	LogLevel      string
}

func loadConfig() (config, error) {
	cfg := config{
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		Port:          envOr("PORT", "8080"),
		CookieSecure:  envBool("COOKIE_SECURE", false),
		RunMigrations: envBool("RUN_MIGRATIONS", true),
		LogLevel:      envOr("LOG_LEVEL", "info"),
	}
	if cfg.DatabaseURL == "" {
		return cfg, errors.New("DATABASE_URL is required")
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}

// systemClock implements auction.Clock and auth.Clock with system time; tests
// replace it with fakes.
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func main() {
	// Docker healthcheck subcommand: distroless ships no shell or wget, so the
	// binary probes its own /healthz endpoint (see docker-compose.yml).
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		os.Exit(healthcheck(envOr("PORT", "8080")))
	}
	if err := run(); err != nil {
		slog.Error("server exited with error", "err", err)
		os.Exit(1)
	}
}

// healthcheck returns 0 when GET /healthz answers 200, 1 otherwise.
func healthcheck(port string) int {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	logger := newLogger(cfg.LogLevel)

	pool, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if cfg.RunMigrations {
		if err := postgres.RunMigrations(cfg.DatabaseURL); err != nil {
			return fmt.Errorf("run migrations: %w", err)
		}
		logger.Info("migrations applied")
	}

	clock := systemClock{}
	userRepo := postgres.NewUserRepository(pool)
	sessionRepo := postgres.NewSessionRepository(pool)
	auctionRepo := postgres.NewAuctionRepository(pool)

	authSvc := auth.NewService(userRepo, sessionRepo, auth.BcryptHasher{}, clock)
	auctionSvc := auction.NewService(auctionRepo, clock)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           httpapi.New(authSvc, auctionSvc, logger, httpapi.Config{CookieSecure: cfg.CookieSecure}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("api listening", "addr", srv.Addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info("server stopped")
	return nil
}
