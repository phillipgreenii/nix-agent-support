package session

import (
	"context"
	"errors"
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

// A dead row whose recorder probe errors trips the pass's availability gate: it
// and every later unresolved row are kept (never pruned on a guess), the sweep
// is not failed, and the rows the stat already settled are unaffected.
func TestReap_probeErrorKeepsUnresolvedRowsWithoutFailingSweep(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	e := deadFixture(t, now,
		deadRow{id: "a-bad", state: store.Idle, ageSec: 600},
		deadRow{id: "b-unasked", state: store.Idle, ageSec: 600})
	ex := &perPathExister{
		errs: map[string]error{"/p/a-bad.jsonl": errors.New("daemon offline")},
	}
	e.s.d.Exister = ex

	if err := e.s.Reap(context.Background(), 6, time.Hour); err != nil {
		t.Fatalf("Reap = %v; a probe outage is one WARN, not a sweep failure", err)
	}
	if !e.exists(t, "a-bad") || !e.exists(t, "b-unasked") {
		t.Error("a row left unresolved by the outage was pruned")
	}
	if len(ex.asked) != 1 {
		t.Errorf("recorder asked %d times (%v); the first failure must stop the probing", len(ex.asked), ex.asked)
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
