package httpapi

import (
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// collectorTimeout bounds how long one collector of the /metrics catalog may
// hold a snapshot build. Variable only so a test can shorten it.
var collectorTimeout = 10 * time.Second

// collectorDuration is the wall time of each bounded collector call, labelled
// by the collector's fixed name (a small closed set, so the label stays
// bounded). It is observed inside the call's own goroutine, so a call that
// outlives collectorTimeout is still measured. The edges are dense between 2.5
// and 5 s, where the snapshot-build target sits. It is shared by every
// registry newMetricsRegistry builds.
var collectorDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "pg_desk_metrics_collector_duration_seconds",
	Help:    "Wall time of one bounded /metrics collector call, including calls that outlive the collector timeout.",
	Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 2.5, 3, 3.5, 4, 4.5, 5, 10, 30, 60},
}, []string{"collector"})

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
			began := time.Now()
			v, err := c.fn()
			collectorDuration.WithLabelValues(c.name).Observe(time.Since(began).Seconds())
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
