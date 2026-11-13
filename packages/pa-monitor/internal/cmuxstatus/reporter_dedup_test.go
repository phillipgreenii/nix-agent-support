package cmuxstatus_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/cmuxstatus"
)

// dedupRun records cmux calls and fails set-status while failStatus is true.
type dedupRun struct {
	mu         sync.Mutex
	calls      []string
	failStatus bool
}

func (d *dedupRun) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, strings.Join(args, " "))
	if d.failStatus && len(args) > 0 && args[0] == "set-status" {
		return nil, errors.New("boom")
	}
	return nil, nil
}

func (d *dedupRun) count(sub string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.calls {
		if strings.HasPrefix(c, sub) {
			n++
		}
	}
	return n
}

func newDedup(async bool) (*dedupRun, cmuxstatus.Reporter) {
	d := &dedupRun{}
	return d, cmuxstatus.NewReporter(cmuxstatus.Options{
		Enable: true, RunCmd: d.run, LookupEnv: inCmuxEnv(), Async: async,
	})
}

func snap(p float64, label string) cmuxstatus.Snapshot {
	return cmuxstatus.Snapshot{State: cmuxstatus.StateWorking, HasProgress: true, Progress: p, ProgressLabel: label}
}

func TestDedupIdenticalSnapshotsPushOnce(t *testing.T) {
	d, r := newDedup(false)
	for i := 0; i < 10; i++ {
		r.Push(snap(0.5, "x"))
	}
	if s, p := d.count("set-status"), d.count("set-progress"); s != 1 || p != 1 {
		t.Fatalf("want 1/1 calls, got status=%d progress=%d", s, p)
	}
}

// Negative control: with the remembered state wiped before every Push (dedup
// effectively disabled) every snapshot pushes.
func TestDedupDisabledControlPushesEveryTime(t *testing.T) {
	d, r := newDedup(false)
	for i := 0; i < 10; i++ {
		r.Reset()
		r.Push(snap(0.5, "x"))
	}
	if s, p := d.count("set-status"), d.count("set-progress"); s != 10 || p != 10 {
		t.Fatalf("want 10/10 calls, got status=%d progress=%d", s, p)
	}
}

func TestDedupChangesStillPush(t *testing.T) {
	d, r := newDedup(false)
	r.Push(snap(0.50, "a"))
	r.Push(snap(0.50, "a")) // dup
	if d.count("set-status") != 1 || d.count("set-progress") != 1 {
		t.Fatalf("baseline wrong: %v", d.calls)
	}
	r.Push(cmuxstatus.Snapshot{State: cmuxstatus.StateIdle, HasProgress: true, Progress: 0.5, ProgressLabel: "a"})
	if d.count("set-status") != 2 || d.count("set-progress") != 1 {
		t.Fatalf("value change: %v", d.calls)
	}
	r.Push(cmuxstatus.Snapshot{State: cmuxstatus.StateIdle, HasProgress: true, Progress: 0.5, ProgressLabel: "b"})
	if d.count("set-progress") != 2 {
		t.Fatalf("label change: %v", d.calls)
	}
	r.Push(cmuxstatus.Snapshot{State: cmuxstatus.StateIdle, HasProgress: true, Progress: 0.51, ProgressLabel: "b"})
	if d.count("set-progress") != 3 {
		t.Fatalf("%%.2f boundary: %v", d.calls)
	}
	r.Push(cmuxstatus.Snapshot{State: cmuxstatus.StateIdle, HasProgress: true, Progress: 0.512, ProgressLabel: "b"})
	if d.count("set-progress") != 3 {
		t.Fatalf("equal at two decimals must not push: %v", d.calls)
	}
	for _, l := range []string{"3s", "2s", "1s"} {
		r.Push(cmuxstatus.Snapshot{State: cmuxstatus.StateIdle, HasProgress: true, Progress: 0.51, ProgressLabel: l})
	}
	if d.count("set-progress") != 6 {
		t.Fatalf("countdown label must push each tick: %v", d.calls)
	}
}

func TestDedupClampedProgressEqual(t *testing.T) {
	d, r := newDedup(false)
	r.Push(snap(1.0, ""))
	r.Push(snap(1.004, ""))
	r.Push(snap(7, ""))
	if d.count("set-progress") != 1 {
		t.Fatalf("clamped-equal progress must push once: %v", d.calls)
	}
	r.Push(snap(-3, ""))
	r.Push(snap(0, ""))
	if d.count("set-progress") != 2 {
		t.Fatalf("0 after 1 pushes once more, clamped negatives dedup: %v", d.calls)
	}
}

func TestDedupPausedResetAtNoiseDoesNotPush(t *testing.T) {
	d, r := newDedup(false)
	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	r.Push(cmuxstatus.Snapshot{State: cmuxstatus.StatePaused, PausedResetAt: base})
	r.Push(cmuxstatus.Snapshot{State: cmuxstatus.StatePaused, PausedResetAt: base.Add(time.Second)})
	if d.count("set-status") != 1 {
		t.Fatalf("sub-minute PausedResetAt noise must not push: %v", d.calls)
	}
}

func TestDedupResetAndClearForcePush(t *testing.T) {
	d, r := newDedup(false)
	r.Push(snap(0.5, "x"))
	r.Reset()
	r.Push(snap(0.5, "x"))
	if d.count("set-status") != 2 || d.count("set-progress") != 2 {
		t.Fatalf("after Reset: %v", d.calls)
	}
	r.Clear()
	r.Push(snap(0.5, "x"))
	if d.count("set-status") != 3 || d.count("set-progress") != 3 {
		t.Fatalf("after Clear: %v", d.calls)
	}
}

func TestDedupFailedStatusRetriedThenNotAfterSuccess(t *testing.T) {
	d, r := newDedup(false)
	d.failStatus = true
	r.Push(snap(0.5, "x"))
	r.Push(snap(0.5, "x"))
	if d.count("set-status") != 2 {
		t.Fatalf("failed set-status must retry: %v", d.calls)
	}
	d.mu.Lock()
	d.failStatus = false
	d.mu.Unlock()
	r.Push(snap(0.5, "x")) // succeeds
	r.Push(snap(0.5, "x")) // deduped
	if d.count("set-status") != 3 {
		t.Fatalf("no retry after success: %v", d.calls)
	}
}

func TestDedupThroughAsyncWrapper(t *testing.T) {
	d, r := newDedup(true)
	push := func() {
		// Async Push is latest-wins; wait for the worker to drain between pushes.
		r.Push(snap(0.5, "x"))
		time.Sleep(30 * time.Millisecond)
	}
	for i := 0; i < 5; i++ {
		push()
	}
	if d.count("set-status") != 1 || d.count("set-progress") != 1 {
		t.Fatalf("async dedup: %v", d.calls)
	}
	r.Reset()
	push()
	if d.count("set-status") != 2 || d.count("set-progress") != 2 {
		t.Fatalf("Reset must reach core through async: %v", d.calls)
	}
	r.Clear()
}
