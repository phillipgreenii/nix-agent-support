package collect

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/bd"
)

// fakeClock is a manually advanced clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeAdapter is a recording bd.Adapter with scripted results.
type fakeAdapter struct {
	mu sync.Mutex

	calls []string

	list     []bd.Bead
	ready    []bd.Bead
	blocked  []bd.Bead
	counts   map[string]int
	statuses []string

	// spawnReady maps a joined queue argument list to the result of a
	// spawn-only bd ready call.
	spawnReady map[string][]bd.Bead
	// created and closed are the results of the two throughput list calls.
	created []bd.Bead
	closed  []bd.Bead

	// fail maps a call name to an error returned by it.
	fail map[string]error
	// onCall, when set, runs at the start of every call (to advance a clock or
	// block).
	onCall func(name string) error

	// lastListOpts records the options of the last list calls.
	listOpts []bd.ListOpts
}

func (a *fakeAdapter) record(name string) error {
	a.mu.Lock()
	a.calls = append(a.calls, name)
	hook := a.onCall
	err := a.fail[name]
	a.mu.Unlock()
	if hook != nil {
		if herr := hook(name); herr != nil {
			return herr
		}
	}
	return err
}

func (a *fakeAdapter) callNames() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string{}, a.calls...)
}

func (a *fakeAdapter) resetCalls() {
	a.mu.Lock()
	a.calls = nil
	a.listOpts = nil
	a.mu.Unlock()
}

func (a *fakeAdapter) List(_ context.Context, o bd.ListOpts) ([]bd.Bead, error) {
	name := "list"
	if o.All {
		name = "list-all"
	}
	a.mu.Lock()
	a.listOpts = append(a.listOpts, o)
	a.mu.Unlock()
	if err := a.record(name); err != nil {
		return nil, err
	}
	switch {
	case !o.CreatedAfter.IsZero():
		return a.created, nil
	case !o.ClosedAfter.IsZero():
		return a.closed, nil
	}
	return a.list, nil
}

func (a *fakeAdapter) Ready(_ context.Context, extra []string) ([]bd.Bead, error) {
	name := "ready"
	if len(extra) > 0 {
		name = "ready:" + strings.Join(extra, " ")
	}
	if err := a.record(name); err != nil {
		return nil, err
	}
	if len(extra) > 0 {
		return a.spawnReady[strings.Join(extra, " ")], nil
	}
	return a.ready, nil
}

func (a *fakeAdapter) Blocked(context.Context) ([]bd.Bead, error) {
	if err := a.record("blocked"); err != nil {
		return nil, err
	}
	return a.blocked, nil
}

func (a *fakeAdapter) CountByStatus(context.Context) (map[string]int, error) {
	if err := a.record("count"); err != nil {
		return nil, err
	}
	return a.counts, nil
}

func (a *fakeAdapter) Statuses(context.Context) ([]string, error) {
	if err := a.record("statuses"); err != nil {
		return nil, err
	}
	return a.statuses, nil
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func ptr(t time.Time) *time.Time { return &t }

func mkBead(id, status, typ string, prio int, created time.Time, labels ...string) bd.Bead {
	return bd.Bead{ID: id, Status: status, IssueType: typ, Priority: prio, CreatedAt: ptr(created), Labels: labels}
}

func errf(format string, a ...any) error { return fmt.Errorf(format, a...) }
