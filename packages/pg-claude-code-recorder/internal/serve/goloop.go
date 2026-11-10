package serve

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// errDaemonClosing is the cancellation cause Close gives the root context.
var errDaemonClosing = errors.New("daemon closing")

// loopRun is one goroutine started through goLoop.
type loopRun struct {
	name  string
	since time.Time
}

// LoopFlight is a goLoop goroutine that is still running: its name and how long
// ago it started.
type LoopFlight struct {
	Name string
	Age  time.Duration
}

// goroutines is the daemon's goroutine lifecycle: an errgroup that Close waits
// on, a closing flag that Close sets (under mu) before it waits, and the set of
// goroutines still running. The errgroup alone does not close the race between
// a late Go call and Wait; the flag does, because goLoop checks it under the
// same mutex Close takes.
//
// Lock order: Daemon.forceMu, then goroutines.mu (opSweep holds forceMu while
// it calls goLoop). Nothing that holds mu takes another daemon lock.
type goroutines struct {
	group   errgroup.Group
	mu      sync.Mutex
	closing bool
	seq     uint64
	running map[uint64]loopRun
}

// rootCtx is the daemon's root context. A Daemon built as a struct literal
// without Open has none; it then gets a context that is never cancelled.
func (d *Daemon) rootCtx() context.Context {
	if d.ctx == nil {
		return context.Background()
	}
	return d.ctx
}

// goLoop runs fn in a goroutine that Close waits for, and records its name and
// start time while it runs (inFlight reads them without any loopTracker lock,
// so a loop blocked outside runLoop is still visible). It returns false, and
// starts nothing, once Close has begun.
func (d *Daemon) goLoop(name string, fn func()) bool {
	g := &d.gor
	g.mu.Lock()
	if g.closing {
		g.mu.Unlock()
		return false
	}
	g.seq++
	id := g.seq
	if g.running == nil {
		g.running = map[uint64]loopRun{}
	}
	g.running[id] = loopRun{name: name, since: d.clock.Now()}
	g.group.Go(func() error {
		defer func() {
			g.mu.Lock()
			delete(g.running, id)
			g.mu.Unlock()
		}()
		fn()
		return nil
	})
	g.mu.Unlock()
	return true
}

// beginClosing marks the daemon closing, so goLoop and opSweep refuse new work.
func (d *Daemon) beginClosing() {
	d.gor.mu.Lock()
	d.gor.closing = true
	d.gor.mu.Unlock()
}

// waitLoops blocks until every goLoop goroutine has returned.
func (d *Daemon) waitLoops() { _ = d.gor.group.Wait() }

// inFlight lists the goLoop goroutines still running, longest first.
func (d *Daemon) inFlight() []LoopFlight {
	g := &d.gor
	g.mu.Lock()
	runs := make([]loopRun, 0, len(g.running))
	for _, r := range g.running {
		runs = append(runs, r)
	}
	g.mu.Unlock()
	now := d.clock.Now()
	out := make([]LoopFlight, 0, len(runs))
	for _, r := range runs {
		out = append(out, LoopFlight{Name: r.name, Age: now.Sub(r.since)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Age != out[j].Age {
			return out[i].Age > out[j].Age
		}
		return out[i].Name < out[j].Name
	})
	return out
}
