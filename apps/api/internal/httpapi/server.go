// Package httpapi is the driving adapter: it exposes the auction and auth
// services over REST/JSON using chi, resolves the session cookie to a user,
// and renders a uniform error envelope. It contains no business rules.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
	"github.com/FranklinF25/auction-platform/apps/api/internal/auth"
)

// Config carries the environment-driven adapter settings.
type Config struct {
	// CookieSecure marks the session cookie Secure (true behind TLS).
	CookieSecure bool
}

// sessionCookieName is the httpOnly cookie carrying the raw session token.
const sessionCookieName = "auction_session"

// Server wires the domain services into HTTP handlers.
type Server struct {
	auth     *auth.Service
	auctions *auction.Service
	logger   *slog.Logger
	cfg      Config
}

// New builds the complete handler with middleware. The composition root calls
// this once; tests call it with fake-backed services.
func New(authSvc *auth.Service, auctionSvc *auction.Service, logger *slog.Logger, cfg Config) http.Handler {
	s := &Server{auth: authSvc, auctions: auctionSvc, logger: logger, cfg: cfg}

	r := chi.NewRouter()
	r.Use(requestLogger(logger))
	r.Use(middleware.Recoverer)
	r.Use(s.resolveSession)

	r.Get("/healthz", s.handleHealth)

	r.Route("/api", func(r chi.Router) {
		r.Post("/auth/register", s.handleRegister)
		r.Post("/auth/login", s.handleLogin)
		r.Post("/auth/logout", s.handleLogout)
		r.Get("/me", s.requireAuth(s.handleMe))

		r.Post("/auctions", s.requireAuth(s.handleCreateAuction))
		r.Get("/auctions", s.handleListAuctions)
		r.Get("/auctions/{id}", s.handleGetAuction)
		r.Get("/auctions/{id}/bids", s.handleListBids)
	})

	return r
}

// handleHealth is the compose healthcheck endpoint.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type userCtxKey struct{}

var ctxKeyUser userCtxKey

// resolveSession middleware: if a valid session cookie is present, load the
// user and store it in the request context. Invalid or missing cookies just
// mean the request is anonymous; requireAuth enforces endpoints that are not.
func (s *Server) resolveSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
			if user, err := s.auth.CurrentUser(r.Context(), c.Value); err == nil {
				r = r.WithContext(context.WithValue(r.Context(), ctxKeyUser, user))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// UserFrom returns the authenticated user from the context, if any.
func UserFrom(ctx context.Context) (auth.User, bool) {
	user, ok := ctx.Value(ctxKeyUser).(auth.User)
	return user, ok
}

// requireAuth wraps handlers that need a logged-in user, answering 401
// otherwise.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := UserFrom(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		next(w, r)
	}
}
