package wait

import (
	"context"
	"errors"
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
	fp := &fakePoller{gen: 5, state: store.Working, advanceAt: 3, advState: store.Idle}
	out, err := ForGenerationAdvance(context.Background(), fp, "alpha", 5, Opts{
		Timeout: time.Second, Interval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.State != store.Idle || out.TimedOut {
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

// A failed liveness check ends the wait at once with its error, long before the
// (here one-hour) timeout.
func TestForGenerationAdvance_livenessFailureEndsWaitAtOnce(t *testing.T) {
	fp := &fakePoller{gen: 5, state: store.Starting, advanceAt: 1 << 30}
	boom := errors.New("pane died")
	checks := 0
	start := time.Now()
	out, err := ForGenerationAdvance(context.Background(), fp, "alpha", 5, Opts{
		Timeout: time.Hour, Interval: time.Millisecond,
		Liveness: func(context.Context) error {
			checks++
			if checks == 3 {
				return boom
			}
			return nil
		},
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if out.TimedOut {
		t.Errorf("a liveness failure is not a timeout: %+v", out)
	}
	if checks != 3 {
		t.Errorf("liveness consulted %d times, want 3 (once per non-advancing poll)", checks)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("wait took %v; want it to end at once", time.Since(start))
	}
}

// An advance wins over the liveness check: a session that reached ready and
// then ended must not be reported as a failed launch.
func TestForGenerationAdvance_advanceSkipsLiveness(t *testing.T) {
	fp := &fakePoller{gen: 5, state: store.Starting, advanceAt: 1, advState: store.Ready}
	out, err := ForGenerationAdvance(context.Background(), fp, "alpha", 5, Opts{
		Timeout: time.Second, Interval: time.Millisecond,
		Liveness: func(context.Context) error { return errors.New("must not be consulted") },
	})
	if err != nil || out.State != store.Ready {
		t.Fatalf("out=%+v err=%v, want ready and no error", out, err)
	}
}

func TestForGenerationAdvance_timeoutCarriesDiagnosis(t *testing.T) {
	fp := &fakePoller{gen: 5, state: store.Starting, advanceAt: 1 << 30}
	out, err := ForGenerationAdvance(context.Background(), fp, "alpha", 5, Opts{
		Timeout: 10 * time.Millisecond, Interval: time.Millisecond,
		Liveness: func(context.Context) error { return nil },
		Diagnose: func(context.Context) string { return "the pane tail" },
	})
	if err != nil || !out.TimedOut {
		t.Fatalf("out=%+v err=%v, want a timeout", out, err)
	}
	if out.Detail != "the pane tail" {
		t.Errorf("Detail = %q, want the diagnosis", out.Detail)
	}
}
