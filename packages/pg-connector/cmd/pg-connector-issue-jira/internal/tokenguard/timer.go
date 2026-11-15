package tokenguard

import (
	"sort"
	"sync"
	"time"
)

// Timer is the guard's timer seam (governor.Clock has only Now).
type Timer interface {
	// After returns a channel that receives once d has elapsed.
	After(d time.Duration) <-chan time.Time
}

// SystemTimer is the real timer.
type SystemTimer struct{}

// After is time.After.
func (SystemTimer) After(d time.Duration) <-chan time.Time { return time.After(d) }

// ManualTimer is a Timer that fires only when Advance moves it past a
// waiter's deadline. It is safe for concurrent use and is exported for tests
// of this and neighbouring packages.
type ManualTimer struct {
	mu      sync.Mutex
	cond    *sync.Cond
	now     time.Duration
	waiters []*waiter
}

type waiter struct {
	at time.Duration
	ch chan time.Time
}

// NewManualTimer returns a ManualTimer at time zero.
func NewManualTimer() *ManualTimer {
	t := &ManualTimer{}
	t.cond = sync.NewCond(&t.mu)
	return t
}

// After registers a waiter due d from now.
func (t *ManualTimer) After(d time.Duration) <-chan time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	w := &waiter{at: t.now + d, ch: make(chan time.Time, 1)}
	t.waiters = append(t.waiters, w)
	t.cond.Broadcast()
	return w.ch
}

// Advance moves the timer forward by d and fires every waiter now due, in
// deadline order.
func (t *ManualTimer) Advance(d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.now += d
	sort.SliceStable(t.waiters, func(i, j int) bool { return t.waiters[i].at < t.waiters[j].at })
	var rest []*waiter
	for _, w := range t.waiters {
		if w.at <= t.now {
			w.ch <- time.Time{}
		} else {
			rest = append(rest, w)
		}
	}
	t.waiters = rest
}

// Pending reports how many waiters have not fired.
func (t *ManualTimer) Pending() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.waiters)
}

// AwaitPending blocks until at least n waiters are pending.
func (t *ManualTimer) AwaitPending(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for len(t.waiters) < n {
		t.cond.Wait()
	}
}
