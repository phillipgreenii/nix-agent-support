package recorderfacts

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestForEachBoundedVisitsEveryIndexWithinTheLimit(t *testing.T) {
	var inFlight, peak, visited atomic.Int32
	err := forEachBounded(context.Background(), 40, 4, func(_ context.Context, _ int) error {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		inFlight.Add(-1)
		visited.Add(1)
		return nil
	})
	if err != nil || visited.Load() != 40 {
		t.Fatalf("err=%v visited=%d", err, visited.Load())
	}
	if p := peak.Load(); p > 4 || p < 2 {
		t.Errorf("peak in flight = %d, want between 2 and 4", p)
	}
}

func TestForEachBoundedStopsOnTheFirstError(t *testing.T) {
	boom := errors.New("boom")
	var started atomic.Int32
	err := forEachBounded(context.Background(), 1000, 2, func(ctx context.Context, i int) error {
		started.Add(1)
		if i == 3 {
			return boom
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Millisecond):
		}
		return nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if s := started.Load(); s >= 1000 {
		t.Errorf("started %d calls; the error should stop the rest", s)
	}
}

func TestForEachBoundedHonoursParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var ran atomic.Int32
	err := forEachBounded(ctx, 10, 3, func(context.Context, int) error { ran.Add(1); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}
