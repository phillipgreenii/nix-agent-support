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
}

type Outcome struct {
	State    store.State
	TimedOut bool
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
		if time.Now().After(deadline) {
			return Outcome{State: st, TimedOut: true}, nil
		}
		select {
		case <-ctx.Done():
			return Outcome{State: st, TimedOut: true}, ctx.Err()
		case <-time.After(o.Interval):
		}
	}
}
