package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
	"github.com/FranklinF25/auction-platform/apps/api/internal/auth"
)

// errorEnvelope is the uniform error body: {"error":{"code","message"}}.
type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// writeError writes a uniform JSON error with the given status.
func writeError(w http.ResponseWriter, status int, code, message string) {
	var env errorEnvelope
	env.Error.Code = code
	env.Error.Message = message
	writeJSON(w, status, env)
}

// writeJSON marshals v with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeDomainError maps domain and service errors onto HTTP statuses:
// validation -> 400, auth failures -> 401, missing auctions -> 404,
// duplicate email -> 409, everything else -> 500 (logged).
func (s *Server) writeDomainError(w http.ResponseWriter, err error) {
	var authValErr *auth.ValidationError
	var auctionValErr *auction.ValidationError
	switch {
	case errors.As(err, &authValErr):
		writeError(w, http.StatusBadRequest, "validation_error", authValErr.Error())
	case errors.As(err, &auctionValErr):
		writeError(w, http.StatusBadRequest, "validation_error", auctionValErr.Error())
	case errors.Is(err, auth.ErrEmailTaken):
		writeError(w, http.StatusConflict, "email_taken", "that email is already registered")
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
	case errors.Is(err, auction.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "auction not found")
	default:
		s.logger.Error("internal error", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "something went wrong")
	}
}
