package closer_test

// Worker behavior: every tick drives one CloseDue pass, a slow pass makes
// ticks skip (never overlap), errors are logged without stopping the worker,
// and cancelling the context stops it. Tested with a fake DueCloser whose
// passes can block, so the no-overlap property is observed, not assumed.

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FranklinF25/auction-platform/apps/api/internal/closer"
)

// fakeCloser counts calls and can block each pass on a channel until released.
type fakeCloser struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{} // one signal per call start (buffered)
	release chan struct{} // a pass blocks on this when set
	block   bool
	err     error
}

func newFakeCloser() *fakeCloser {
	return &fakeCloser{entered: make(chan struct{}, 64), release: make(chan struct{})}
}

func (f *fakeCloser) CloseDue(_ context.Context) error {
	f.mu.Lock()
	f.calls++
	block, err, release := f.block, f.err, f.release
	f.mu.Unlock()
	select {
	case f.entered <- struct{}{}:
	default: // never let the signal block a pass
	}
	if block {
		<-release
	}
	return err
}

func (f *fakeCloser) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeCloser) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// waitFor polls until cond is true or the deadline passes.
func waitFor(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(msg)
}

func TestWorkerTicksCallCloseDue(t *testing.T) {
	fc := newFakeCloser()
	w := closer.NewWorker(fc, discardLogger(), 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()

	waitFor(t, 2*time.Second, func() bool { return fc.count() >= 3 },
		"worker should call CloseDue on every tick")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func TestWorkerNeverOverlapsPasses(t *testing.T) {
	fc := newFakeCloser()
	fc.mu.Lock()
	fc.block = true // every pass blocks until released
	fc.mu.Unlock()
	w := closer.NewWorker(fc, discardLogger(), 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()
	defer cancel()

	// The first pass starts and blocks. Give the ticker several intervals:
	// every further tick must be skipped while that pass is in flight.
	<-fc.entered
	time.Sleep(50 * time.Millisecond) // ~10 ticks at 5ms
	if got := fc.count(); got != 1 {
		t.Fatalf("calls = %d while a pass is in flight, want 1 (no overlap)", got)
	}

	// Unblock the first pass with blocking already off, so whichever pass the
	// next tick starts can never stall on the old release channel.
	fc.mu.Lock()
	fc.block = false
	fc.mu.Unlock()
	fc.release <- struct{}{}
	waitFor(t, 2*time.Second, func() bool { return fc.count() >= 2 },
		"worker must resume ticking once the pass finishes")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func TestWorkerStopsAfterCancellation(t *testing.T) {
	fc := newFakeCloser()
	w := closer.NewWorker(fc, discardLogger(), 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()

	waitFor(t, 2*time.Second, func() bool { return fc.count() >= 1 }, "worker should tick at least once")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}

	// No new passes after Run returned.
	count := fc.count()
	time.Sleep(30 * time.Millisecond)
	if got := fc.count(); got != count {
		t.Errorf("calls grew from %d to %d after cancellation", count, got)
	}
}

func TestWorkerWaitsForInFlightPassOnShutdown(t *testing.T) {
	fc := newFakeCloser()
	fc.mu.Lock()
	fc.block = true
	fc.mu.Unlock()
	w := closer.NewWorker(fc, discardLogger(), 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()

	<-fc.entered // a pass is in flight and blocked
	cancel()

	// Run must NOT return while the pass is still running: close events from
	// the in-flight pass need to flush before the caller tears anything down.
	select {
	case <-done:
		t.Fatal("Run returned while a close pass was still in flight")
	case <-time.After(50 * time.Millisecond):
	}

	fc.release <- struct{}{} // let the pass finish
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the in-flight pass finished")
	}
}

func TestWorkerLogsErrorsAndKeepsTicking(t *testing.T) {
	fc := newFakeCloser()
	fc.setErr(io.EOF) // any non-nil error
	w := closer.NewWorker(fc, discardLogger(), 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()
	defer cancel()

	// Errors never stop the worker: calls keep accumulating.
	waitFor(t, 2*time.Second, func() bool { return fc.count() >= 3 },
		"a failing pass must not stop the worker")
}

func TestNewWorkerDefaults(t *testing.T) {
	var calls atomic.Int32
	svc := dueCloserFunc(func(context.Context) error { calls.Add(1); return nil })

	// Non-positive interval falls back to DefaultInterval; nil logger to the
	// default one. Only exercise that construction and one tick work.
	w := closer.NewWorker(svc, nil, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()
	waitFor(t, 3*time.Second, func() bool { return calls.Load() >= 1 },
		"defaulted worker should still tick")
	cancel()
	<-done
}

// dueCloserFunc adapts a function to the DueCloser port.
type dueCloserFunc func(context.Context) error

func (f dueCloserFunc) CloseDue(ctx context.Context) error { return f(ctx) }
