package auction

import "context"

// Paging defaults and limits, applied by the service before any repository
// call so every adapter sees normalized values.
const (
	DefaultPage     = 1
	DefaultPageSize = 20
	MaxPageSize     = 50
)

// Page is one page of results: the items plus the normalized paging metadata
// and the total count matching the query.
type Page[T any] struct {
	Items    []T
	Page     int
	PageSize int
	Total    int
}

// Service holds the M1 use cases directly on the domain and its ports — no
// extra usecase layer. Bidding (M2) will live here too.
type Service struct {
	repo  Repository
	clock Clock
}

// NewService wires the repository and clock into the service.
func NewService(repo Repository, clock Clock) *Service {
	return &Service{repo: repo, clock: clock}
}

// CreateAuction validates and persists a new active auction for sellerID.
// Validation errors are *ValidationError; everything else comes from the
// repository.
func (s *Service) CreateAuction(ctx context.Context, sellerID string, in CreateInput) (*Auction, error) {
	a, err := NewAuction(sellerID, in, s.clock.Now())
	if err != nil {
		return nil, err
	}
	if err := s.repo.Create(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// GetAuction loads one aggregate (including bid history) by id.
func (s *Service) GetAuction(ctx context.Context, id string) (*Auction, error) {
	return s.repo.ByID(ctx, id)
}

// ListAuctions returns one page of summaries matching the filter, with
// normalized paging (defaults 1/20, page_size capped at 50).
func (s *Service) ListAuctions(ctx context.Context, f ListFilter) (Page[ListItem], error) {
	f.Page, f.PageSize = normalizePaging(f.Page, f.PageSize)
	items, total, err := s.repo.List(ctx, f)
	if err != nil {
		return Page[ListItem]{}, err
	}
	return Page[ListItem]{Items: items, Page: f.Page, PageSize: f.PageSize, Total: total}, nil
}

// ListBids returns one page of the auction's bids, newest first.
func (s *Service) ListBids(ctx context.Context, auctionID string, page, pageSize int) (Page[Bid], error) {
	page, pageSize = normalizePaging(page, pageSize)
	bids, total, err := s.repo.ListBids(ctx, auctionID, page, pageSize)
	if err != nil {
		return Page[Bid]{}, err
	}
	return Page[Bid]{Items: bids, Page: page, PageSize: pageSize, Total: total}, nil
}

func normalizePaging(page, pageSize int) (int, int) {
	if page < DefaultPage {
		page = DefaultPage
	}
	if pageSize < 1 {
		pageSize = DefaultPageSize
	}
	if pageSize > MaxPageSize {
		pageSize = MaxPageSize
	}
	return page, pageSize
}
