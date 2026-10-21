package metrics

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
)

// fakeClock is a settable time source for the TTL checks.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// countingFocus returns a FocusFunc that reports Selected = the number of
// computations so far, so a test sees which computation a scrape was served.
func countingFocus(calls *atomic.Int32) FocusFunc {
	return func() (focus.Health, error) {
		return focus.Health{Selected: int(calls.Add(1))}, nil
	}
}

func TestCachedFocusServesAFreshValueWithoutRecomputing(t *testing.T) {
	var calls atomic.Int32
	clk := &fakeClock{t: time.Unix(1_000, 0)}
	c := newCachedFocus(countingFocus(&calls), 30*time.Second, clk.now)

	for i := 0; i < 5; i++ {
		h, err := c.get()
		if err != nil || h.Selected != 1 {
			t.Fatalf("scrape %d = %d, %v; want the first computation (1)", i, h.Selected, err)
		}
		clk.advance(5 * time.Second) // 25 s in total: still inside the TTL
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("computations = %d, want 1: scrapes inside the TTL must not recompute", got)
	}
}

func TestCachedFocusRefreshesInTheBackgroundAfterTheTTL(t *testing.T) {
	var calls atomic.Int32
	clk := &fakeClock{t: time.Unix(1_000, 0)}
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	fn := func() (focus.Health, error) {
		n := int(calls.Add(1))
		if n > 1 { // only the refresh blocks, never the first call
			started <- struct{}{}
			<-release
		}
		return focus.Health{Selected: n}, nil
	}
	c := newCachedFocus(fn, 30*time.Second, clk.now)

	if h, _ := c.get(); h.Selected != 1 {
		t.Fatalf("first get = %d, want 1", h.Selected)
	}
	clk.advance(31 * time.Second)

	// The stale value is served at once even though the refresh is blocked: the
	// scrape path never waits on the computation.
	got := make(chan int, 1)
	go func() { h, _ := c.get(); got <- h.Selected }()
	<-started
	select {
	case n := <-got:
		if n != 1 {
			t.Fatalf("stale get = %d, want the cached 1", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a scrape blocked on the background refresh")
	}

	// Scrapes arriving while the refresh runs join it: still one computation.
	for i := 0; i < 3; i++ {
		if h, _ := c.get(); h.Selected != 1 {
			t.Fatalf("get during refresh = %d, want 1", h.Selected)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("computations = %d, want 2 (first + ONE refresh)", got)
	}

	close(release)
	c.settle()
	if h, _ := c.get(); h.Selected != 2 {
		t.Fatalf("after the refresh get = %d, want 2", h.Selected)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("computations = %d, want 2 (the refreshed value is fresh)", got)
	}
}

func TestCachedFocusConcurrentFirstCallsShareOneComputation(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	fn := func() (focus.Health, error) {
		calls.Add(1)
		<-release
		return focus.Health{Selected: 7}, nil
	}
	c := newCachedFocus(fn, time.Minute, time.Now)

	var wg sync.WaitGroup
	results := make([]int, 6)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, _ := c.get()
			results[i] = h.Selected
		}()
	}
	// Let every caller reach the wait, then finish the one computation.
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("computations = %d, want 1 for concurrent first calls", got)
	}
	for i, r := range results {
		if r != 7 {
			t.Errorf("caller %d got %d, want 7", i, r)
		}
	}
}

func TestCachedFocusSurfacesAFailedRefreshOnTheNextScrape(t *testing.T) {
	var calls atomic.Int32
	boom := errors.New("store unreadable")
	fn := func() (focus.Health, error) {
		if calls.Add(1) > 1 {
			return focus.Health{}, boom
		}
		return focus.Health{Selected: 1}, nil
	}
	clk := &fakeClock{t: time.Unix(1_000, 0)}
	c := newCachedFocus(fn, 30*time.Second, clk.now)

	if _, err := c.get(); err != nil {
		t.Fatalf("first get: %v", err)
	}
	clk.advance(time.Minute)
	if _, err := c.get(); err != nil { // stale-while-revalidate: the old good value
		t.Fatalf("stale get: %v", err)
	}
	c.settle()
	if _, err := c.get(); !errors.Is(err, boom) { // the failure is not masked
		t.Fatalf("get after a failed refresh = %v, want %v", err, boom)
	}
}

func TestCachedFocusWorksThroughRegisterFocus(t *testing.T) {
	var calls atomic.Int32
	fn := CachedFocus(func() (focus.Health, error) {
		calls.Add(1)
		return fullHealth(), nil
	}, time.Minute)

	first, err := collectFocus(t, fn)
	if err != nil {
		t.Fatal(err)
	}
	second, err := collectFocus(t, fn)
	if err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("computations over two scrapes = %d, want 1", got)
	}
	a, b := series(first), series(second)
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("series counts %d vs %d", len(a), len(b))
	}
	for k, v := range a {
		if b[k] != v {
			t.Errorf("%s = %d then %d across a cached scrape", k, v, b[k])
		}
	}
}
