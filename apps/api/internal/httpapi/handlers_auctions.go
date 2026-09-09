package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
)

type createAuctionRequest struct {
	Title              string `json:"title"`
	Description        string `json:"description"`
	StartingPriceCents int64  `json:"starting_price_cents"`
	MinIncrementCents  int64  `json:"min_increment_cents"`
	ReservePriceCents  *int64 `json:"reserve_price_cents"`
	DurationMinutes    int    `json:"duration_minutes"`
}

// auctionResponse is the public representation of an auction. The reserve
// price value is intentionally absent: only the reserve-met boolean is public.
type auctionResponse struct {
	ID                 string    `json:"id"`
	SellerID           string    `json:"seller_id"`
	Title              string    `json:"title"`
	Description        string    `json:"description"`
	StartingPriceCents int64     `json:"starting_price_cents"`
	MinIncrementCents  int64     `json:"min_increment_cents"`
	CurrentPriceCents  int64     `json:"current_price_cents"`
	ReserveMet         bool      `json:"reserve_met"`
	BidCount           int       `json:"bid_count"`
	Status             string    `json:"status"`
	EndsAt             time.Time `json:"ends_at"`
	CreatedAt          time.Time `json:"created_at"`
}

func newAuctionResponse(a *auction.Auction) auctionResponse {
	return auctionResponse{
		ID:                 a.ID,
		SellerID:           a.SellerID,
		Title:              a.Title,
		Description:        a.Description,
		StartingPriceCents: a.StartingPriceCents,
		MinIncrementCents:  a.MinIncrementCents,
		CurrentPriceCents:  a.CurrentPrice(),
		ReserveMet:         a.ReserveMet(),
		BidCount:           a.BidCount(),
		Status:             string(a.Status),
		EndsAt:             a.EndsAt,
		CreatedAt:          a.CreatedAt,
	}
}

func newListItemResponse(it auction.ListItem) auctionResponse {
	return auctionResponse{
		ID:                 it.ID,
		SellerID:           it.SellerID,
		Title:              it.Title,
		Description:        it.Description,
		StartingPriceCents: it.StartingPriceCents,
		MinIncrementCents:  it.MinIncrementCents,
		CurrentPriceCents:  it.CurrentPriceCents,
		ReserveMet:         it.ReserveMet,
		BidCount:           it.BidCount,
		Status:             string(it.Status),
		EndsAt:             it.EndsAt,
		CreatedAt:          it.CreatedAt,
	}
}

// pageResponse is the shared envelope for paged collections.
type pageResponse[T any] struct {
	Items    []T `json:"items"`
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
	Total    int `json:"total"`
}

func (s *Server) handleCreateAuction(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	var req createAuctionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a, err := s.auctions.CreateAuction(r.Context(), user.ID, auction.CreateInput{
		Title:              req.Title,
		Description:        req.Description,
		StartingPriceCents: req.StartingPriceCents,
		MinIncrementCents:  req.MinIncrementCents,
		ReservePriceCents:  req.ReservePriceCents,
		DurationMinutes:    req.DurationMinutes,
	})
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newAuctionResponse(a))
}

func (s *Server) handleListAuctions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter := auction.ListFilter{Query: strings.TrimSpace(q.Get("q"))}
	if st := q.Get("status"); st != "" {
		switch auction.Status(st) {
		case auction.StatusActive, auction.StatusClosed, auction.StatusCancelled:
			filter.Status = auction.Status(st)
		default:
			writeError(w, http.StatusBadRequest, "invalid_query", "status must be active, closed or cancelled")
			return
		}
	}

	page, pageSize, ok := s.parsePaging(w, q)
	if !ok {
		return // error already written
	}
	filter.Page, filter.PageSize = page, pageSize

	result, err := s.auctions.ListAuctions(r.Context(), filter)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	items := make([]auctionResponse, 0, len(result.Items))
	for _, it := range result.Items {
		items = append(items, newListItemResponse(it))
	}
	writeJSON(w, http.StatusOK, pageResponse[auctionResponse]{
		Items: items, Page: result.Page, PageSize: result.PageSize, Total: result.Total,
	})
}

func (s *Server) handleGetAuction(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !isUUID(id) {
		writeError(w, http.StatusBadRequest, "invalid_id", "auction id must be a UUID")
		return
	}
	a, err := s.auctions.GetAuction(r.Context(), id)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newAuctionResponse(a))
}

type bidResponse struct {
	ID          string    `json:"id"`
	BidderName  string    `json:"bidder_name"`
	AmountCents int64     `json:"amount_cents"`
	CreatedAt   time.Time `json:"created_at"`
}

func (s *Server) handleListBids(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !isUUID(id) {
		writeError(w, http.StatusBadRequest, "invalid_id", "auction id must be a UUID")
		return
	}
	// Resolve the auction first so a missing id is 404, not an empty list.
	if _, err := s.auctions.GetAuction(r.Context(), id); err != nil {
		s.writeDomainError(w, err)
		return
	}
	page, pageSize, ok := s.parsePaging(w, r.URL.Query())
	if !ok {
		return
	}
	result, err := s.auctions.ListBids(r.Context(), id, page, pageSize)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	items := make([]bidResponse, 0, len(result.Items))
	for _, b := range result.Items {
		items = append(items, bidResponse{
			ID:          b.ID,
			BidderName:  b.BidderName,
			AmountCents: b.AmountCents,
			CreatedAt:   b.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, pageResponse[bidResponse]{
		Items: items, Page: result.Page, PageSize: result.PageSize, Total: result.Total,
	})
}

// parsePaging reads page/page_size with defaults 1/20. Values above the max
// are clamped by the service; non-numeric or non-positive values are 400.
func (s *Server) parsePaging(w http.ResponseWriter, q url.Values) (int, int, bool) {
	page, pageSize := auction.DefaultPage, auction.DefaultPageSize

	if v := q.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "invalid_query", "page must be a positive integer")
			return 0, 0, false
		}
		page = n
	}
	if v := q.Get("page_size"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "invalid_query", "page_size must be a positive integer")
			return 0, 0, false
		}
		pageSize = n
	}
	return page, pageSize, true
}

// isUUID checks the canonical 8-4-4-4-12 hex shape before hitting storage.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			isHex := c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
			if !isHex {
				return false
			}
		}
	}
	return true
}
