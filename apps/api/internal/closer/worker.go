// Package closer holds the background workers that drive the periodic sweeps:
// the M3 close worker (auctions whose end time has passed) and the M4 expiry
// worker (checkout transactions whose 48-hour payment window has closed). On
// a fixed tick each worker asks the auction service for one pass.
// Hexagonal rule: concurrency lives in this adapter — the domain stays pure
// and synchronous; this package only schedules calls.
package closer

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultInterval is the close-sweep tick used when no interval is given
// (production wiring): fast enough that auctions close within a second of
// their end time, cheap enough for a single-instance demo deployment.
const DefaultInterval = time.Second

// DueCloser is the slice of the auction service the worker needs: close every
// auction that is due. Declared locally so the worker depends on a port, not
// on the concrete service; *auction.Service satisfies it.
type DueCloser interface {
	CloseDue(ctx context.Context) error
}

// Worker periodically runs CloseDue. A pass failure is logged and never stops
// the worker — the next tick tries again (idempotent: already-closed auctions
// are skipped by the repository).
type Worker struct {
	svc      DueCloser
	logger   *slog.Logger
	interval time.Duration
}

// NewWorker builds a worker driving svc every interval. A non-positive
// interval falls back to DefaultInterval; a nil logger falls back to the
// default one.
func NewWorker(svc DueCloser, logger *slog.Logger, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = DefaultInterval
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{svc: svc, logger: logger, interval: interval}
}

// Run ticks until ctx is cancelled, then drains the in-flight pass (see
// runLoop).
func (w *Worker) Run(ctx context.Context) {
	runLoop(ctx, w.logger, w.interval, "close pass", w.svc.CloseDue)
}

// runLoop is the shared tick engine behind the package's workers. Each tick
// starts one pass of fn in its own goroutine unless the previous pass is
// still running — a slow pass makes the loop skip ticks, never overlap them
// (the passes are safe to call concurrently, but overlap would only add lock
// contention for nothing). It returns once ctx is cancelled AND the in-flight
// pass, if any, has finished: a started pass always runs to completion on a
// context detached from cancellation, so its effects still flush during
// shutdown. label names the pass in the skip/error log lines.
func runLoop(ctx context.Context, logger *slog.Logger, interval time.Duration, label string, fn func(context.Context) error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var inFlight atomic.Bool
	var wg sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			// Drain the in-flight pass before returning (bounded by the pass
			// itself, which ignores cancellation once started).
			wg.Wait()
			return
		case <-ticker.C:
			if !inFlight.CompareAndSwap(false, true) {
				logger.Debug(label + " still running; skipping tick")
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer inFlight.Store(false)
				// Detached from ctx: a started pass completes even while the
				// worker is shutting down, so its effects reach the adapters.
				if err := fn(context.WithoutCancel(ctx)); err != nil {
					logger.Error(label+" failed", "err", err)
				}
			}()
		}
	}
}
