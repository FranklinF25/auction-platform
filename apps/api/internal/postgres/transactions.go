// M4 checkout on PostgreSQL: the transactions table (migration 0002) and the
// payment, expiry, purchase and sales read models. Same adapter-owned
// transaction discipline as PlaceBid/CloseDue: the row lock and the commit
// live here, the rules live in the pure Transaction.Pay.

package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
)

// transactionSelect is the transactions column list; FOR UPDATE is appended by
// the locked variant used on the payment path.
const transactionSelect = `SELECT id, auction_id, winner_id, amount_cents, status,
       created_at, expires_at, paid_at
FROM transactions`

// scanTransaction materializes one transaction row, mapping pgx's ErrNoRows
// onto the domain's ErrNotFound.
func scanTransaction(row pgx.Row) (*auction.Transaction, error) {
	t := &auction.Transaction{}
	var status string
	err := row.Scan(&t.ID, &t.AuctionID, &t.WinnerID, &t.AmountCents, &status,
		&t.CreatedAt, &t.ExpiresAt, &t.PaidAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, auction.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.Status = auction.Status(status)
	return t, nil
}

// insertTransaction inserts t through q (pool or an open transaction) and
// fills in the database-generated ID. The caller owns every stored value,
// timestamps included: the closing sweep and the demo seed both construct
// full domain objects before calling.
func insertTransaction(ctx context.Context, q querier, t *auction.Transaction) error {
	return q.QueryRow(ctx, `
		INSERT INTO transactions (auction_id, winner_id, amount_cents, status,
		                          created_at, expires_at, paid_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7)
		RETURNING id`,
		t.AuctionID, t.WinnerID, t.AmountCents, string(t.Status),
		t.CreatedAt, t.ExpiresAt, t.PaidAt,
	).Scan(&t.ID)
}

// CreateTransaction inserts a transaction; the UNIQUE index on auction_id is
// the authority guaranteeing at most one transaction per auction.
func (r *AuctionRepository) CreateTransaction(ctx context.Context, t *auction.Transaction) error {
	return insertTransaction(ctx, r.pool, t)
}

// TransactionByID loads one transaction, ErrNotFound when absent.
func (r *AuctionRepository) TransactionByID(ctx context.Context, id string) (*auction.Transaction, error) {
	return scanTransaction(r.pool.QueryRow(ctx, transactionSelect+`
WHERE id = $1::uuid`, id))
}

// PayTransaction executes one payment attempt in a single transaction with the
// transaction row locked: BEGIN; SELECT ... FOR UPDATE; pure Transaction.Pay;
// persist status + paid_at; COMMIT. A decline is not an error — the failed
// status is persisted (retryable while the window is open) and the failed
// transaction is returned. Rejections (validation, completed, expired) change
// nothing and surface the pure sentinel errors unchanged.
func (r *AuctionRepository) PayTransaction(ctx context.Context, id string, cardNumber string, now time.Time) (*auction.Transaction, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) // no-op after commit

	// Serialize with the expiry sweeper's UPDATE under the row lock.
	t, err := scanTransaction(tx.QueryRow(ctx, transactionSelect+`
WHERE id = $1::uuid
FOR UPDATE`, id))
	if err != nil {
		return nil, err
	}

	if err := t.Pay(cardNumber, now); err != nil { // THE rules: pure, no IO
		return nil, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE transactions SET status = $2, paid_at = $3 WHERE id = $1::uuid`,
		id, string(t.Status), t.PaidAt); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return t, nil
}

// ExpireDueTransactions moves every pending or failed transaction whose
// payment window has passed to expired in one UPDATE, returning the count.
func (r *AuctionRepository) ExpireDueTransactions(ctx context.Context, now time.Time) (int, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE transactions SET status = 'expired'
		WHERE status IN ('pending', 'failed') AND expires_at <= $1`, now)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// purchaseWhere is the shared WHERE fragment for the purchases read model:
// the ListWon sold-auction predicates (closed, user is the top bidder,
// reserve met) plus the join onto the user's own transaction.
const purchaseWhere = `a.status = 'closed'
  AND top.bidder_id = $1::uuid
  AND (a.reserve_price_cents IS NULL OR top.amount_cents >= a.reserve_price_cents)`

// ListMyTransactions returns one page of the user's purchases — won auctions
// joined with their transaction, newest close first — plus the total count.
func (r *AuctionRepository) ListMyTransactions(ctx context.Context, userID string, page, pageSize int) ([]auction.PurchaseItem, int, error) {
	var total int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*)
		FROM auctions a
		JOIN (`+wonTopBid+`) top ON top.auction_id = a.id
		JOIN transactions t ON t.auction_id = a.id AND t.winner_id = $1::uuid
		WHERE `+purchaseWhere, userID,
	).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * pageSize
	rows, err := r.pool.Query(ctx, `
		SELECT a.id, a.seller_id, a.title, a.description, a.starting_price_cents,
		       a.min_increment_cents, a.status, a.ends_at, a.created_at, a.closed_at,
		       top.amount_cents AS current_price_cents,
		       COALESCE(cnt.bid_count, 0) AS bid_count,
		       t.id, t.status, t.amount_cents, t.created_at, t.expires_at, t.paid_at
		FROM auctions a
		JOIN (`+wonTopBid+`) top ON top.auction_id = a.id
		LEFT JOIN (
			SELECT auction_id, COUNT(*) AS bid_count FROM bids GROUP BY auction_id
		) cnt ON cnt.auction_id = a.id
		JOIN transactions t ON t.auction_id = a.id AND t.winner_id = $1::uuid
		WHERE `+purchaseWhere+`
		ORDER BY a.closed_at DESC, a.id
		LIMIT $2 OFFSET $3`, userID, pageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := []auction.PurchaseItem{}
	for rows.Next() {
		var it auction.PurchaseItem
		var st, txStatus string
		var closedAt *time.Time // NULL never happens for a closed auction, but scan defensively
		if err := rows.Scan(&it.ID, &it.SellerID, &it.Title, &it.Description,
			&it.StartingPriceCents, &it.MinIncrementCents, &st, &it.EndsAt, &it.CreatedAt, &closedAt,
			&it.CurrentPriceCents, &it.BidCount,
			&it.Transaction.ID, &txStatus, &it.Transaction.AmountCents,
			&it.Transaction.CreatedAt, &it.Transaction.ExpiresAt, &it.Transaction.PaidAt); err != nil {
			return nil, 0, err
		}
		it.Status = auction.Status(st)
		it.Transaction.Status = auction.Status(txStatus)
		it.ReserveMet = true // a won auction is sold by definition
		if closedAt != nil {
			it.ClosedAt = *closedAt
		}
		items = append(items, it)
	}
	return items, total, rows.Err()
}

// SellerSales aggregates one seller's checkout state across their sold
// auctions: completed transactions, still-pending transactions, and the
// revenue from completed ones (failed/expired count towards neither).
func (r *AuctionRepository) SellerSales(ctx context.Context, sellerID string) (int, int, int64, error) {
	var completed, pending int
	var revenue int64
	err := r.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE t.status = 'completed'),
			COUNT(*) FILTER (WHERE t.status = 'pending'),
			COALESCE(SUM(t.amount_cents) FILTER (WHERE t.status = 'completed'), 0)
		FROM transactions t
		JOIN auctions a ON a.id = t.auction_id
		WHERE a.seller_id = $1::uuid`, sellerID,
	).Scan(&completed, &pending, &revenue)
	if err != nil {
		return 0, 0, 0, err
	}
	return completed, pending, revenue, nil
}
