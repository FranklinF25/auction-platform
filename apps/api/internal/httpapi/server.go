// Package httpapi is the driving adapter: it exposes the auction and auth
// services over REST/JSON using chi, resolves the session cookie to a user,
// and renders a uniform error envelope. It contains no business rules.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/websocket"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
	"github.com/FranklinF25/auction-platform/apps/api/internal/auth"
)

// Config carries the environment-driven adapter settings.
type Config struct {
	// CookieSecure marks the session cookie Secure (true behind TLS).
	CookieSecure bool
	// PublicOrigin is API_PUBLIC_ORIGIN (default http://localhost:8080): the
	// browser-reachable origin of this API, advertised by GET /api/config so
	// web clients know where to open WebSocket connections directly. The Next
	// middleware proxy only handles REST; browsers connect to this origin
	// directly (session cookies are host-scoped and shared across ports).
	PublicOrigin string
}

// DefaultPublicOrigin is the advertised API origin when API_PUBLIC_ORIGIN
// is not set. Single source of truth: the handler falls back to it and the
// composition root uses it as the env default.
const DefaultPublicOrigin = "http://localhost:8080"

// sessionCookieName is the httpOnly cookie carrying the raw session token.
const sessionCookieName = "auction_session"

// wsHub is the narrow slice of the hub adapter the WS endpoint needs,
// declared here so this adapter depends on a port instead of the concrete
// hub package; the composition root passes the real *hub.Hub (which also
// implements auction.EventPublisher for the domain service).
type wsHub interface {
	// Subscribe registers a watcher for auctionID, returning its buffered
	// event feed (raw JSON bytes) and an idempotent unsubscribe func.
	Subscribe(auctionID string) (chan []byte, func())
	// Watchers returns the current watcher count for presence snapshots.
	Watchers(auctionID string) int
}

// Server wires the domain services into HTTP handlers.
type Server struct {
	auth     *auth.Service
	auctions *auction.Service
	ws       wsHub
	now      func() time.Time // stamps server_now on WS snapshots
	logger   *slog.Logger
	cfg      Config
	upgrader websocket.Upgrader
}

// New builds the complete handler with middleware. The composition root calls
// this once; tests call it with fake-backed services.
func New(authSvc *auth.Service, auctionSvc *auction.Service, h wsHub, now func() time.Time, logger *slog.Logger, cfg Config) http.Handler {
	s := &Server{auth: authSvc, auctions: auctionSvc, ws: h, now: now, logger: logger, cfg: cfg}
	s.upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     wsCheckOrigin,
	}

	r := chi.NewRouter()
	r.Use(requestLogger(logger))
	r.Use(middleware.Recoverer)
	r.Use(s.resolveSession)

	r.Get("/healthz", s.handleHealth)

	// The WebSocket endpoint lives outside /api: it upgrades, it does not
	// answer JSON (except the pre-upgrade 404/400).
	r.Get("/ws/auctions/{id}", s.handleWSAuction)

	r.Route("/api", func(r chi.Router) {
		r.Get("/config", s.handleConfig)

		r.Post("/auth/register", s.handleRegister)
		r.Post("/auth/login", s.handleLogin)
		r.Post("/auth/logout", s.handleLogout)
		r.Get("/me", s.requireAuth(s.handleMe))

		r.Post("/auctions", s.requireAuth(s.handleCreateAuction))
		r.Get("/auctions", s.handleListAuctions)
		r.Get("/auctions/{id}", s.handleGetAuction)
		r.Get("/auctions/{id}/bids", s.handleListBids)
		// The bid hot path: transactional DB write + post-commit broadcast.
		r.Post("/auctions/{id}/bids", s.requireAuth(s.handlePlaceBid))
	})

	return r
}

// handleConfig exposes the non-authenticated client bootstrap settings:
// ws_origin tells browsers which origin to open WebSocket connections
// against (see Config.PublicOrigin; defaults to DefaultPublicOrigin).
func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	origin := s.cfg.PublicOrigin
	if origin == "" {
		origin = DefaultPublicOrigin
	}
	writeJSON(w, http.StatusOK, configResponse{WSOrigin: origin})
}

type configResponse struct {
	WSOrigin string `json:"ws_origin"`
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
