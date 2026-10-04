package closer_test

// Expiry worker behavior mirrors the close worker's contract: every tick
// drives one ExpireDueTransactions pass, a slow pass makes ticks skip (never
// overlap), errors are logged without stopping the worker, and cancelling the
// context stops it. The shared tick engine is the same runLoop the close
// worker uses; these tests pin the M4 wiring onto it.

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/FranklinF25/auction-platform/apps/api/internal/closer"
)

// fakeExpirer counts calls and can block each pass until released. It shares
// the close worker fake's mutex discipline: the worker goroutine writes while
// the test goroutine reads, and go test -race must stay clean.
type fakeExpirer struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
	block   bool
	err     error
}

func newFakeExpirer() *fakeExpirer {
	return &fakeExpirer{entered: make(chan struct{}, 64), release: make(chan struct{})}
}

func (f *fakeExpirer) ExpireDueTransactions(_ context.Context) (int, error) {
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
	// The count itself is the domain's business; the worker only relays it.
	return 0, err
}

func (f *fakeExpirer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeExpirer) setBlock(block bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.block = block
}

func (f *fakeExpirer) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func TestExpiryWorkerTicksCallExpireDueTransactions(t *testing.T) {
	fe := newFakeExpirer()
	w := closer.NewExpiryWorker(fe, discardLogger(), 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()

	waitFor(t, 2*time.Second, func() bool { return fe.count() >= 3 },
		"expiry worker should call ExpireDueTransactions on every tick")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func TestExpiryWorkerNeverOverlapsPasses(t *testing.T) {
	fe := newFakeExpirer()
	fe.setBlock(true) // every pass blocks until released
	w := closer.NewExpiryWorker(fe, discardLogger(), 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()
	defer cancel()

	// The first pass starts and blocks; every further tick must be skipped.
	<-fe.entered
	time.Sleep(50 * time.Millisecond) // ~10 ticks at 5ms
	if fe.count() != 1 {
		t.Fatalf("calls = %d while a pass is in flight, want 1 (no overlap)", fe.count())
	}

	fe.setBlock(false)
	fe.release <- struct{}{}
	waitFor(t, 2*time.Second, func() bool { return fe.count() >= 2 },
		"expiry worker must resume ticking once the pass finishes")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func TestExpiryWorkerStopsAfterCancellation(t *testing.T) {
	fe := newFakeExpirer()
	fe.setErr(io.EOF) // errors never stop the worker either
	w := closer.NewExpiryWorker(fe, discardLogger(), 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()

	waitFor(t, 2*time.Second, func() bool { return fe.count() >= 1 },
		"expiry worker should tick at least once despite errors")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}

	// No new passes after Run returned.
	count := fe.count()
	time.Sleep(30 * time.Millisecond)
	if fe.count() != count {
		t.Errorf("calls grew from %d to %d after cancellation", count, fe.count())
	}
}

func TestNewExpiryWorkerDefaults(t *testing.T) {
	if closer.DefaultExpiryInterval <= 0 {
		t.Fatal("DefaultExpiryInterval must be positive")
	}
	// Construction-level defaults are pinned by the internal test
	// (expiry_internal_test.go); driving a full 30-second default tick here
	// would slow the suite for no extra signal.
}
