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
// closed_at is emitted on every representation (detail and list items) as null
// while the auction is not closed and RFC3339 once it is, so clients render
// the actual close time instead of deriving it from ends_at.
type auctionResponse struct {
	ID                 string     `json:"id"`
	SellerID           string     `json:"seller_id"`
	Title              string     `json:"title"`
	Description        string     `json:"description"`
	StartingPriceCents int64      `json:"starting_price_cents"`
	MinIncrementCents  int64      `json:"min_increment_cents"`
	CurrentPriceCents  int64      `json:"current_price_cents"`
	ReserveMet         bool       `json:"reserve_met"`
	BidCount           int        `json:"bid_count"`
	Status             string     `json:"status"`
	EndsAt             time.Time  `json:"ends_at"`
	CreatedAt          time.Time  `json:"created_at"`
	ClosedAt           *time.Time `json:"closed_at"`
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
		ClosedAt:           a.ClosedAt,
	}
}

func newListItemResponse(it auction.ListItem) auctionResponse {
	resp := auctionResponse{
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
	// The ListItem zero value means "not closed"; on the wire that is null.
	if !it.ClosedAt.IsZero() {
		resp.ClosedAt = &it.ClosedAt
	}
	return resp
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
	filter, ok := s.parseListQuery(w, r.URL.Query())
	if !ok {
		return // error already written
	}

	result, err := s.auctions.ListAuctions(r.Context(), filter)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	writeListItemPage(w, result)
}

// handleListMyAuctions serves the seller dashboard: the same list envelope as
// GET /api/auctions, scoped to the authenticated seller's own auctions.
func (s *Server) handleListMyAuctions(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	filter, ok := s.parseListQuery(w, r.URL.Query())
	if !ok {
		return // error already written
	}
	filter.SellerID = user.ID

	result, err := s.auctions.ListAuctions(r.Context(), filter)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	writeListItemPage(w, result)
}

// handleListMyPurchases serves the buyer dashboard: auctions the
// authenticated user has won (closed, sold, highest bidder), newest close
// first, each joined with its checkout transaction (M4). Losing bids and
// unsold (reserve-not-met) auctions never appear.
func (s *Server) handleListMyPurchases(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	page, pageSize, ok := s.parsePaging(w, r.URL.Query())
	if !ok {
		return // error already written
	}

	result, err := s.auctions.ListPurchases(r.Context(), user.ID, page, pageSize)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	items := make([]purchaseItemResponse, 0, len(result.Items))
	for _, it := range result.Items {
		items = append(items, newPurchaseItemResponse(it))
	}
	writeJSON(w, http.StatusOK, pageResponse[purchaseItemResponse]{
		Items: items, Page: result.Page, PageSize: result.PageSize, Total: result.Total,
	})
}

// parseListQuery reads the shared q/status/page/page_size list parameters
// into a filter. ok=false means the error response has been written.
func (s *Server) parseListQuery(w http.ResponseWriter, q url.Values) (auction.ListFilter, bool) {
	filter := auction.ListFilter{Query: strings.TrimSpace(q.Get("q"))}
	if st := q.Get("status"); st != "" {
		switch auction.Status(st) {
		case auction.StatusActive, auction.StatusClosed, auction.StatusCancelled:
			filter.Status = auction.Status(st)
		default:
			writeError(w, http.StatusBadRequest, "invalid_query", "status must be active, closed or cancelled")
			return filter, false
		}
	}
	page, pageSize, ok := s.parsePaging(w, q)
	if !ok {
		return filter, false // error already written
	}
	filter.Page, filter.PageSize = page, pageSize
	return filter, true
}

// writeListItemPage renders the shared paged-list envelope.
func writeListItemPage(w http.ResponseWriter, result auction.Page[auction.ListItem]) {
	items := make([]auctionResponse, 0, len(result.Items))
	for _, it := range result.Items {
		items = append(items, newListItemResponse(it))
	}
	writeJSON(w, http.StatusOK, pageResponse[auctionResponse]{
		Items: items, Page: result.Page, PageSize: result.PageSize, Total: result.Total,
	})
}

// transactionResponse is the checkout view embedded in a purchase item:
// payment status, the amount owed and the remaining payment window. The
// auction id and winner id are deliberately absent — the enclosing item
// already identifies the lot, and the winner is the requesting user.
type transactionResponse struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	AmountCents int64      `json:"amount_cents"`
	ExpiresAt   time.Time  `json:"expires_at"`
	PaidAt      *time.Time `json:"paid_at"`
}

// purchaseItemResponse is the M4 purchases item: the existing list-item
// representation plus the checkout transaction. Additive only — every prior
// field keeps its name and shape.
type purchaseItemResponse struct {
	auctionResponse
	Transaction transactionResponse `json:"transaction"`
}

func newPurchaseItemResponse(it auction.PurchaseItem) purchaseItemResponse {
	return purchaseItemResponse{
		auctionResponse: newListItemResponse(it.ListItem),
		Transaction: transactionResponse{
			ID:          it.Transaction.ID,
			Status:      string(it.Transaction.Status),
			AmountCents: it.Transaction.AmountCents,
			ExpiresAt:   it.Transaction.ExpiresAt,
			PaidAt:      it.Transaction.PaidAt,
		},
	}
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

	// Winner view, derived from the already-loaded bids with the same domain
	// rules the close uses (HighestBid + ReserveMet): winner_name appears only
	// on a closed, sold auction; you_won is true only for the authenticated
	// winner. Never user ids, never the reserve value.
	detail := auctionDetailResponse{auctionResponse: newAuctionResponse(a)}
	if a.Status == auction.StatusClosed {
		if highest, ok := a.HighestBid(); ok && a.ReserveMet() {
			name := highest.BidderName
			detail.WinnerName = &name
			if user, authed := UserFrom(r.Context()); authed && user.ID == highest.BidderID {
				detail.YouWon = true
			}
		}
	}
	writeJSON(w, http.StatusOK, detail)
}

// auctionDetailResponse extends the public representation with the post-close
// winner view served by GET /api/auctions/{id}: winner_name is null unless the
// auction closed sold, and you_won tells the authenticated requester whether
// they are the winner (false for guests and everyone else).
type auctionDetailResponse struct {
	auctionResponse
	WinnerName *string `json:"winner_name"`
	YouWon     bool    `json:"you_won"`
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
