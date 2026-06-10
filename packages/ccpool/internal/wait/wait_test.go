package wait

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

type fakePoller struct {
	calls int
	gen   int64
	state store.State
	// advanceAt: on the Nth poll, bump gen and set state.
	advanceAt int
	advState  store.State
}

func (f *fakePoller) Poll(_ context.Context, _ string) (int64, store.State, bool, error) {
	f.calls++
	if f.calls >= f.advanceAt {
		return f.gen + 1, f.advState, true, nil
	}
	return f.gen, f.state, true, nil
}

func TestForGenerationAdvance_returnsNewStateOnAdvance(t *testing.T) {
	fp := &fakePoller{gen: 5, state: store.Working, advanceAt: 3, advState: store.Done}
	out, err := ForGenerationAdvance(context.Background(), fp, "alpha", 5, Opts{
		Timeout: time.Second, Interval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.State != store.Done || out.TimedOut {
		t.Errorf("out = %+v, want state=done timedOut=false", out)
	}
	if fp.calls < 3 {
		t.Errorf("polled %d times, expected >=3", fp.calls)
	}
}

func TestForGenerationAdvance_timesOut(t *testing.T) {
	fp := &fakePoller{gen: 5, state: store.Working, advanceAt: 1 << 30} // never advances
	out, err := ForGenerationAdvance(context.Background(), fp, "alpha", 5, Opts{
		Timeout: 20 * time.Millisecond, Interval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !out.TimedOut {
		t.Errorf("expected TimedOut, got %+v", out)
	}
}
