package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auth"
)

// credentialsRequest is the register/login body.
type credentialsRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

type userResponse struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

func newUserResponse(u auth.User) userResponse {
	return userResponse{ID: u.ID, Email: u.Email, Name: u.Name}
}

// setSessionCookie writes the raw token into an httpOnly cookie. The cookie
// carries SameSite=Lax and, behind TLS (COOKIE_SECURE), the Secure flag.
func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(auth.SessionTTL.Seconds()),
	})
}

// clearSessionCookie expires the cookie client-side.
func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req credentialsRequest
	if !decodeJSON(w, r, &req) {
		return // decodeJSON already wrote the error
	}
	user, token, err := s.auth.Register(r.Context(), req.Email, req.Password, req.Name)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	s.setSessionCookie(w, token)
	writeJSON(w, http.StatusCreated, newUserResponse(user))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req credentialsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	user, token, err := s.auth.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	s.setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, newUserResponse(user))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Idempotent: a missing or unknown cookie still clears client-side state.
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		if err := s.auth.Logout(r.Context(), c.Value); err != nil {
			s.writeDomainError(w, err)
			return
		}
	}
	s.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	writeJSON(w, http.StatusOK, newUserResponse(user))
}

// decodeJSON decodes the request body into v, answering 400 on malformed
// JSON. It reports whether decoding succeeded.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return false
	}
	return true
}
