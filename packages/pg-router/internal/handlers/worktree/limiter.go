package worktree

import (
	"os"
	"strconv"
	"sync"
	"time"
)

// Process-wide removal budget.
//
// Pacing each removal on its own (Removal.BatchSize and Pause) bounds one
// delete, but several removals at once (a sweep pass, a teardown and an orphan
// reconcile together) add up to a burst again. Limiter is the shared half: one
// events-per-minute budget that every drain in the process draws from, plus a
// bound on how many drains may be pending at once. A drain that finds the queue
// full is not lost: the tree has already been renamed into the trash, and the
// janitor sweep deletes it on a later pass, so the cost of a full queue is
// latency, never a leaked or half-registered worktree. The same holds across a
// restart, because the queue lives only in memory and the trash is on disk.

// Defaults for the shared limiter. 60,000 events per minute is 1,000 a second,
// a tenth of the unthrottled delete (about 11,000 a second measured) and enough
// for the per-removal budget to finish a 100,000-file tree.
const (
	DefaultEventsPerMinute = 60000
	DefaultMaxPending      = 8
)

// Environment variables that tune SharedLimiter. A value that does not parse as
// a positive integer is ignored. Setting the events variable to 0 disables the
// rate budget (the per-removal pacing still applies).
const (
	EnvEventsPerMinute = "PG_ROUTER_WORKTREE_REMOVAL_EVENTS_PER_MINUTE"
	EnvMaxPending      = "PG_ROUTER_WORKTREE_REMOVAL_MAX_PENDING"
)

// LimiterConfig configures a Limiter. EventsPerMinute <= 0 means no rate
// budget; MaxPending <= 0 means an unbounded queue.
type LimiterConfig struct {
	EventsPerMinute int
	MaxPending      int
	// Now is the clock; tests inject a fake (default time.Now).
	Now func() time.Time
}

// Limiter is a shared removal budget. It is safe for concurrent use.
type Limiter struct {
	cfg LimiterConfig

	mu      sync.Mutex
	next    time.Time // earliest start of the next reservation
	pending int
}

// NewLimiter returns a Limiter for cfg.
func NewLimiter(cfg LimiterConfig) *Limiter {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Limiter{cfg: cfg}
}

// LimiterFromEnv builds a Limiter from the defaults and the Env* variables.
func LimiterFromEnv(getenv func(string) string) *Limiter {
	cfg := LimiterConfig{EventsPerMinute: DefaultEventsPerMinute, MaxPending: DefaultMaxPending}
	if v, err := strconv.Atoi(getenv(EnvEventsPerMinute)); err == nil && v >= 0 {
		cfg.EventsPerMinute = v
	}
	if v, err := strconv.Atoi(getenv(EnvMaxPending)); err == nil && v > 0 {
		cfg.MaxPending = v
	}
	return NewLimiter(cfg)
}

// SharedLimiter is the budget every removal in this process uses.
var SharedLimiter = LimiterFromEnv(os.Getenv)

// Admit takes a place in the pending queue. When the queue is full it returns
// false and the caller leaves its tree in the trash for the sweep. A true result
// must be paired with a call to release.
func (l *Limiter) Admit() (release func(), ok bool) {
	if l == nil {
		return func() {}, true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cfg.MaxPending > 0 && l.pending >= l.cfg.MaxPending {
		return nil, false
	}
	l.pending++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			l.pending--
			l.mu.Unlock()
		})
	}, true
}

// Pending reports how many drains hold a place in the queue.
func (l *Limiter) Pending() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pending
}

// Reserve books n events against the budget and returns how long the caller
// must wait before issuing them. Reservations are laid end to end at
// minute/EventsPerMinute per event, so over any window the events started
// never exceed the budget by more than one reservation (the first reservation
// after an idle period starts at once).
func (l *Limiter) Reserve(n int) time.Duration {
	if l == nil || l.cfg.EventsPerMinute <= 0 || n <= 0 {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.cfg.Now()
	start := l.next
	if start.Before(now) {
		start = now
	}
	l.next = start.Add(time.Duration(n) * time.Minute / time.Duration(l.cfg.EventsPerMinute))
	return start.Sub(now)
}
