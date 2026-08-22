package eventqueue

import (
	"slices"
	"testing"
	"time"
)

// --- INV-CONC-1 serialize marks (pg2-cl9jz, DEC-CONC-1) -------------------
//
// These tests cover the per-type occupancy gate WithSerializeTypes adds to
// headFor: a marked type's successor event must reach NO listener at all —
// not merely be withheld from the listener holding the occupant, which the
// existing per-listener FIFO (TestPerListenerCursorIndependence) already
// guarantees on its own — until the occupant is RELEASED (settled for every
// currently-bound listener that matches it).

// TestSerializeMarkWithholdsSuccessorFromEveryListener proves cross-handler
// mutual exclusion: while e1 (the occupant) remains unreleased because ONE of
// its two bound listeners has not yet settled it, e2 — the same marked type's
// successor — is offered to NEITHER listener, including the one (fast) that
// already settled e1 and would otherwise be free to move on to e2 under
// ordinary per-listener FIFO.
func TestSerializeMarkWithholdsSuccessorFromEveryListener(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk, WithSerializeTypes("shutdown"))
	fast := newListener("fast", "shutdown")
	slow := newListener("slow", "shutdown")
	slow.busyRemaining["e1"] = 1000 // stays busy on e1 across every pass this test drives
	q.Register(fast)
	q.Register(slow)
	mustEnqueue(t, q, evtUntil("e1", "shutdown", clk.in(time.Hour)))
	mustEnqueue(t, q, evtUntil("e2", "shutdown", clk.in(time.Hour)))

	q.Dispatch() // fast accepts e1; slow declines (busy) — e1 remains unreleased
	if !equal(fast.accepted, []string{"e1"}) {
		t.Fatalf("fast accepted = %v, want [e1]", fast.accepted)
	}
	if len(slow.accepted) != 0 {
		t.Fatalf("slow accepted = %v, want none yet (still busy on the occupant)", slow.accepted)
	}

	// Further passes — advancing the clock past the retry cadence each time so
	// slow is genuinely re-offered its head — must never reach e2 for EITHER
	// listener while e1 remains unreleased.
	for range 3 {
		clk.advance(3 * time.Minute)
		q.Dispatch()
	}
	if slices.Contains(fast.offered, "e2") {
		t.Fatalf("fast offered = %v, must not include e2 while e1 is unreleased", fast.offered)
	}
	if slices.Contains(slow.offered, "e2") {
		t.Fatalf("slow offered = %v, must not include e2 while e1 is unreleased", slow.offered)
	}

	// Release the occupant: slow finally accepts e1.
	slow.busyRemaining["e1"] = 0
	clk.advance(3 * time.Minute)
	q.Dispatch()
	if !equal(slow.accepted, []string{"e1"}) {
		t.Fatalf("slow accepted = %v, want [e1]", slow.accepted)
	}

	// e2 is now the occupant and must reach both listeners.
	q.Dispatch()
	if !slices.Contains(fast.offered, "e2") {
		t.Fatalf("fast never offered e2 once the occupant e1 released")
	}
	if !slices.Contains(slow.offered, "e2") {
		t.Fatalf("slow never offered e2 once the occupant e1 released")
	}
}

// TestUnmarkedTypeUnaffectedBySerializeMarks proves the occupancy gate is
// opt-in per TYPE: with "shutdown" marked to serialize, an entirely
// different, UNMARKED type's events reach every matching listener exactly as
// before — each listener's own per-listener FIFO is the only gate that
// applies, never a type-wide occupancy check.
func TestUnmarkedTypeUnaffectedBySerializeMarks(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk, WithSerializeTypes("shutdown")) // "T" below is NOT marked
	a := newListener("a", "T")
	b := newListener("b", "T")
	q.Register(a)
	q.Register(b)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))
	mustEnqueue(t, q, evtUntil("e2", "T", clk.in(time.Hour)))

	q.Dispatch() // both listeners accept e1 immediately
	q.Dispatch() // e2 must be reachable right away — no gate to release first

	if !equal(a.accepted, []string{"e1", "e2"}) {
		t.Fatalf("a accepted = %v, want [e1 e2] (unmarked type unaffected)", a.accepted)
	}
	if !equal(b.accepted, []string{"e1", "e2"}) {
		t.Fatalf("b accepted = %v, want [e1 e2] (unmarked type unaffected)", b.accepted)
	}
}

// TestSerializeReleaseIsSettlementNotEviction proves release means SETTLED
// (accepted by every currently-bound listener), not evicted/retired: e1 stays
// RETAINED (its own expiresAt is still an hour out and WithEarlyEviction is
// not set, ADR 0031) after its one listener accepts it, yet the type's next
// event must be offered immediately regardless — gating release on
// retirement instead would leave e2 blocked for the rest of e1's retention
// window even though the only bound handler already took it, which
// INV-CONC-1's "completes ... " documents as the rejected reading.
func TestSerializeReleaseIsSettlementNotEviction(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk, WithSerializeTypes("shutdown"))
	l := newListener("h", "shutdown")
	q.Register(l)
	mustEnqueue(t, q, evtUntil("e1", "shutdown", clk.in(time.Hour)))
	mustEnqueue(t, q, evtUntil("e2", "shutdown", clk.in(time.Hour)))

	q.Dispatch() // l accepts e1
	if !equal(l.accepted, []string{"e1"}) {
		t.Fatalf("accepted = %v, want [e1]", l.accepted)
	}
	// e1 remains RETAINED (no early eviction, and its own expiresAt is an hour
	// out) — confirm it, so the next check is genuinely about occupancy and
	// not a side effect of e1 having already left the queue.
	if d := q.DepthByType()["shutdown"]; d != 2 {
		t.Fatalf("DepthByType = %d, want 2 (e1 retained + e2 pending)", d)
	}

	q.Dispatch() // e2 must now be the occupant and reach l immediately
	if !equal(l.accepted, []string{"e1", "e2"}) {
		t.Fatalf("accepted = %v, want [e1 e2] (e2 offered right after e1 settled, though still retained)", l.accepted)
	}
}

// TestIdleReflectsWithheldSerializedSuccessor confirms Idle() still correctly
// reports "not idle" while a serialize-marked successor is withheld — it goes
// through headFor exactly like any other pending delivery, so no separate
// occupancy-awareness is needed in Idle() itself.
func TestIdleReflectsWithheldSerializedSuccessor(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk, WithSerializeTypes("shutdown"))
	l := newListener("h", "shutdown")
	q.Register(l)
	mustEnqueue(t, q, evtUntil("e1", "shutdown", clk.in(time.Hour)))
	mustEnqueue(t, q, evtUntil("e2", "shutdown", clk.in(time.Hour)))

	if q.Idle() {
		t.Fatal("Idle() = true before anything was even offered")
	}
	q.Dispatch() // l accepts e1; e2 becomes the new occupant, still owed to l
	if q.Idle() {
		t.Fatal("Idle() = true while e2 (the new occupant) is still owed to l")
	}
	q.Dispatch() // l accepts e2
	if !q.Idle() {
		t.Fatal("Idle() = false after both events settled and the queue drained")
	}
}

// idFilterListener matches every event of a given type EXCEPT one excluded
// id. It is the simplest way to construct, with a single registered listener,
// an "orphan" entry of a marked type (no bound listener matches it) followed
// by a LATER, reachable entry of the SAME type — fakeListener's Matches is
// purely type-keyed and cannot express that distinction on its own.
type idFilterListener struct {
	id        string
	typ       string
	excludeID string
	accepted  []string
}

func (l *idFilterListener) ID() string           { return l.id }
func (l *idFilterListener) Matches(e Event) bool { return e.Type == l.typ && e.ID != l.excludeID }
func (l *idFilterListener) Offer(e Event) bool {
	l.accepted = append(l.accepted, e.ID)
	return true
}

// TestSerializeMarkOrphanEntryIsVacuouslyReleased confirms an orphan entry of
// a marked type (no currently-bound listener matches it) never occupies the
// slot — mirroring retainedLocked's own vacuous-retention reading for the
// same case — so it must not block a LATER, listener-reachable entry of the
// same type.
func TestSerializeMarkOrphanEntryIsVacuouslyReleased(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk, WithSerializeTypes("shutdown"))
	l := &idFilterListener{id: "h", typ: "shutdown", excludeID: "orphan"}
	q.Register(l)
	mustEnqueue(t, q, evtUntil("orphan", "shutdown", clk.in(time.Hour)))    // no listener matches this one
	mustEnqueue(t, q, evtUntil("reachable", "shutdown", clk.in(time.Hour))) // l matches this one

	q.Dispatch()
	if !equal(l.accepted, []string{"reachable"}) {
		t.Fatalf("accepted = %v, want [reachable] (an orphan entry of a marked type must not occupy the slot)", l.accepted)
	}
}
