package httpapi

// The bid hot path over REST: POST /api/auctions/{id}/bids. Commands travel
// over HTTP (one battle-tested validation path); the resulting state change is
// broadcast to the auction's WS room by the service's EventPublisher.

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
)

type placeBidRequest struct {
	AmountCents int64 `json:"amount_cents"`
}

// placeBidResponse is the pinned 201 body for an accepted bid. server_now
// lets the client compute its clock offset from the same emit instant the
// events carry.
type placeBidResponse struct {
	BidID             string    `json:"bid_id"`
	AuctionID         string    `json:"auction_id"`
	AmountCents       int64     `json:"amount_cents"`
	CurrentPriceCents int64     `json:"current_price_cents"`
	EndsAt            time.Time `json:"ends_at"`
	ServerNow         time.Time `json:"server_now"`
}

// handlePlaceBid places a bid for the authenticated user. Error mapping per
// the M2 contract: 401 unauthenticated (requireAuth), 400 non-positive
// amount, 403 own auction, 409 not active / ended / too low, 404 unknown.
func (s *Server) handlePlaceBid(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())

	id := chi.URLParam(r, "id")
	if !isUUID(id) {
		writeError(w, http.StatusBadRequest, "invalid_id", "auction id must be a UUID")
		return
	}

	var req placeBidRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	res, err := s.auctions.PlaceBid(r.Context(), auction.PlaceBidInput{
		AuctionID:   id,
		BidderID:    user.ID,
		AmountCents: req.AmountCents,
	})
	if err != nil {
		s.writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, placeBidResponse{
		BidID:             res.Bid.ID,
		AuctionID:         res.Bid.AuctionID,
		AmountCents:       res.Bid.AmountCents,
		CurrentPriceCents: res.Auction.CurrentPrice(),
		EndsAt:            res.Auction.EndsAt,
		ServerNow:         res.ServerNow,
	})
}
