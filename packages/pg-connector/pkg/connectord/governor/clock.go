package governor

import (
	"sync"
	"time"
)

// Clock is the governor's time source. Production passes SystemClock; tests
// pass a ManualClock so windows, pauses and a long downtime are simulated
// without sleeping.
type Clock interface {
	Now() time.Time
}

// SystemClock is the wall clock.
type SystemClock struct{}

// Now returns time.Now().
func (SystemClock) Now() time.Time { return time.Now() }

// ManualClock is a Clock that moves only when told to. It is safe for
// concurrent use and is exported so the packages that build on the governor
// (the scheduler, the query runner) can drive one clock through a whole test.
type ManualClock struct {
	mu sync.Mutex
	t  time.Time
}

// NewManualClock returns a ManualClock reading t.
func NewManualClock(t time.Time) *ManualClock { return &ManualClock{t: t} }

// Now returns the clock's current reading.
func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance moves the clock forward by d (backward for a negative d).
func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// Set moves the clock to t.
func (c *ManualClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}
