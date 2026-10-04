// The M4 expiry worker: on a fixed tick it asks the auction service to move
// every pending/failed checkout transaction whose payment window has passed to
// expired. Same driving-adapter shape as the close worker — it only schedules
// calls; the domain stays pure and synchronous.

package closer

import (
	"context"
	"log/slog"
	"time"
)

// DefaultExpiryInterval is the expiry-sweep tick used when no interval is
// given (production wiring). Thirty seconds is plenty: the payment window is
// 48 hours, so at worst a transaction lingers that long past its deadline.
const DefaultExpiryInterval = 30 * time.Second

// DueExpirer is the slice of the auction service the expiry worker needs:
// expire due transactions. Declared locally so the worker depends on a port,
// not on the concrete service; *auction.Service satisfies it.
type DueExpirer interface {
	ExpireDueTransactions(ctx context.Context) (int, error)
}

// ExpiryWorker periodically runs ExpireDueTransactions. A pass failure is
// logged and never stops the worker — the next tick tries again (idempotent:
// already-expired transactions no longer match the sweep's UPDATE).
type ExpiryWorker struct {
	svc      DueExpirer
	logger   *slog.Logger
	interval time.Duration
}

// NewExpiryWorker builds a worker driving svc every interval. A non-positive
// interval falls back to DefaultExpiryInterval; a nil logger falls back to
// the default one.
func NewExpiryWorker(svc DueExpirer, logger *slog.Logger, interval time.Duration) *ExpiryWorker {
	if interval <= 0 {
		interval = DefaultExpiryInterval
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ExpiryWorker{svc: svc, logger: logger, interval: interval}
}

// Run ticks until ctx is cancelled, then drains the in-flight pass (see
// runLoop).
func (w *ExpiryWorker) Run(ctx context.Context) {
	runLoop(ctx, w.logger, w.interval, "expiry pass", func(ctx context.Context) error {
		_, err := w.svc.ExpireDueTransactions(ctx)
		return err
	})
}
