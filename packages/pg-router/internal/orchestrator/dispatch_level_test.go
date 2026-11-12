package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

type fakeDispatchLevelObserver struct {
	levels [][2]string
	calls  []levelCall
}

type levelCall struct {
	role   string
	at     [][2]string
	ran    time.Duration
	failed bool
}

func (f *fakeDispatchLevelObserver) DispatchLevels() [][2]string { return f.levels }
func (f *fakeDispatchLevelObserver) OnDispatchAtLevel(role string, at [][2]string, ran time.Duration, failed bool) {
	f.calls = append(f.calls, levelCall{role, at, ran, failed})
}

// A finished dispatch is booked once against the levels snapshotted before the
// handler ran; success and failure differ only in failed. A retried attempt is
// not booked.
func TestOffer_DispatchLevelObserver_SnapshotAtStartOutcomeAtEnd(t *testing.T) {
	lobs := &fakeDispatchLevelObserver{levels: [][2]string{{"disk", "TIGHT"}}}
	o := newOrch(fastCfg(t), testQuerySet(nil, nil))
	o.DispatchLevelObserver = lobs
	role := roles.Role{Name: "desk-pr", Binds: []string{"pr.changed"}, MaxDispatchRetries: 1}
	l := o.NewListener(context.Background(), role)

	// Success.
	o.Handler = &seqHandler{}
	if got := l.Offer(eventqueue.Offering{ID: "d1", Event: retryEvent("e1")}); !got.Accepted {
		t.Fatalf("success must be accepted, got %+v", got)
	}
	// Transient failure (retried: not booked), then a deterministic failure.
	o.Handler = &seqHandler{errs: []error{killedErr, &wireclient.ExitError{Role: "desk-pr", Code: 1, Detail: "invalid_argument: bad"}}}
	if got := l.Offer(eventqueue.Offering{ID: "d2", Event: retryEvent("e2")}); got.Accepted {
		t.Fatalf("transient failure must be handed back, got %+v", got)
	}
	if len(lobs.calls) != 1 {
		t.Fatalf("a retried attempt was booked: %+v", lobs.calls)
	}
	lobs.levels = [][2]string{{"disk", "EXHAUSTED"}}
	if got := l.Offer(eventqueue.Offering{ID: "d2", Event: retryEvent("e2")}); !got.Accepted {
		t.Fatalf("deterministic failure ends accepted, got %+v", got)
	}
	if len(lobs.calls) != 2 {
		t.Fatalf("calls = %+v, want 2", lobs.calls)
	}
	c0, c1 := lobs.calls[0], lobs.calls[1]
	if c0.role != "desk-pr" || c0.failed || len(c0.at) != 1 || c0.at[0] != [2]string{"disk", "TIGHT"} {
		t.Errorf("success call = %+v", c0)
	}
	if !c1.failed || c1.at[0] != [2]string{"disk", "EXHAUSTED"} {
		t.Errorf("failure call = %+v", c1)
	}
}
