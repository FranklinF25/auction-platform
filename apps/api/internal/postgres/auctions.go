package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewAuctionRepository returns an AuctionRepository backed by pool. The
// logger reports per-auction failures inside CloseDue (which continues past
// them); a nil logger falls back to the default one.
func NewAuctionRepository(pool *pgxpool.Pool, logger *slog.Logger) *AuctionRepository {
	if logger == nil {
		logger = slog.Default()
	}
	return &AuctionRepository{pool: pool, logger: logger}
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

// listBids returns one auction's bids oldest-first (aggregate order), with
// bidder display names joined from users — the aggregate's bid history is the
// single source for winner derivation, so names travel with the bids.
func listBids(ctx context.Context, q querier, auctionID string) ([]auction.Bid, error) {
	rows, err := q.Query(ctx, `
		SELECT b.id, b.auction_id, b.bidder_id, b.amount_cents, b.created_at, u.name
		FROM bids b
		JOIN users u ON u.id = b.bidder_id
		WHERE b.auction_id = $1::uuid
		ORDER BY b.created_at ASC`, auctionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bids := []auction.Bid{}
	for rows.Next() {
		var b auction.Bid
		if err := rows.Scan(&b.ID, &b.AuctionID, &b.BidderID, &b.AmountCents, &b.CreatedAt, &b.BidderName); err != nil {
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
// the ILIKE filter is escaped by escapeLike so it matches literally. A NULL
// seller_id parameter means "any seller" (the public list); the seller
// dashboard passes the seller's uuid to scope the query.
const listWhere = `($1::text IS NULL OR status = $1::text)
	AND ($2::text IS NULL OR title ILIKE '%' || $2::text || '%')
	AND ($3::uuid IS NULL OR seller_id = $3::uuid)`

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
	var seller any
	if f.SellerID != "" {
		seller = f.SellerID
	}

	var total int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM auctions WHERE `+listWhere, status, query, seller,
	).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	offset := (f.Page - 1) * f.PageSize
	rows, err := r.pool.Query(ctx, `
		SELECT a.id, a.seller_id, a.title, a.description, a.starting_price_cents,
		       a.min_increment_cents, a.status, a.ends_at, a.created_at, a.closed_at,
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
		LIMIT $4 OFFSET $5`, status, query, seller, f.PageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := []auction.ListItem{}
	for rows.Next() {
		var it auction.ListItem
		var st string
		// closed_at is NULL until the closing sweep runs; nil scans to the
		// ListItem zero value ("not closed").
		var closedAt *time.Time
		if err := rows.Scan(&it.ID, &it.SellerID, &it.Title, &it.Description,
			&it.StartingPriceCents, &it.MinIncrementCents, &st, &it.EndsAt, &it.CreatedAt, &closedAt,
			&it.CurrentPriceCents, &it.ReserveMet, &it.BidCount); err != nil {
			return nil, 0, err
		}
		it.Status = auction.Status(st)
		if closedAt != nil {
			it.ClosedAt = *closedAt
		}
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

// CloseDue implements the M3 closing sweep on PostgreSQL. It follows the
// same tx-owned-by-adapter discipline as PlaceBid: every auction closes in
// its own transaction (BEGIN; SELECT ... FOR UPDATE; load aggregate; pure
// Auction.Close; persist status + closed_at; COMMIT), so one failing auction
// rolls back only itself — the failure is logged and the sweep continues. The
// initial id scan is a plain read; per-auction correctness is re-established
// under the row lock, where a soft-close extension that moved ends_at past
// now (or a status change) simply skips that auction for this pass.
func (r *AuctionRepository) CloseDue(ctx context.Context, now time.Time) ([]auction.ClosedAuction, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id FROM auctions
		WHERE status = 'active' AND ends_at <= $1
		ORDER BY ends_at, id`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	results := []auction.ClosedAuction{}
	for _, id := range ids {
		res, err := r.closeOne(ctx, id, now)
		if err != nil {
			// One bad auction must not roll back the others: log and continue.
			r.logger.Error("close auction failed", "auction_id", id, "err", err)
			continue
		}
		if res != nil {
			results = append(results, *res)
		}
	}
	return results, nil
}

// closeOne closes a single due auction inside its own transaction and returns
// the post-close result. A nil result with a nil error means the auction turned
// out not to be closable under the lock (no longer active, or ends_at moved
// past now via the soft close) and was skipped.
func (r *AuctionRepository) closeOne(ctx context.Context, id string, now time.Time) (*auction.ClosedAuction, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) // no-op after commit

	// Serialize with bids and any other closer under the per-auction row lock.
	a, err := scanAuction(tx.QueryRow(ctx, auctionSelect+`
WHERE id = $1::uuid
FOR UPDATE`, id))
	if err != nil {
		return nil, err
	}
	bids, err := listBids(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	a.LoadBids(bids)

	winner, won, err := a.Close(now) // THE rules: pure, no IO
	if err != nil {
		if errors.Is(err, auction.ErrNotClosable) || errors.Is(err, auction.ErrNotActive) {
			return nil, nil // state moved on since the id scan: skip, not fail
		}
		return nil, err
	}

	// Winner's display name for the close event payload, read inside the same
	// transaction so the lock covers the full close decision.
	if won {
		if err := tx.QueryRow(ctx, `SELECT name FROM users WHERE id = $1::uuid`, winner.BidderID).
			Scan(&winner.BidderName); err != nil {
			return nil, fmt.Errorf("load winner: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE auctions SET status = $2, closed_at = $3 WHERE id = $1::uuid`,
		id, string(a.Status), a.ClosedAt); err != nil {
		return nil, err
	}

	// M4 checkout: a sold close creates the pending payment in the same locked
	// transaction — amount is the final price at close, and the winner has 48
	// hours to pay. The UNIQUE index on auction_id backstops double-creation.
	var txn *auction.Transaction
	if won {
		txn = &auction.Transaction{
			AuctionID:   a.ID,
			WinnerID:    winner.BidderID,
			AmountCents: a.CurrentPrice(),
			Status:      auction.TransactionPending,
			CreatedAt:   now,
			ExpiresAt:   now.Add(auction.TransactionExpiryDuration),
		}
		if err := insertTransaction(ctx, tx, txn); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &auction.ClosedAuction{Auction: a, Winner: winner, Won: won, Transaction: txn}, nil
}

// wonTopBid is the shared CTE picking each auction's winning bid: the highest
// amount, ties broken by earliest placement, matching the domain's
// HighestBid semantics (bids load oldest-first, strictly-greater wins).
const wonTopBid = `SELECT DISTINCT ON (b.auction_id)
		b.auction_id, b.bidder_id, b.amount_cents
		FROM bids b
		ORDER BY b.auction_id, b.amount_cents DESC, b.created_at ASC, b.id ASC`

// ListWon returns one page of auctions the user has won: closed, sold
// (reserve met) and the user is the highest bidder, newest close first.
func (r *AuctionRepository) ListWon(ctx context.Context, userID string, page, pageSize int) ([]auction.ListItem, int, error) {
	// A won auction always has at least one bid, and every accepted bid is at
	// least starting price + increment, so the top bid is the current price.
	const itemSelect = `SELECT a.id, a.seller_id, a.title, a.description, a.starting_price_cents,
		       a.min_increment_cents, a.status, a.ends_at, a.created_at, a.closed_at,
		       top.amount_cents AS current_price_cents,
		       COALESCE(cnt.bid_count, 0) AS bid_count
		FROM auctions a
		JOIN (` + wonTopBid + `) top ON top.auction_id = a.id
		LEFT JOIN (
			SELECT auction_id, COUNT(*) AS bid_count FROM bids GROUP BY auction_id
		) cnt ON cnt.auction_id = a.id
		WHERE a.status = 'closed'
		  AND top.bidder_id = $1::uuid
		  AND (a.reserve_price_cents IS NULL OR top.amount_cents >= a.reserve_price_cents)
		ORDER BY a.closed_at DESC, a.id
		LIMIT $2 OFFSET $3`

	var total int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*)
		FROM auctions a
		JOIN (`+wonTopBid+`) top ON top.auction_id = a.id
		WHERE a.status = 'closed'
		  AND top.bidder_id = $1::uuid
		  AND (a.reserve_price_cents IS NULL OR top.amount_cents >= a.reserve_price_cents)`, userID,
	).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * pageSize
	rows, err := r.pool.Query(ctx, itemSelect, userID, pageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := []auction.ListItem{}
	for rows.Next() {
		var it auction.ListItem
		var st string
		var closedAt *time.Time // NULL never happens for a closed auction, but scan defensively
		if err := rows.Scan(&it.ID, &it.SellerID, &it.Title, &it.Description,
			&it.StartingPriceCents, &it.MinIncrementCents, &st, &it.EndsAt, &it.CreatedAt, &closedAt,
			&it.CurrentPriceCents, &it.BidCount); err != nil {
			return nil, 0, err
		}
		it.Status = auction.Status(st)
		it.ReserveMet = true // a won auction is sold by definition
		if closedAt != nil {
			it.ClosedAt = *closedAt
		}
		items = append(items, it)
	}
	return items, total, rows.Err()
}

// escapeLike neutralizes LIKE wildcards in user input so q matches literally.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	return strings.ReplaceAll(s, `_`, `\_`)
}

// SeedAuction inserts an auction together with its full historical state —
// status, ends_at, created_at, closed_at and the bid history with explicit
// timestamps — inside one transaction, and loads the stored bids (IDs
// assigned) into the aggregate. It exists for the demo seed, which must
// construct finished auctions (closed, sold, already paid) that the
// create/place-bid/close lifecycle cannot express; every normal write still
// goes through Create and PlaceBid. The caller is responsible for passing a
// bid history that respects the domain rules (the seed package validates).
func (r *AuctionRepository) SeedAuction(ctx context.Context, a *auction.Auction, bids []auction.Bid) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // no-op after commit

	err = tx.QueryRow(ctx, `
		INSERT INTO auctions (seller_id, title, description, starting_price_cents,
		                      min_increment_cents, reserve_price_cents, status, ends_at,
		                      created_at, closed_at)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id`,
		a.SellerID, a.Title, a.Description, a.StartingPriceCents,
		a.MinIncrementCents, a.ReservePriceCents, string(a.Status), a.EndsAt,
		a.CreatedAt, a.ClosedAt,
	).Scan(&a.ID)
	if err != nil {
		return err
	}

	for i := range bids {
		bids[i].AuctionID = a.ID
		if err := tx.QueryRow(ctx, `
			INSERT INTO bids (auction_id, bidder_id, amount_cents, created_at)
			VALUES ($1::uuid, $2::uuid, $3, $4)
			RETURNING id`,
			bids[i].AuctionID, bids[i].BidderID, bids[i].AmountCents, bids[i].CreatedAt,
		).Scan(&bids[i].ID); err != nil {
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	a.LoadBids(bids)
	return nil
}
