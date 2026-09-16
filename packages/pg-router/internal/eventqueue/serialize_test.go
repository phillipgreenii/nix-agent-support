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
func (l *idFilterListener) Offer(o Offering) OfferResult {
	l.accepted = append(l.accepted, o.Event.ID)
	return OfferResult{Accepted: true, Decline: DeclineNone}
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

// --- Task 6.3 (pg2-3brwx.3): the serialize mark becomes load-bearing -------
//
// The tests above prove INV-CONC-1's occupancy gate using DECLINE-based
// simulation (fakeListener's busyRemaining / idFilterListener), which never
// exercises a genuinely BLOCKED, concurrently-running Offer call — under the
// pre-Task-6.2 sequential phase 2, "no two same-type offers outstanding at
// once" held merely because a for-loop calling Offer one at a time can never
// have two offers in flight simultaneously to begin with, occupancy gate or
// not. Task 6.2 (bead pg2-3brwx.2) made phase 2 fan out one goroutine per
// pendingOffer, so that free "sequential dispatch already gives you this"
// guarantee is gone — headFor's occupancy gate (computed entirely in phase 1,
// under q.mu, BEFORE any phase-2 goroutine starts) is now the ONLY thing
// standing between a marked type's second event and a concurrently-launched
// Offer call for it.

// onlyIDBlockingListener matches exactly one (type, id) pair and blocks
// inside Offer until told to proceed. Matching a single id (not merely a
// type, as fakeListener does) is what lets two of these, registered
// together, each own a DIFFERENT event of the SAME marked type — the shape
// TestSerializeOccupancyWithholdsSuccessorFromConcurrentPhase2 needs so that
// one listener's still-outstanding, genuinely-blocked offer can be checked
// against the OTHER listener's offer never even starting.
type onlyIDBlockingListener struct {
	id      string
	typ     string
	onlyID  string
	entered chan struct{} // closed the instant Offer is invoked
	proceed chan struct{} // Offer blocks reading this until the test closes it
}

func newOnlyIDBlockingListener(id, typ, onlyID string) *onlyIDBlockingListener {
	return &onlyIDBlockingListener{
		id: id, typ: typ, onlyID: onlyID,
		entered: make(chan struct{}),
		proceed: make(chan struct{}),
	}
}

func (l *onlyIDBlockingListener) ID() string           { return l.id }
func (l *onlyIDBlockingListener) Matches(e Event) bool { return e.Type == l.typ && e.ID == l.onlyID }

func (l *onlyIDBlockingListener) Offer(Offering) OfferResult {
	close(l.entered)
	<-l.proceed
	return OfferResult{Accepted: true, Decline: DeclineNone}
}

// TestSerializeOccupancyWithholdsSuccessorFromConcurrentPhase2 is Task 6.3's
// proof test. Two listeners each match a DIFFERENT event of the same marked
// type, enqueued in order (e1, then e2); e1's listener blocks inside Offer.
// If headFor had minted a pendingOffer for e2's listener in this SAME pass,
// Task 6.2's goroutine-per-pendingOffer phase 2 would launch that Offer call
// immediately — genuinely concurrently with e1's listener's still-blocked
// one, not after it. This asserts that never happens: e2's listener is never
// even entered until e1's listener has settled e1 (INV-CONC-1's "same TYPE,
// different EVENTS never have outstanding offers simultaneously" — the two
// listeners here are never offered the SAME event, so the "concurrently
// offering one event to two listeners is fine" carve-out is untouched).
func TestSerializeOccupancyWithholdsSuccessorFromConcurrentPhase2(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk, WithSerializeTypes("shutdown"))
	l1 := newOnlyIDBlockingListener("l1", "shutdown", "e1")
	l2 := newOnlyIDBlockingListener("l2", "shutdown", "e2")
	q.Register(l1)
	q.Register(l2)
	mustEnqueue(t, q, evtUntil("e1", "shutdown", clk.in(time.Hour)))
	mustEnqueue(t, q, evtUntil("e2", "shutdown", clk.in(time.Hour)))

	dispatchDone := make(chan int, 1)
	go func() { dispatchDone <- q.Dispatch() }()

	select {
	case <-l1.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("l1 was never offered e1 (timeout)")
	}

	// e1 (the occupant) is still unreleased — l1's Offer is blocked inside
	// the running Dispatch call's phase 2. If e2's pendingOffer had been
	// minted in the same pass, its goroutine would enter l2.Offer right
	// away; assert it never does within a generous bounded wait.
	select {
	case <-l2.entered:
		t.Fatal("l2 was offered e2 while e1 (the occupant) was still unreleased — the serialize occupancy gate failed to withhold the successor from a concurrently-launched phase-2 offer")
	case <-time.After(200 * time.Millisecond):
		// expected: l2 has not been entered
	}

	close(l1.proceed)
	if accepted := <-dispatchDone; accepted != 1 {
		t.Fatalf("first Dispatch() accepted = %d, want 1 (only e1 this pass)", accepted)
	}

	// e1 is now released: a fresh pass must reach l2 for e2 immediately.
	close(l2.proceed) // pre-arm so this pass's Offer returns right away
	secondDone := make(chan int, 1)
	go func() { secondDone <- q.Dispatch() }()

	select {
	case <-l2.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("l2 was never offered e2 even after e1 released (timeout)")
	}
	select {
	case accepted := <-secondDone:
		if accepted != 1 {
			t.Fatalf("second Dispatch() accepted = %d, want 1 (e2, now the occupant)", accepted)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second Dispatch() never returned (timeout)")
	}
}
