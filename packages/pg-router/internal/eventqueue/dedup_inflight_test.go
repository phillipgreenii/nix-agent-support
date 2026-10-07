package eventqueue

import (
	"testing"
	"time"
)

// Per-change ids (pg-router-source-pg-connector's "changes --retry-window",
// bead pg2-1ldvy) rely on the queue treating the id as the whole dedup key: two
// pr.changed events for ONE PR whose ids differ only by the "@<digest>" suffix
// are distinct events and are both admitted while the first is still in
// flight, whereas the identical id twice is a retained duplicate (INV-EVT-3).
func TestEnqueue_PerChangeSuffixedIDsAdmittedWhileFirstInFlight_IdenticalIDDeduped(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	l := newKickSequentialListener("desk-pr", "pr.changed")
	q.Register(l)

	window := clk.in(time.Hour)
	first := evtUntil("acme/widgets#7@aaaaaaaaaaaa", "pr.changed", window)
	if r := mustEnqueue(t, q, first); r != Enqueued {
		t.Fatalf("first suffixed event = %v, want Enqueued", r)
	}

	entered, release := l.arm()
	t.Cleanup(release)
	if launched := q.Kick(); launched != 1 {
		t.Fatalf("Kick launched %d offers, want 1 (the first event)", launched)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("listener never entered its offer for the first event (timeout)")
	}
	if got := q.SessionsInFlight(); got != 1 {
		t.Fatalf("SessionsInFlight = %d, want 1: the first event must be in flight", got)
	}

	// Same PR, a different change (different suffix): a NEW event.
	second := evtUntil("acme/widgets#7@bbbbbbbbbbbb", "pr.changed", window)
	if r := mustEnqueue(t, q, second); r != Enqueued {
		t.Fatalf("different-suffix event while first in flight = %v, want Enqueued", r)
	}
	// The identical ids, again, are retained duplicates, in-flight or not.
	if r := mustEnqueue(t, q, first); r != Deduped {
		t.Fatalf("identical id of the in-flight event = %v, want Deduped", r)
	}
	if r := mustEnqueue(t, q, second); r != Deduped {
		t.Fatalf("identical id of the queued event = %v, want Deduped", r)
	}
	if got := q.DepthByType()["pr.changed"]; got != 2 {
		t.Fatalf("depth of pr.changed = %d, want 2 (both suffixed events retained, no duplicates)", got)
	}
}
