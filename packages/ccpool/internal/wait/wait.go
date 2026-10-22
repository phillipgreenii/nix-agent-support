// Package wait blocks until a session row's generation advances past a snapshot
// taken before the prompt/launch (so a transition landing the instant after the
// send is still observed, closing the lost-wakeup race). It polls the store — no
// sentinels — so it observes transitions written by the (separate-process) hooks.
package wait

import (
	"context"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// Poller reads a row's current generation + state. ok=false means no such row.
type Poller interface {
	Poll(ctx context.Context, name string) (generation int64, state store.State, ok bool, err error)
}

type Opts struct {
	Timeout  time.Duration
	Interval time.Duration
	// Liveness, when set, is called after every poll that has not advanced. A
	// non-nil error means the awaited launch can no longer succeed: the wait
	// ends at once and returns that error instead of waiting out Timeout.
	Liveness func(ctx context.Context) error
	// Diagnose, when set, is called once when the wait times out; its text is
	// returned in Outcome.Detail.
	Diagnose func(ctx context.Context) string
}

type Outcome struct {
	State    store.State
	TimedOut bool
	// Detail is Opts.Diagnose's text on a timeout; empty otherwise.
	Detail string
}

// ForGenerationAdvance polls until generation > since, the deadline elapses, or
// ctx is cancelled. On advance it returns the freshly-read state.
func ForGenerationAdvance(ctx context.Context, p Poller, name string, since int64, o Opts) (Outcome, error) {
	if o.Interval <= 0 {
		o.Interval = 250 * time.Millisecond
	}
	deadline := time.Now().Add(o.Timeout)
	for {
		gen, st, ok, err := p.Poll(ctx, name)
		if err != nil {
			return Outcome{}, err
		}
		if ok && gen > since {
			return Outcome{State: st}, nil
		}
		if o.Liveness != nil {
			if lerr := o.Liveness(ctx); lerr != nil {
				return Outcome{State: st}, lerr
			}
		}
		if time.Now().After(deadline) {
			out := Outcome{State: st, TimedOut: true}
			if o.Diagnose != nil {
				out.Detail = o.Diagnose(ctx)
			}
			return out, nil
		}
		select {
		case <-ctx.Done():
			return Outcome{State: st, TimedOut: true}, ctx.Err()
		case <-time.After(o.Interval):
		}
	}
}
