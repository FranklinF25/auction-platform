package httpapi

// M4 checkout endpoints: POST /api/transactions/{id}/pay (simulated payment,
// decline/retry), GET /api/users/me/sales (seller aggregates) and the
// purchases item's embedded transaction. The landscape reuses the M3 fixture:
// its CloseDue (the honest fake) already creates the pending transactions,
// exactly like the production closing sweep.

import (
	"net/http"
	"testing"
	"time"

	"github.com/FranklinF25/auction-platform/apps/api/internal/auction"
)

// txSummary mirrors the purchase item's embedded transaction object.
type txSummary struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	AmountCents int64      `json:"amount_cents"`
	ExpiresAt   time.Time  `json:"expires_at"`
	PaidAt      *time.Time `json:"paid_at"`
}

type purchaseItemResp struct {
	auctionResp
	Transaction txSummary `json:"transaction"`
}

type purchasesResp struct {
	Items    []purchaseItemResp `json:"items"`
	Page     int                `json:"page"`
	PageSize int                `json:"page_size"`
	Total    int                `json:"total"`
}

type payResp struct {
	TransactionID string     `json:"transaction_id"`
	Status        string     `json:"status"`
	PaidAt        *time.Time `json:"paid_at"`
}

type salesResp struct {
	CompletedSales int   `json:"completed_sales"`
	PendingSales   int   `json:"pending_sales"`
	RevenueCents   int64 `json:"revenue_cents"`
}

// adaTransaction fetches Ada's single purchase transaction from the
// purchases dashboard (itself part of the M4 contract).
func adaTransaction(t *testing.T, f *m3Fixture) txSummary {
	t.Helper()
	var pr purchasesResp
	resp, _ := doJSON(t, f.ada, http.MethodGet, f.ts.URL+"/api/users/me/purchases", nil, &pr)
	if resp.StatusCode != http.StatusOK || pr.Total != 1 {
		t.Fatalf("purchases: status %d total %d, want 200/1", resp.StatusCode, pr.Total)
	}
	return pr.Items[0].Transaction
}

func payURL(f *m3Fixture, txID string) string { return f.ts.URL + "/api/transactions/" + txID + "/pay" }

func TestPayTransactionEndpoint(t *testing.T) {
	f := newM3Fixture(t)
	tx := adaTransaction(t, f)

	if tx.ID == "" {
		t.Fatal("purchases item carries no transaction id")
	}
	if tx.Status != "pending" {
		t.Fatalf("fresh close: transaction status = %q, want pending", tx.Status)
	}
	if tx.AmountCents != 1500 {
		t.Fatalf("transaction amount = %d, want the final price 1500", tx.AmountCents)
	}
	if tx.PaidAt != nil {
		t.Fatalf("pending transaction paid_at = %v, want nil", *tx.PaidAt)
	}
	wantExpires := f.clock.Now().Add(auction.TransactionExpiryDuration)
	if !tx.ExpiresAt.Equal(wantExpires) {
		t.Errorf("transaction expires_at = %v, want close time + 48h (%v)", tx.ExpiresAt, wantExpires)
	}

	t.Run("happy path completes and stamps paid_at", func(t *testing.T) {
		var got payResp
		resp, _ := doJSON(t, f.ada, http.MethodPost, payURL(f, tx.ID),
			map[string]string{"card_number": "4111 1111 1111 1111"}, &got)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if got.TransactionID != tx.ID || got.Status != "completed" {
			t.Errorf("body = %+v, want {transaction_id: %s, status: completed}", got, tx.ID)
		}
		if got.PaidAt == nil {
			t.Error("paid_at = nil on a completed payment, want RFC3339")
		}
	})

	t.Run("paying again is 409 transaction_completed", func(t *testing.T) {
		var e errResp
		resp, _ := doJSON(t, f.ada, http.MethodPost, payURL(f, tx.ID),
			map[string]string{"card_number": "4111111111111111"}, &e)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409", resp.StatusCode)
		}
		if e.Error.Code != "transaction_completed" {
			t.Errorf("code = %q, want transaction_completed", e.Error.Code)
		}
	})

	t.Run("another user's transaction is 404, unpaid afterwards", func(t *testing.T) {
		fresh := newM3Fixture(t)
		freshTx := adaTransaction(t, fresh)
		var e errResp
		resp, _ := doJSON(t, fresh.grace, http.MethodPost, payURL(fresh, freshTx.ID),
			map[string]string{"card_number": "4111111111111111"}, &e)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
		if e.Error.Code != "not_found" {
			t.Errorf("code = %q, want not_found", e.Error.Code)
		}
		if got := adaTransaction(t, fresh); got.Status != "pending" {
			t.Errorf("foreign attempt paid the transaction: status = %q", got.Status)
		}
	})
}

func TestPayTransactionDeclineAndRetry(t *testing.T) {
	f := newM3Fixture(t)
	tx := adaTransaction(t, f)

	// The demo decline card: a 200 outcome with status failed, paid_at null.
	var declined payResp
	resp, raw := doJSON(t, f.ada, http.MethodPost, payURL(f, tx.ID),
		map[string]string{"card_number": "4000000000000002"}, &declined)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("decline status = %d, want 200 (body: %s)", resp.StatusCode, raw)
	}
	if declined.Status != "failed" || declined.PaidAt != nil {
		t.Fatalf("decline body = %+v, want status failed and paid_at null", declined)
	}

	// The decline is persisted and visible on the purchases dashboard.
	if got := adaTransaction(t, f); got.Status != "failed" {
		t.Fatalf("stored status after decline = %q, want failed", got.Status)
	}

	// Retry with a good card completes the same transaction.
	var retried payResp
	resp, _ = doJSON(t, f.ada, http.MethodPost, payURL(f, tx.ID),
		map[string]string{"card_number": "5500005555555559"}, &retried)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retry status = %d, want 200", resp.StatusCode)
	}
	if retried.Status != "completed" || retried.PaidAt == nil {
		t.Fatalf("retry body = %+v, want completed with paid_at", retried)
	}
}

func TestPayTransactionErrorMapping(t *testing.T) {
	f := newM3Fixture(t)
	tx := adaTransaction(t, f)

	t.Run("malformed card is 400 validation_error", func(t *testing.T) {
		var e errResp
		resp, _ := doJSON(t, f.ada, http.MethodPost, payURL(f, tx.ID),
			map[string]string{"card_number": "123"}, &e)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
		if e.Error.Code != "validation_error" {
			t.Errorf("code = %q, want validation_error", e.Error.Code)
		}
	})

	t.Run("invalid json is 400 invalid_json", func(t *testing.T) {
		var e errResp
		resp, _ := doRaw(t, f.ada, http.MethodPost, payURL(f, tx.ID), []byte("{not json"), &e)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
		if e.Error.Code != "invalid_json" {
			t.Errorf("code = %q, want invalid_json", e.Error.Code)
		}
	})

	t.Run("malformed id is 400 invalid_id", func(t *testing.T) {
		resp, _ := doJSON(t, f.ada, http.MethodPost, f.ts.URL+"/api/transactions/not-a-uuid/pay",
			map[string]string{"card_number": "4111111111111111"}, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("unknown transaction is 404", func(t *testing.T) {
		resp, _ := doJSON(t, f.ada, http.MethodPost,
			f.ts.URL+"/api/transactions/00000000-0000-0000-0000-000000000000/pay",
			map[string]string{"card_number": "4111111111111111"}, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("expired window is 409 transaction_expired", func(t *testing.T) {
		fresh := newM3Fixture(t)
		freshTx := adaTransaction(t, fresh)
		fresh.clock.Set(freshTx.ExpiresAt.Add(time.Second)) // past the 48h window

		var e errResp
		resp, _ := doJSON(t, fresh.ada, http.MethodPost, payURL(fresh, freshTx.ID),
			map[string]string{"card_number": "4111111111111111"}, &e)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409", resp.StatusCode)
		}
		if e.Error.Code != "transaction_expired" {
			t.Errorf("code = %q, want transaction_expired", e.Error.Code)
		}
	})

	t.Run("unauthenticated is 401", func(t *testing.T) {
		resp, _ := doJSON(t, &http.Client{}, http.MethodPost, payURL(f, tx.ID),
			map[string]string{"card_number": "4111111111111111"}, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
	})
}

func TestSalesEndpoint(t *testing.T) {
	f := newM3Fixture(t)
	base := f.ts.URL

	t.Run("requires authentication", func(t *testing.T) {
		var e errResp
		resp, _ := doJSON(t, &http.Client{}, http.MethodGet, base+"/api/users/me/sales", nil, &e)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("pending sales before any payment", func(t *testing.T) {
		var got salesResp
		resp, _ := doJSON(t, f.seller, http.MethodGet, base+"/api/users/me/sales", nil, &got)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		// Two sold closes (Ada's lot and Grace's lot), both unpaid.
		if got.CompletedSales != 0 || got.PendingSales != 2 || got.RevenueCents != 0 {
			t.Errorf("sales = %+v, want 0 completed / 2 pending / 0 revenue", got)
		}
	})

	t.Run("completed sale adds revenue", func(t *testing.T) {
		tx := adaTransaction(t, f)
		if resp, _ := doJSON(t, f.ada, http.MethodPost, payURL(f, tx.ID),
			map[string]string{"card_number": "4111111111111111"}, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("pay: status %d, want 200", resp.StatusCode)
		}

		var got salesResp
		resp, _ := doJSON(t, f.seller, http.MethodGet, base+"/api/users/me/sales", nil, &got)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if got.CompletedSales != 1 || got.PendingSales != 1 || got.RevenueCents != 1500 {
			t.Errorf("sales after payment = %+v, want 1 completed / 1 pending / 1500 revenue", got)
		}
	})

	t.Run("a non-seller sees zeros", func(t *testing.T) {
		var got salesResp
		resp, _ := doJSON(t, f.ada, http.MethodGet, base+"/api/users/me/sales", nil, &got)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if got.CompletedSales != 0 || got.PendingSales != 0 || got.RevenueCents != 0 {
			t.Errorf("buyer sales = %+v, want all zeros", got)
		}
	})
}

func TestPurchasesCarryTransactionObject(t *testing.T) {
	f := newM3Fixture(t)
	tx := adaTransaction(t, f)

	// Pay, then reload the dashboard: status and paid_at must follow.
	if resp, _ := doJSON(t, f.ada, http.MethodPost, payURL(f, tx.ID),
		map[string]string{"card_number": "4111111111111111"}, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("pay: status %d, want 200", resp.StatusCode)
	}

	var pr purchasesResp
	resp, _ := doJSON(t, f.ada, http.MethodGet, f.ts.URL+"/api/users/me/purchases", nil, &pr)
	if resp.StatusCode != http.StatusOK || pr.Total != 1 || len(pr.Items) != 1 {
		t.Fatalf("purchases: status %d total %d items %d, want 200/1/1", resp.StatusCode, pr.Total, len(pr.Items))
	}
	item := pr.Items[0]
	if item.ID != f.sold.ID || item.CurrentPriceCents != 1500 {
		t.Fatalf("item = %s at %d, want the sold lot at 1500", item.ID, item.CurrentPriceCents)
	}
	if item.Transaction.ID != tx.ID {
		t.Errorf("transaction id = %q, want %q", item.Transaction.ID, tx.ID)
	}
	if item.Transaction.Status != "completed" {
		t.Errorf("transaction status = %q, want completed after payment", item.Transaction.Status)
	}
	if item.Transaction.AmountCents != 1500 {
		t.Errorf("transaction amount = %d, want 1500", item.Transaction.AmountCents)
	}
	if item.Transaction.PaidAt == nil {
		t.Error("transaction paid_at = nil after payment, want RFC3339")
	}
	// The page envelope stays the shared shape.
	if pr.Page != 1 || pr.PageSize != 20 {
		t.Errorf("page/page_size = %d/%d, want 1/20", pr.Page, pr.PageSize)
	}
}
