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
// bid rule violations -> 403/409, duplicate email -> 409, everything else ->
// 500 (logged).
func (s *Server) writeDomainError(w http.ResponseWriter, err error) {
	var authValErr *auth.ValidationError
	var auctionValErr *auction.ValidationError
	switch {
	case errors.As(err, &authValErr):
		writeError(w, http.StatusBadRequest, "validation_error", authValErr.Error())
	case errors.As(err, &auctionValErr):
		writeError(w, http.StatusBadRequest, "validation_error", auctionValErr.Error())
	case errors.Is(err, auction.ErrOwnAuction):
		writeError(w, http.StatusForbidden, "own_auction", "sellers cannot bid on their own auction")
	case errors.Is(err, auction.ErrNotActive):
		writeError(w, http.StatusConflict, "auction_not_active", "this auction is no longer active")
	case errors.Is(err, auction.ErrClosed):
		writeError(w, http.StatusConflict, "auction_closed", "this auction has ended")
	case errors.Is(err, auction.ErrBidTooLow):
		// Repositories wrap the sentinel with the concrete numbers (minimum
		// acceptable, current price, increment), so err.Error() is actionable.
		writeError(w, http.StatusConflict, "bid_too_low", err.Error())
	case errors.Is(err, auction.ErrTransactionCompleted):
		writeError(w, http.StatusConflict, "transaction_completed", "this transaction is already paid")
	case errors.Is(err, auction.ErrTransactionExpired):
		writeError(w, http.StatusConflict, "transaction_expired", "the payment window for this transaction has closed")
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
