// Package sched runs a function on a fixed period, single-flight: a run that
// overruns its period causes the following tick to be skipped rather than
// queued or overlapped.
package sched

import (
	"context"
	"sync"
	"time"
)

// Ticker is the subset of time.Ticker the scheduler needs.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// Clock creates tickers and reads the time; tests substitute a fake.
type Clock interface {
	Now() time.Time
	NewTicker(d time.Duration) Ticker
}

// RealClock is the wall clock.
type RealClock struct{}

// Now implements Clock.
func (RealClock) Now() time.Time { return time.Now() }

// NewTicker implements Clock.
func (RealClock) NewTicker(d time.Duration) Ticker { return realTicker{time.NewTicker(d)} }

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time { return r.t.C }
func (r realTicker) Stop()               { r.t.Stop() }

// Run calls fn once immediately and then on every tick of period, until ctx is
// cancelled. It never runs fn concurrently with itself: a tick that arrives
// while a run is in flight is dropped and onSkip (if non-nil) is called. Run
// returns after the in-flight run, if any, has finished.
func Run(ctx context.Context, clock Clock, period time.Duration, fn func(context.Context), onSkip func()) {
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		inflight bool
	)
	launch := func() bool {
		mu.Lock()
		defer mu.Unlock()
		if inflight {
			return false
		}
		inflight = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(ctx)
			mu.Lock()
			inflight = false
			mu.Unlock()
		}()
		return true
	}

	ticker := clock.NewTicker(period)
	defer ticker.Stop()
	launch()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-ticker.C():
			if !launch() && onSkip != nil {
				onSkip()
			}
		}
	}
}
