package sched

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeTicker struct {
	ch      chan time.Time
	stopped atomic.Bool
}

func (f *fakeTicker) C() <-chan time.Time { return f.ch }
func (f *fakeTicker) Stop()               { f.stopped.Store(true) }

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	tickers []*fakeTicker
	period  time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTicker(d time.Duration) Ticker {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTicker{ch: make(chan time.Time)}
	c.tickers = append(c.tickers, t)
	c.period = d
	return t
}

func (c *fakeClock) ticker(t *testing.T) *fakeTicker {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		if len(c.tickers) > 0 {
			tk := c.tickers[0]
			c.mu.Unlock()
			return tk
		}
		c.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("scheduler never created a ticker")
	return nil
}

func tick(t *testing.T, tk *fakeTicker) {
	t.Helper()
	select {
	case tk.ch <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler is not receiving ticks")
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRunsImmediatelyThenOnEveryTick(t *testing.T) {
	clock := &fakeClock{}
	var runs atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, clock, 7*time.Second, func(context.Context) { runs.Add(1) }, nil)
		close(done)
	}()
	tk := clock.ticker(t)
	waitFor(t, func() bool { return runs.Load() == 1 }, "the immediate run")
	if clock.period != 7*time.Second {
		t.Fatalf("ticker period = %v", clock.period)
	}
	tick(t, tk)
	waitFor(t, func() bool { return runs.Load() == 2 }, "the run after tick 1")
	tick(t, tk)
	waitFor(t, func() bool { return runs.Load() == 3 }, "the run after tick 2")
	cancel()
	<-done
	if !tk.stopped.Load() {
		t.Fatal("ticker not stopped on exit")
	}
}

func TestOverrunSkipsRatherThanQueuesOrOverlaps(t *testing.T) {
	clock := &fakeClock{}
	var (
		active, maxActive, runs atomic.Int32
		skipped                 atomic.Int32
	)
	release := make(chan struct{})
	started := make(chan struct{}, 10)
	fn := func(context.Context) {
		n := active.Add(1)
		for {
			m := maxActive.Load()
			if n <= m || maxActive.CompareAndSwap(m, n) {
				break
			}
		}
		runs.Add(1)
		started <- struct{}{}
		<-release
		active.Add(-1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, clock, time.Second, fn, func() { skipped.Add(1) })
		close(done)
	}()
	tk := clock.ticker(t)
	<-started // first run is in flight and blocked

	tick(t, tk)
	tick(t, tk)
	tick(t, tk)
	waitFor(t, func() bool { return skipped.Load() == 3 }, "three skipped ticks")
	if runs.Load() != 1 {
		t.Fatalf("runs = %d while the first run is in flight, want 1", runs.Load())
	}

	release <- struct{}{} // finish run 1
	waitFor(t, func() bool { return active.Load() == 0 }, "run 1 to finish")
	// The skipped ticks were NOT queued: nothing runs until the next tick.
	time.Sleep(20 * time.Millisecond)
	if runs.Load() != 1 {
		t.Fatalf("runs = %d after the overrun, want skipped ticks not to queue", runs.Load())
	}
	tick(t, tk)
	<-started
	if runs.Load() != 2 {
		t.Fatalf("runs = %d, want 2", runs.Load())
	}
	if maxActive.Load() != 1 {
		t.Fatalf("max concurrent runs = %d, want 1 (single-flight)", maxActive.Load())
	}
	close(release)
	cancel()
	<-done
}

func TestRunWaitsForTheInflightRunOnShutdown(t *testing.T) {
	clock := &fakeClock{}
	var finished atomic.Bool
	started := make(chan struct{})
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, clock, time.Second, func(context.Context) {
			close(started)
			<-release
			finished.Store(true)
		}, nil)
		close(done)
	}()
	<-started
	cancel()
	select {
	case <-done:
		t.Fatal("Run returned before the in-flight run finished")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	<-done
	if !finished.Load() {
		t.Fatal("in-flight run did not finish")
	}
}

func TestNilSkipCallbackIsFine(t *testing.T) {
	clock := &fakeClock{}
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, clock, time.Second, func(context.Context) { started <- struct{}{}; <-release }, nil)
		close(done)
	}()
	tk := clock.ticker(t)
	<-started
	tick(t, tk) // skipped with a nil callback must not panic
	close(release)
	cancel()
	<-done
}

func TestRealClockTicks(t *testing.T) {
	var c RealClock
	if time.Since(c.Now()) > time.Minute {
		t.Fatal("RealClock.Now is wrong")
	}
	tk := c.NewTicker(5 * time.Millisecond)
	defer tk.Stop()
	select {
	case <-tk.C():
	case <-time.After(5 * time.Second):
		t.Fatal("real ticker never ticked")
	}
}
