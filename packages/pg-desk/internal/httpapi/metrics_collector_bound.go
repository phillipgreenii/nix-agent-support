package httpapi

import (
	"fmt"
	"sync"
	"time"
)

// collectorTimeout bounds how long one collector of the /metrics catalog may
// hold a snapshot build. Variable only so a test can shorten it.
var collectorTimeout = 10 * time.Second

// boundedCollector wraps one catalog data function so a hung call (a store
// read stuck behind a lock, a slow external call) cannot stretch the whole
// snapshot build (Bulkhead with a Timeout). It is single-flight: a call that
// outlives the timeout keeps running in the background and later calls join it
// instead of piling up. On timeout it serves the last good value, or an error
// when there has been none yet.
type boundedCollector[T any] struct {
	name    string
	timeout time.Duration
	fn      func() (T, error)

	mu      sync.Mutex
	flight  chan struct{} // non-nil while a call runs; closed on completion
	last    T
	have    bool
	lastErr error
}

func newBounded[T any](name string, fn func() (T, error)) func() (T, error) {
	return (&boundedCollector[T]{name: name, timeout: collectorTimeout, fn: fn}).get
}

func (c *boundedCollector[T]) get() (T, error) {
	c.mu.Lock()
	if c.flight == nil {
		flight := make(chan struct{})
		c.flight = flight
		go func() {
			v, err := c.fn()
			c.mu.Lock()
			c.lastErr = err
			if err == nil {
				c.last, c.have = v, true
			}
			c.flight = nil
			c.mu.Unlock()
			close(flight)
		}()
	}
	flight := c.flight
	c.mu.Unlock()

	timer := time.NewTimer(c.timeout)
	defer timer.Stop()
	select {
	case <-flight:
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.lastErr != nil {
			var zero T
			return zero, c.lastErr
		}
		return c.last, nil
	case <-timer.C:
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.have {
			return c.last, nil
		}
		var zero T
		return zero, fmt.Errorf("collector %s timed out after %s", c.name, c.timeout)
	}
}

// opt3 carries a (value, present) pair through newBounded for the collectors
// that report "no series" with a false flag.
type opt3[T any] struct {
	v  T
	ok bool
}

func newBounded3[T any](name string, fn func() (T, bool, error)) func() (T, bool, error) {
	b := newBounded(name, func() (opt3[T], error) {
		v, ok, err := fn()
		return opt3[T]{v, ok}, err
	})
	return func() (T, bool, error) {
		r, err := b()
		return r.v, r.ok, err
	}
}
