package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
)

// AuctionRepository implements auction.Repository on PostgreSQL with
// hand-written pgx queries.
type AuctionRepository struct {
	pool *pgxpool.Pool
}

// NewAuctionRepository returns an AuctionRepository backed by pool.
func NewAuctionRepository(pool *pgxpool.Pool) *AuctionRepository {
	return &AuctionRepository{pool: pool}
}

// Create inserts an auction and fills in the database-generated ID and
// created_at.
func (r *AuctionRepository) Create(ctx context.Context, a *auction.Auction) error {
	return r.pool.QueryRow(ctx, `
		INSERT INTO auctions (seller_id, title, description, starting_price_cents,
		                      min_increment_cents, reserve_price_cents, status, ends_at)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at`,
		a.SellerID, a.Title, a.Description, a.StartingPriceCents,
		a.MinIncrementCents, a.ReservePriceCents, string(a.Status), a.EndsAt,
	).Scan(&a.ID, &a.CreatedAt)
}

// ByID loads the aggregate including its bid history, oldest first.
func (r *AuctionRepository) ByID(ctx context.Context, id string) (*auction.Auction, error) {
	a := &auction.Auction{}
	var status string
	err := r.pool.QueryRow(ctx, `
		SELECT id, seller_id, title, description, starting_price_cents,
		       min_increment_cents, reserve_price_cents, status, ends_at,
		       created_at, closed_at
		FROM auctions
		WHERE id = $1::uuid`, id,
	).Scan(&a.ID, &a.SellerID, &a.Title, &a.Description, &a.StartingPriceCents,
		&a.MinIncrementCents, &a.ReservePriceCents, &status, &a.EndsAt,
		&a.CreatedAt, &a.ClosedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, auction.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.Status = auction.Status(status)

	bids, err := r.loadBids(ctx, id)
	if err != nil {
		return nil, err
	}
	a.LoadBids(bids)
	return a, nil
}

// loadBids returns one auction's bids oldest-first (aggregate order).
func (r *AuctionRepository) loadBids(ctx context.Context, auctionID string) ([]auction.Bid, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, auction_id, bidder_id, amount_cents, created_at
		FROM bids
		WHERE auction_id = $1::uuid
		ORDER BY created_at ASC`, auctionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bids := []auction.Bid{}
	for rows.Next() {
		var b auction.Bid
		if err := rows.Scan(&b.ID, &b.AuctionID, &b.BidderID, &b.AmountCents, &b.CreatedAt); err != nil {
			return nil, err
		}
		bids = append(bids, b)
	}
	return bids, rows.Err()
}

// listWhere is the shared WHERE fragment for the list query. User input for
// the ILIKE filter is escaped by escapeLike so it matches literally.
const listWhere = `($1::text IS NULL OR status = $1::text)
	AND ($2::text IS NULL OR title ILIKE '%' || $2::text || '%')`

// List returns one page of auction summaries plus the total count. Derived
// values (current price, reserve met, bid count) are computed in SQL so the
// list endpoint does not load every aggregate's bids.
func (r *AuctionRepository) List(ctx context.Context, f auction.ListFilter) ([]auction.ListItem, int, error) {
	var status any // nil means no filter
	if f.Status != "" {
		status = string(f.Status)
	}
	var query any
	if f.Query != "" {
		query = escapeLike(f.Query)
	}

	var total int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM auctions WHERE `+listWhere, status, query,
	).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	offset := (f.Page - 1) * f.PageSize
	rows, err := r.pool.Query(ctx, `
		SELECT a.id, a.seller_id, a.title, a.description, a.starting_price_cents,
		       a.min_increment_cents, a.status, a.ends_at, a.created_at,
		       COALESCE(b.max_amount, a.starting_price_cents) AS current_price_cents,
		       (a.reserve_price_cents IS NULL
		         OR COALESCE(b.max_amount, a.starting_price_cents) >= a.reserve_price_cents) AS reserve_met,
		       COALESCE(b.bid_count, 0) AS bid_count
		FROM auctions a
		LEFT JOIN (
			SELECT auction_id, MAX(amount_cents) AS max_amount, COUNT(*) AS bid_count
			FROM bids
			GROUP BY auction_id
		) b ON b.auction_id = a.id
		WHERE `+listWhere+`
		ORDER BY a.created_at DESC, a.id
		LIMIT $3 OFFSET $4`, status, query, f.PageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := []auction.ListItem{}
	for rows.Next() {
		var it auction.ListItem
		var st string
		if err := rows.Scan(&it.ID, &it.SellerID, &it.Title, &it.Description,
			&it.StartingPriceCents, &it.MinIncrementCents, &st, &it.EndsAt, &it.CreatedAt,
			&it.CurrentPriceCents, &it.ReserveMet, &it.BidCount); err != nil {
			return nil, 0, err
		}
		it.Status = auction.Status(st)
		items = append(items, it)
	}
	return items, total, rows.Err()
}

// ListBids returns one page of bids, newest first, with bidder names joined
// from users.
func (r *AuctionRepository) ListBids(ctx context.Context, auctionID string, page, pageSize int) ([]auction.Bid, int, error) {
	var total int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM bids WHERE auction_id = $1::uuid`, auctionID,
	).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * pageSize
	rows, err := r.pool.Query(ctx, `
		SELECT b.id, u.name, b.amount_cents, b.created_at
		FROM bids b
		JOIN users u ON u.id = b.bidder_id
		WHERE b.auction_id = $1::uuid
		ORDER BY b.created_at DESC
		LIMIT $2 OFFSET $3`, auctionID, pageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	bids := []auction.Bid{}
	for rows.Next() {
		var b auction.Bid
		if err := rows.Scan(&b.ID, &b.BidderName, &b.AmountCents, &b.CreatedAt); err != nil {
			return nil, 0, err
		}
		bids = append(bids, b)
	}
	return bids, total, rows.Err()
}

// escapeLike neutralizes LIKE wildcards in user input so q matches literally.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	return strings.ReplaceAll(s, `_`, `\_`)
}
