package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// perPathExister answers the recorder probe per transcript path: a path in
// errs fails, a path in present exists, anything else is gone.
type perPathExister struct {
	present map[string]bool
	errs    map[string]error
	asked   []string
	onAsk   func()
}

func (p *perPathExister) Exists(path string) bool { ok, _ := p.ExistsChecked(path); return ok }
func (p *perPathExister) ExistsChecked(path string) (bool, error) {
	p.asked = append(p.asked, path)
	if p.onAsk != nil {
		p.onAsk()
	}
	return p.present[path], p.errs[path]
}

// One dead row whose resumability check errors must not stop the sweep: the
// phantom beside it is still pruned, and Reap still reports the failure.
func TestReap_oneRowCheckErrorDoesNotAbortSweep(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	e := deadFixture(t, now,
		deadRow{id: "a-bad", state: store.Idle, ageSec: 600},
		deadRow{id: "b-phantom", state: store.Idle, ageSec: 600},
		deadRow{id: "c-kept", state: store.Idle, ageSec: 600})
	e.s.d.Exister = &perPathExister{
		present: map[string]bool{"/p/c-kept.jsonl": true},
		errs:    map[string]error{"/p/a-bad.jsonl": errors.New("daemon offline")},
	}

	err := e.s.Reap(context.Background(), 6, time.Hour)
	if err == nil {
		t.Fatal("Reap must report the failed row at the end, not return nil")
	}
	if !strings.Contains(err.Error(), "a-bad") || !strings.Contains(err.Error(), "daemon offline") {
		t.Errorf("error = %v; want it to name the failed row and its cause", err)
	}
	if e.exists(t, "b-phantom") {
		t.Error("phantom row after the failing one was not pruned")
	}
	if !e.exists(t, "a-bad") {
		t.Error("the row whose check failed must be left alone, never pruned on a guess")
	}
	if !e.exists(t, "c-kept") {
		t.Error("resumable row was removed")
	}
}

// Several failures are all reported, not just the first.
func TestReap_allRowFailuresAreJoined(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	e := deadFixture(t, now,
		deadRow{id: "x1", state: store.Idle, ageSec: 600},
		deadRow{id: "x2", state: store.Idle, ageSec: 600})
	boom := errors.New("daemon offline")
	e.s.d.Exister = &perPathExister{errs: map[string]error{"/p/x1.jsonl": boom, "/p/x2.jsonl": boom}}
	err := e.s.Reap(context.Background(), 6, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "x1") || !strings.Contains(err.Error(), "x2") {
		t.Fatalf("error = %v; want both failed rows named", err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("error = %v; want it to wrap the cause", err)
	}
}

// A cancelled context is not a per-row failure: the sweep aborts at once and the
// cancellation is returned.
func TestReap_contextCancelAbortsSweep(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	e := deadFixture(t, now,
		deadRow{id: "a", state: store.Idle, ageSec: 600},
		deadRow{id: "b", state: store.Idle, ageSec: 600},
		deadRow{id: "c", state: store.Idle, ageSec: 600})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ex := &perPathExister{errs: map[string]error{}}
	ex.onAsk = func() {
		cancel()
		for _, p := range ex.asked {
			ex.errs[p] = ctx.Err()
		}
	}
	e.s.d.Exister = ex
	err := e.s.Reap(ctx, 6, time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Reap = %v; want context.Canceled", err)
	}
	if len(ex.asked) != 1 {
		t.Errorf("rows probed = %d (%v); the sweep must stop at the cancellation", len(ex.asked), ex.asked)
	}
}
