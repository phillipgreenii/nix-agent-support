package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
)

// A moot dispatch the handler accepted and completed as skipped is a completion
// the queue settles on its first offer, but it is not throughput: the
// pg-router-queue-stalled rule reads a throughput increase as "something was
// processed", and a stream of no-op completions must not make a stuck queue look
// like it is progressing. These tests pin the counting: skipped completions land
// on pg_router_skipped_total (type, role, reason) and never on
// pg_router_throughput_total, while a real completion on the same lane still does.

// skippingListener accepts every event of typ and, like orchestrator.roleListener,
// reports it to the SkippedObserver inline before returning the accepted result.
type skippingListener struct {
	id, typ, reason string
	obs             *Emitter
}

func (l skippingListener) ID() string                      { return l.id }
func (l skippingListener) Matches(e eventqueue.Event) bool { return e.Type == l.typ }
func (l skippingListener) Offer(o eventqueue.Offering) eventqueue.OfferResult {
	l.obs.OnSkipped(o.Event.ID, o.Event.Type, l.id, l.reason)
	return eventqueue.OfferResult{Accepted: true, Decline: eventqueue.DeclineNone}
}

func counterFor(t *testing.T, h *harness, name, typ string) int64 {
	t.Helper()
	for _, sm := range h.collect(t).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return sumFor(m, "type", typ)
			}
		}
	}
	return -1
}

func TestSkippedCompletionIsNotThroughput(t *testing.T) {
	h := newHarness(t)
	h.q.Register(skippingListener{id: "triager", typ: "escalated.pg2", reason: "skipped-bead-closed", obs: h.emitter})
	h.enqueue(t, "e1", "escalated.pg2", time.Hour)
	h.enqueue(t, "e2", "escalated.pg2", time.Hour)

	h.q.Dispatch()
	h.q.Dispatch()

	if got := counterFor(t, h, MetricThroughput, "escalated.pg2"); got > 0 {
		t.Fatalf("throughput[escalated.pg2] = %d after two skipped completions, want none: a no-op must not read as progress", got)
	}
	m := findMetric(t, h.collect(t), MetricSkipped)
	if got := sumForBoth(m, "type", "escalated.pg2", "reason", "skipped-bead-closed"); got != 2 {
		t.Fatalf("skipped[escalated.pg2,skipped-bead-closed] = %d, want 2", got)
	}
	if got := sumForBoth(m, "type", "escalated.pg2", "role", "triager"); got != 2 {
		t.Fatalf("skipped[escalated.pg2,role=triager] = %d, want 2", got)
	}
}

// A real completion on the same emitter still counts as throughput, and a skip of
// one event does not swallow the throughput of another: the mark is per
// (event, listener).
func TestSkippedMarkIsPerEventAndListener(t *testing.T) {
	h := newHarness(t)
	h.q.Register(skippingListener{id: "triager", typ: "escalated.pg2", reason: "skipped-bead-closed", obs: h.emitter})
	h.q.Register(namedAcceptingListener{id: "worker", typ: "work.ready"})
	h.enqueue(t, "e1", "escalated.pg2", time.Hour)
	h.enqueue(t, "e2", "work.ready", time.Hour)

	h.q.Dispatch()

	if got := counterFor(t, h, MetricThroughput, "work.ready"); got != 1 {
		t.Fatalf("throughput[work.ready] = %d, want 1: a real completion still counts", got)
	}
	if got := counterFor(t, h, MetricThroughput, "escalated.pg2"); got > 0 {
		t.Fatalf("throughput[escalated.pg2] = %d, want none", got)
	}
}

// The same listener accepting the same event id twice in two passes (an id re-used
// after retirement) is counted by what each accept was: the first skipped, the
// second a real completion. The mark of the first is consumed by its OnAccept.
func TestSkippedMarkIsConsumedByItsAccept(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnEnqueue(eventqueue.Event{ID: "e1", Type: "escalated.pg2"})
	h.emitter.OnSkipped("e1", "escalated.pg2", "triager", "skipped-bead-closed")
	h.emitter.OnAccept("e1", "triager")
	h.emitter.OnAccept("e1", "triager")

	if got := counterFor(t, h, MetricThroughput, "escalated.pg2"); got != 1 {
		t.Fatalf("throughput[escalated.pg2] = %d, want 1 (only the unmarked accept)", got)
	}
	if got := counterFor(t, h, MetricSkipped, "escalated.pg2"); got != 1 {
		t.Fatalf("skipped[escalated.pg2] = %d, want 1", got)
	}
}

// The skipped counter's reason label is bounded: a tag that is not a short
// identifier, and every distinct tag past the cap, folds into one value.
func TestSkippedReasonLabelIsBounded(t *testing.T) {
	h := newHarness(t)
	h.emitter.OnSkipped("e0", "t", "r", "Not A Tag!")
	h.emitter.OnSkipped("e1", "t", "r", strings.Repeat("a", maxEventTypeLen+1))
	for i := 0; i < maxSkipReasonLabels+5; i++ {
		h.emitter.OnSkipped("e", "t", "r", "reason-"+string(rune('a'+i)))
	}
	m := findMetric(t, h.collect(t), MetricSkipped)
	if got := sumForBoth(m, "type", "t", "reason", SkipReasonOther); got != 2+5 {
		t.Fatalf("skipped[reason=%s] = %d, want 7 (two malformed tags and five past the cap)", SkipReasonOther, got)
	}
	if got := sumForBoth(m, "type", "t", "reason", "reason-a"); got != 1 {
		t.Fatalf("skipped[reason=reason-a] = %d, want 1", got)
	}
}
