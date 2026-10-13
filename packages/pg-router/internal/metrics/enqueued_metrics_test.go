package metrics

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
)

// pg_router_enqueued_total is the arrivals half of the pg-router-queue-slow-growth
// alert (arrivals versus completions, internal/alertrules). The tests below pin
// what counts as an arrival, because the alert's ratio is only as good as that
// definition: a fresh admission counts; a duplicate, a restore, a refusal and the
// Gate Registry's control-plane records do not.

func enqueuedFor(t *testing.T, h *harness, typ string) int64 {
	t.Helper()
	for _, sm := range h.collect(t).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == MetricEnqueued {
				return sumFor(m, "type", typ)
			}
		}
	}
	return -1
}

func TestEnqueuedCountsFreshArrivalsPerType(t *testing.T) {
	h := newHarness(t)
	h.enqueue(t, "e1", "drain.alpha", time.Hour)
	h.enqueue(t, "e2", "drain.alpha", time.Hour)
	h.enqueue(t, "e3", "pr.changed", time.Hour)

	if got := enqueuedFor(t, h, "drain.alpha"); got != 2 {
		t.Errorf("enqueued[drain.alpha] = %d, want 2", got)
	}
	if got := enqueuedFor(t, h, "pr.changed"); got != 1 {
		t.Errorf("enqueued[pr.changed] = %d, want 1", got)
	}
}

// A re-emit of a still-retained id is dropped as a duplicate (Deduped), so it is
// not a new arrival: counting it would inflate arrivals against completions for a
// source that re-emits its whole result set on every poll.
func TestEnqueuedIgnoresDuplicateReEmit(t *testing.T) {
	h := newHarness(t)
	h.enqueue(t, "e1", "drain.alpha", time.Hour)
	h.enqueue(t, "e1", "drain.alpha", time.Hour)
	h.enqueue(t, "e1", "drain.alpha", time.Hour)

	if got := enqueuedFor(t, h, "drain.alpha"); got != 1 {
		t.Fatalf("enqueued[drain.alpha] = %d after three emits of one id, want 1", got)
	}
}

// An id whose retention is over is retired; a source that emits it again creates a
// FRESH event, which the queue admits and the metric counts (it is an arrival as
// far as the queue is concerned; see the alert's comment on re-emit churn).
func TestEnqueuedCountsReEmitAfterRetirement(t *testing.T) {
	h := newHarness(t)
	h.enqueue(t, "e1", "drain.alpha", time.Minute)
	h.clk.advance(2 * time.Minute)
	h.q.Expire()
	h.enqueue(t, "e1", "drain.alpha", time.Hour)

	if got := enqueuedFor(t, h, "drain.alpha"); got != 2 {
		t.Fatalf("enqueued[drain.alpha] = %d, want 2 (the original and the post-retirement re-emit)", got)
	}
}

// A restored event was counted by the process that first enqueued it, so a daemon
// restart must not look like a burst of arrivals.
func TestEnqueuedIgnoresRestoredEvents(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnRestore(eventqueue.Event{ID: "r1", Type: "drain.alpha", At: h.clk.now()})

	if got := enqueuedFor(t, h, "drain.alpha"); got != -1 {
		t.Fatalf("enqueued[drain.alpha] = %d after a restore alone, want no series", got)
	}
}

// The Gate Registry's routable records (gate.set, gate.cleared, gate.expired) are
// control-plane notifications, not work.
func TestEnqueuedIgnoresGateRecords(t *testing.T) {
	h := newHarness(t)
	for _, typ := range []string{eventqueue.GateEventSet, eventqueue.GateEventCleared, eventqueue.GateEventExpired} {
		h.emitter.OnEnqueue(eventqueue.Event{ID: "g-" + typ, Type: typ, At: h.clk.now()})
		if got := enqueuedFor(t, h, typ); got != -1 {
			t.Errorf("enqueued[%s] = %d, want no series for a gate record", typ, got)
		}
	}
}
