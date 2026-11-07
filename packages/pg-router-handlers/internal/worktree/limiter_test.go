package worktree

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeClock advances only when a drain "sleeps", so a test sees exactly how
// removal events spread over time without waiting on the wall clock.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	return nil
}

func TestLimiter_reservationsAreLaidEndToEnd(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	l := NewLimiter(LimiterConfig{EventsPerMinute: 600, Now: clk.Now}) // 100 ms per event
	if w := l.Reserve(10); w != 0 {
		t.Fatalf("first reservation after idle waits %v, want 0", w)
	}
	if w := l.Reserve(10); w != time.Second {
		t.Fatalf("second reservation waits %v, want 1s", w)
	}
	if w := l.Reserve(5); w != 2*time.Second {
		t.Fatalf("third reservation waits %v, want 2s", w)
	}
	clk.Sleep(context.Background(), time.Hour)
	if w := l.Reserve(10); w != 0 {
		t.Fatalf("reservation after idle waits %v, want 0", w)
	}
}

func TestDrainTree_neverExceedsTheEventsPerMinuteBudget(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	l := NewLimiter(LimiterConfig{EventsPerMinute: 600, Now: clk.Now})
	r := Removal{BatchSize: 10, Pause: -1, Budget: time.Hour, Sleep: clk.Sleep, Limiter: l}
	root := filepath.Join(t.TempDir(), "tree")
	ops := makeTree(t, root, 5, 20) // 5*(20+2)+1 = 111 unlink/rmdir calls
	start := clk.Now()
	st, err := r.DrainTree(context.Background(), root)
	if err != nil || !st.Done || st.Ops != ops {
		t.Fatalf("drain = %+v, %v; want done with %d ops", st, err, ops)
	}
	elapsed := clk.Now().Sub(start)
	// 600 per minute: the events after the first chunk of 10 take (ops-10)/600 min.
	want := time.Duration(ops-10) * time.Minute / 600
	if elapsed < want-time.Duration(10)*time.Minute/600 || elapsed > want+time.Duration(10)*time.Minute/600 {
		t.Fatalf("elapsed %v, want about %v (events spread at the budget)", elapsed, want)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("tree still present: %v", err)
	}
}

func TestDrainTree_concurrentDrainsShareOneBudget(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	l := NewLimiter(LimiterConfig{EventsPerMinute: 600, Now: clk.Now})
	r := Removal{BatchSize: 10, Pause: -1, Budget: time.Hour, Sleep: clk.Sleep, Limiter: l}
	base := t.TempDir()
	total := 0
	var roots []string
	for _, n := range []string{"a", "b"} {
		root := filepath.Join(base, n)
		total += makeTree(t, root, 5, 20)
		roots = append(roots, root)
	}
	var wg sync.WaitGroup
	for _, root := range roots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.DrainTree(context.Background(), root); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	// Two drains together still obey one budget: total events spread over at
	// least (total - 2 first chunks) / 600 per minute.
	min := time.Duration(total-20) * time.Minute / 600
	if got := clk.Now().Sub(time.Unix(1000, 0)); got < min {
		t.Fatalf("two drains finished in %v, below the shared-budget floor %v", got, min)
	}
}

func TestDrainTree_fullQueueLeavesTreeForTheSweepAndLosesNothing(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	l := NewLimiter(LimiterConfig{EventsPerMinute: 600, MaxPending: 1, Now: clk.Now})
	r := Removal{BatchSize: 10, Pause: -1, Budget: time.Hour, Sleep: clk.Sleep, Limiter: l}
	root := filepath.Join(t.TempDir(), "tree")
	makeTree(t, root, 2, 5)

	release, ok := l.Admit() // another drain holds the only place
	if !ok {
		t.Fatal("first Admit refused")
	}
	st, err := r.DrainTree(context.Background(), root)
	if err != nil || st.Done || st.Ops != 0 {
		t.Fatalf("drain with a full queue = %+v, %v; want untouched, not done", st, err)
	}
	if _, err := os.Lstat(root); err != nil {
		t.Fatalf("tree was touched while the queue was full: %v", err)
	}
	release()
	release() // idempotent
	if l.Pending() != 0 {
		t.Fatalf("pending = %d after release, want 0", l.Pending())
	}
	// The sweep's later pass (same call, queue now free) finishes the job.
	st, err = r.DrainTree(context.Background(), root)
	if err != nil || !st.Done {
		t.Fatalf("later drain = %+v, %v; want done", st, err)
	}
	if l.Pending() != 0 {
		t.Fatalf("pending = %d after a drain, want 0", l.Pending())
	}
}

func TestLimiterFromEnv(t *testing.T) {
	env := map[string]string{EnvEventsPerMinute: "120", EnvMaxPending: "3"}
	l := LimiterFromEnv(func(k string) string { return env[k] })
	if l.cfg.EventsPerMinute != 120 || l.cfg.MaxPending != 3 {
		t.Fatalf("cfg = %+v", l.cfg)
	}
	d := LimiterFromEnv(func(string) string { return "junk" })
	if d.cfg.EventsPerMinute != DefaultEventsPerMinute || d.cfg.MaxPending != DefaultMaxPending {
		t.Fatalf("defaults = %+v", d.cfg)
	}
	off := LimiterFromEnv(func(k string) string {
		if k == EnvEventsPerMinute {
			return "0"
		}
		return ""
	})
	if off.Reserve(1000) != 0 {
		t.Fatal("budget 0 should disable the rate limit")
	}
}

func TestNilLimiterIsUnlimited(t *testing.T) {
	var l *Limiter
	rel, ok := l.Admit()
	if !ok {
		t.Fatal("nil limiter refused")
	}
	rel()
	if l.Reserve(100) != 0 || l.Pending() != 0 {
		t.Fatal("nil limiter should be a no-op")
	}
}
