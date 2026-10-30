// Package clock is the recorder's time seam. Every age decision takes a Clock;
// no recorder code calls time.Now directly.
package clock

import (
	"sync"
	"time"
)

// Clock supplies the time and sleeping.
type Clock interface {
	Now() time.Time
	Sleep(d time.Duration)
}

// Real is the production clock.
type Real struct{}

// Now returns the wall-clock time.
func (Real) Now() time.Time { return time.Now() }

// Sleep sleeps for d.
func (Real) Sleep(d time.Duration) { time.Sleep(d) }

// Fake is a manually advanced clock. Sleep advances it instead of blocking.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake returns a Fake starting at t.
func NewFake(t time.Time) *Fake { return &Fake{now: t} }

// Now returns the fake time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Sleep advances the fake time by d without blocking.
func (f *Fake) Sleep(d time.Duration) { f.Advance(d) }

// Advance moves the fake time forward by d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}
