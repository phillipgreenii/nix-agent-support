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
	// After returns a channel that receives once d has passed on this clock.
	After(d time.Duration) <-chan time.Time
}

// Real is the production clock.
type Real struct{}

// Now returns the wall-clock time.
func (Real) Now() time.Time { return time.Now() }

// Sleep sleeps for d.
func (Real) Sleep(d time.Duration) { time.Sleep(d) }

// After waits on the wall clock.
func (Real) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Fake is a manually advanced clock. Sleep advances it instead of blocking.
type Fake struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeTimer
}

type fakeTimer struct {
	at time.Time
	ch chan time.Time
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
	keep := f.timers[:0]
	for _, t := range f.timers {
		if !t.at.After(f.now) {
			t.ch <- f.now
			continue
		}
		keep = append(keep, t)
	}
	f.timers = keep
	f.mu.Unlock()
}

// After returns a channel that fires when Advance moves the clock to or past
// now+d.
func (f *Fake) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- f.now
		return ch
	}
	f.timers = append(f.timers, fakeTimer{at: f.now.Add(d), ch: ch})
	return ch
}

// Waiters reports how many After timers are pending (tests wait for a waiter
// to arm its timer before advancing).
func (f *Fake) Waiters() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.timers)
}
