package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
)

// querier is the slice of pgx pool/tx methods the queries need, so the same
// helpers run both against the pool and inside PlaceBid's transaction.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

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
	a, err := scanAuction(r.pool.QueryRow(ctx, auctionSelect+`
WHERE id = $1::uuid`, id))
	if err != nil {
		return nil, err
	}
	bids, err := listBids(ctx, r.pool, id)
	if err != nil {
		return nil, err
	}
	a.LoadBids(bids)
	return a, nil
}

// auctionSelect is the aggregate's column list; FOR UPDATE is appended by the
// locked variant used on the bid hot path.
const auctionSelect = `SELECT id, seller_id, title, description, starting_price_cents,
       min_increment_cents, reserve_price_cents, status, ends_at,
       created_at, closed_at
FROM auctions`

// scanAuction materializes one auction row, mapping pgx's ErrNoRows onto the
// domain's ErrNotFound.
func scanAuction(row pgx.Row) (*auction.Auction, error) {
	a := &auction.Auction{}
	var status string
	err := row.Scan(&a.ID, &a.SellerID, &a.Title, &a.Description, &a.StartingPriceCents,
		&a.MinIncrementCents, &a.ReservePriceCents, &status, &a.EndsAt,
		&a.CreatedAt, &a.ClosedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, auction.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.Status = auction.Status(status)
	return a, nil
}

// listBids returns one auction's bids oldest-first (aggregate order).
func listBids(ctx context.Context, q querier, auctionID string) ([]auction.Bid, error) {
	rows, err := q.Query(ctx, `
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

// PlaceBid implements the bid hot path on PostgreSQL.
//
// Design note (documented choice, sanctioned by the PRD's "commands over
// HTTP" model): the transaction is owned by this adapter because only it can
// BEGIN/COMMIT. All rules stay in the domain — the single domain call is the
// pure Auction.PlaceBid — while this method orchestrates the IO around it:
// lock the auction row with SELECT ... FOR UPDATE (the concurrency authority:
// two bids on the same auction serialize here, first one wins), load the
// aggregate, let the domain accept or reject, then insert the bid and update
// ends_at in the same transaction. This keeps lock→load→validate→write→commit
// atomic with the least ceremony: no unit-of-work abstraction is threaded
// through the domain port, and the service publishes events only after this
// method returns, i.e. strictly after COMMIT.
func (r *AuctionRepository) PlaceBid(ctx context.Context, auctionID, bidderID string, amountCents int64, now time.Time) (*auction.PlacedBid, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) // no-op after commit

	// Take the per-auction row lock first; every competing bid waits here.
	a, err := scanAuction(tx.QueryRow(ctx, auctionSelect+`
WHERE id = $1::uuid
FOR UPDATE`, auctionID))
	if err != nil {
		return nil, err
	}
	bids, err := listBids(ctx, tx, auctionID)
	if err != nil {
		return nil, err
	}
	a.LoadBids(bids)

	prevEndsAt := a.EndsAt
	bid, err := a.PlaceBid(bidderID, amountCents, now) // THE rules: pure, no IO
	if err != nil {
		if errors.Is(err, auction.ErrBidTooLow) {
			min := a.CurrentPrice() + a.MinIncrementCents
			err = fmt.Errorf(
				"%w: bid must be at least %d cents (current price %d cents plus minimum increment %d cents)",
				err, min, a.CurrentPrice(), a.MinIncrementCents)
		}
		return nil, err
	}

	// Display name for the event payload, read inside the same transaction.
	if err := tx.QueryRow(ctx, `SELECT name FROM users WHERE id = $1::uuid`, bidderID).
		Scan(&bid.BidderName); err != nil {
		return nil, fmt.Errorf("load bidder: %w", err)
	}

	if err := tx.QueryRow(ctx, `
		INSERT INTO bids (auction_id, bidder_id, amount_cents, created_at)
		VALUES ($1::uuid, $2::uuid, $3, $4)
		RETURNING id`, auctionID, bidderID, amountCents, now,
	).Scan(&bid.ID); err != nil {
		return nil, err
	}

	extended := a.EndsAt.After(prevEndsAt)
	if extended {
		// Anti-sniping soft close moved ends_at; persist the new deadline.
		if _, err := tx.Exec(ctx, `
			UPDATE auctions SET ends_at = $2 WHERE id = $1::uuid`, auctionID, a.EndsAt); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &auction.PlacedBid{Bid: bid, Auction: a, Extended: extended}, nil
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
