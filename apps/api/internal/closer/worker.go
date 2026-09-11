// Package closer holds the background worker that drives the M3 close sweep:
// on a fixed tick it asks the auction service to close every auction whose
// end time has passed. Hexagonal rule: concurrency lives in this adapter —
// the domain stays pure and synchronous; this package only schedules calls.
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

// Run ticks until ctx is cancelled. Each tick starts one close pass in its own
// goroutine unless the previous pass is still running — a slow pass makes the
// worker skip ticks, never overlap them (CloseDue is safe to call
// concurrently, but overlap would only add lock contention for nothing).
// Run returns once ctx is cancelled AND the in-flight pass, if any, has
// finished: a started pass always runs to completion on a context detached
// from cancellation, so its close events still flush during shutdown.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
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
				w.logger.Debug("close pass still running; skipping tick")
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer inFlight.Store(false)
				// Detached from ctx: a started pass completes even while the
				// worker is shutting down, so its events reach the hub.
				if err := w.svc.CloseDue(context.WithoutCancel(ctx)); err != nil {
					w.logger.Error("close pass failed", "err", err)
				}
			}()
		}
	}
}
