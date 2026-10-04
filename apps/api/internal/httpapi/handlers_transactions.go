package httpapi

// The M4 checkout over REST: POST /api/transactions/{id}/pay (simulated
// payment on the winner's own transaction) and GET /api/users/me/sales (the
// seller's sales aggregates). No WebSocket events: the PRD's payment flow has
// no realtime requirement, so a payment only answers its requester.

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
)

type payTransactionRequest struct {
	CardNumber string `json:"card_number"`
}

// payTransactionResponse is the pinned 200 body for a payment attempt —
// successful or declined: a decline is an outcome (status "failed",
// paid_at null), not an error.
type payTransactionResponse struct {
	TransactionID string     `json:"transaction_id"`
	Status        string     `json:"status"`
	PaidAt        *time.Time `json:"paid_at"`
}

// handlePayTransaction simulates a payment by the authenticated user. Error
// mapping per the M4 contract: 401 unauthenticated (requireAuth), 400
// invalid id / invalid_json / validation_error (bad card), 404 not_found
// (missing transaction OR someone else's — the service leaks no existence),
// 409 transaction_completed, 409 transaction_expired.
func (s *Server) handlePayTransaction(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())

	id := chi.URLParam(r, "id")
	if !isUUID(id) {
		writeError(w, http.StatusBadRequest, "invalid_id", "transaction id must be a UUID")
		return
	}

	var req payTransactionRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	t, err := s.auctions.PayTransaction(r.Context(), user.ID, id, req.CardNumber)
	if err != nil {
		// Missing and foreign transactions are the same answer from the
		// service (ErrNotFound); phrase it for this endpoint.
		if errors.Is(err, auction.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "transaction not found")
			return
		}
		s.writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, payTransactionResponse{
		TransactionID: t.ID,
		Status:        string(t.Status),
		PaidAt:        t.PaidAt,
	})
}

// salesResponse is the pinned GET /api/users/me/sales body: the seller's
// checkout aggregates.
type salesResponse struct {
	CompletedSales int   `json:"completed_sales"`
	PendingSales   int   `json:"pending_sales"`
	RevenueCents   int64 `json:"revenue_cents"`
}

// handleMySales serves the seller dashboard's sales summary: how many of the
// seller's sold auctions have been paid (completed), how many still await
// payment (pending), and the total revenue collected.
func (s *Server) handleMySales(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	completed, pending, revenue, err := s.auctions.SellerSales(r.Context(), user.ID)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, salesResponse{
		CompletedSales: completed,
		PendingSales:   pending,
		RevenueCents:   revenue,
	})
}
