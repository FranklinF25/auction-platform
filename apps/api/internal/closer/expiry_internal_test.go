package closer

// Internal test: construction defaults of the expiry worker. The 30-second
// default interval makes a live tick impractical in a unit test, so the
// defaulting itself is pinned here while the tick behavior (no-overlap,
// cancellation, error tolerance) is covered by the external tests against the
// shared runLoop engine.

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

type noopExpirer struct{}

func (noopExpirer) ExpireDueTransactions(context.Context) (int, error) { return 0, nil }

func TestNewExpiryWorkerConstruction(t *testing.T) {
	w := NewExpiryWorker(noopExpirer{}, nil, 0)
	if w.interval != DefaultExpiryInterval {
		t.Errorf("default interval = %v, want %v", w.interval, DefaultExpiryInterval)
	}
	if w.logger == nil {
		t.Error("nil logger must fall back to the default logger")
	}

	explicit := slog.New(slog.NewTextHandler(io.Discard, nil))
	w2 := NewExpiryWorker(noopExpirer{}, explicit, time.Minute)
	if w2.interval != time.Minute {
		t.Errorf("explicit interval = %v, want the given time.Minute", w2.interval)
	}
	if w2.logger != explicit {
		t.Error("explicit logger must be kept")
	}
}
