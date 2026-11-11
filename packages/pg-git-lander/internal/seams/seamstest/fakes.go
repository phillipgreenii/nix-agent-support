// Package seamstest holds the unit-test fakes of the seams package. Runner's
// fake is x/cmdruntest.Fake.
package seamstest

import (
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
)

// Clock is an injected clock that tests advance explicitly.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a Clock reading start.
func NewClock(start time.Time) *Clock { return &Clock{now: start} }

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *Clock) Since(t time.Time) time.Duration { return c.Now().Sub(t) }

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// Liveness is a fake that knows which pids are alive and their start times.
type Liveness struct {
	mu    sync.Mutex
	procs map[int]string
}

// NewLiveness returns a Liveness with no live processes.
func NewLiveness() *Liveness { return &Liveness{procs: map[int]string{}} }

// SetAlive marks pid alive with the given start time.
func (l *Liveness) SetAlive(pid int, startTime string) {
	l.mu.Lock()
	l.procs[pid] = startTime
	l.mu.Unlock()
}

// SetDead marks pid dead.
func (l *Liveness) SetDead(pid int) {
	l.mu.Lock()
	delete(l.procs, pid)
	l.mu.Unlock()
}

func (l *Liveness) StartTime(pid int) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.procs[pid]
	if !ok {
		return "", errNoProcess
	}
	return st, nil
}

func (l *Liveness) Alive(pid int, startTime string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.procs[pid]
	return ok && (startTime == "" || st == startTime)
}

type noProcess struct{}

func (noProcess) Error() string { return "no such process" }

var errNoProcess error = noProcess{}

// ConfigPaths is a fixture directory.
type ConfigPaths string

func (c ConfigPaths) Dir() string { return string(c) }

// Host is a fixed name, or a foreign one in lock tests.
type Host string

func (h Host) Name() string { return string(h) }

var (
	_ seams.Clock       = (*Clock)(nil)
	_ seams.Liveness    = (*Liveness)(nil)
	_ seams.ConfigPaths = ConfigPaths("")
	_ seams.Host        = Host("")
)
