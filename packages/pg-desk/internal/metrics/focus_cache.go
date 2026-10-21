package metrics

import (
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
)

// CachedFocus wraps fn so a /metrics scrape never runs the full focus health
// computation (store Load, Rank and the per-entity classification, all of
// which grow with the store) on the scrape path once a value exists.
//
// The wrapper is a single-flight, stale-while-revalidate cache:
//   - a value younger than ttl is returned as is;
//   - an older value is returned immediately (it is at most one scrape
//     interval plus one computation behind) and ONE background refresh is
//     started, however many scrapes arrive meanwhile;
//   - the very first call has nothing to serve, so it waits for the first
//     computation (concurrent first calls share it).
//
// A failed computation is cached like a value, so an error is surfaced by the
// next scrape rather than being masked by an older good value: the lag is one
// scrape, never a quiet all-clear. Because a collection that the scraper has
// abandoned still finishes its computation, a first scrape that outlives the
// scraper's timeout still warms the cache for the next one.
func CachedFocus(fn FocusFunc, ttl time.Duration) FocusFunc {
	return newCachedFocus(fn, ttl, time.Now).get
}

type cachedFocus struct {
	fn  FocusFunc
	ttl time.Duration
	now func() time.Time

	mu     sync.Mutex
	have   bool
	health focus.Health
	err    error
	at     time.Time
	flight chan struct{} // non-nil, and closed on completion, while a refresh runs
}

func newCachedFocus(fn FocusFunc, ttl time.Duration, now func() time.Time) *cachedFocus {
	return &cachedFocus{fn: fn, ttl: ttl, now: now}
}

func (c *cachedFocus) get() (focus.Health, error) {
	c.mu.Lock()
	if c.have && c.now().Sub(c.at) < c.ttl {
		h, err := c.health, c.err
		c.mu.Unlock()
		return h, err
	}
	flight := c.refreshLocked()
	if c.have {
		h, err := c.health, c.err
		c.mu.Unlock()
		return h, err
	}
	c.mu.Unlock()

	<-flight
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.health, c.err
}

// refreshLocked returns the refresh in flight, starting one when none runs.
// The caller MUST hold c.mu.
func (c *cachedFocus) refreshLocked() chan struct{} {
	if c.flight != nil {
		return c.flight
	}
	done := make(chan struct{})
	c.flight = done
	go func() {
		defer close(done)
		var (
			h   focus.Health
			err error
		)
		// Clear the flight even if fn panics, so a later call can retry.
		defer func() {
			c.mu.Lock()
			c.flight = nil
			c.mu.Unlock()
		}()
		h, err = c.fn()
		c.mu.Lock()
		c.have, c.health, c.err, c.at = true, h, err, c.now()
		c.mu.Unlock()
	}()
	return done
}

// settle blocks until no refresh is in flight (tests only).
func (c *cachedFocus) settle() {
	c.mu.Lock()
	f := c.flight
	c.mu.Unlock()
	if f != nil {
		<-f
	}
}
