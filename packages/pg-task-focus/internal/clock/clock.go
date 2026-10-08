// Package clock provides the injectable time source. Everything
// time-dependent in pg-task-focus takes a Clock so tests control time.
package clock

import (
	"sync"
	"time"
)

// Clock reports the current instant.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Real returns the clock backed by the system time.
func Real() Clock { return realClock{} }

// Fake is a settable, advanceable Clock for tests. It is safe for concurrent
// use.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake returns a Fake reading t.
func NewFake(t time.Time) *Fake { return &Fake{now: t} }

// Set moves the clock to t, forwards or backwards.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
}

// Advance moves the clock by d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// Now returns the clock's current instant.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}
