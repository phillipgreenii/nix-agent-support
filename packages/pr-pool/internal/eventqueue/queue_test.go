package eventqueue

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
)

// --- test doubles ---------------------------------------------------------

// mockClock is a deterministic, advanceable clock for expiry tests.
type mockClock struct{ t time.Time }

func newClock() *mockClock {
	return &mockClock{t: time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)}
}
func (c *mockClock) now() time.Time          { return c.t }
func (c *mockClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// in returns the absolute instant d from the clock's current now — the shape
// `expiresAt` takes, since expiry is an instant and never a duration
// (DEC-EVENT-1).
func (c *mockClock) in(d time.Duration) time.Time { return c.t.Add(d) }

// after is the WithSleeper wait seam paired with this mock clock: it advances
// virtual time by d and fires immediately (no real sleep), so RunUntilIdle
// makes progress toward `expiresAt` under the mock clock.
func (c *mockClock) after(d time.Duration) <-chan time.Time {
	c.advance(d)
	ch := make(chan time.Time, 1)
	ch <- c.t
	return ch
}

// fakeListener records every offer and can be configured to decline (busy) a
// given number of times per event id before accepting, or to never bind / never
// accept — enough to drive every INV-CONC-1 / INV-FAIL-1 / INV-EVT-4 branch.
type fakeListener struct {
	id            string
	binds         map[string]bool // event types this listener binds (INV-DISP-1)
	offered       []string        // event ids offered, in order (incl. re-offers)
	accepted      []string        // event ids accepted, in order
	busyRemaining map[string]int  // per-id busy declines before accepting
	neverAccept   bool
}

func newListener(id string, types ...string) *fakeListener {
	b := map[string]bool{}
	for _, t := range types {
		b[t] = true
	}
	return &fakeListener{id: id, binds: b, busyRemaining: map[string]int{}}
}

func (f *fakeListener) ID() string           { return f.id }
func (f *fakeListener) Matches(e Event) bool { return f.binds[e.Type] }
func (f *fakeListener) Offer(e Event) bool {
	f.offered = append(f.offered, e.ID)
	if f.neverAccept {
		return false
	}
	if n := f.busyRemaining[e.ID]; n > 0 {
		f.busyRemaining[e.ID] = n - 1
		return false
	}
	f.accepted = append(f.accepted, e.ID)
	return true
}

// recordingObserver captures Observer callbacks for assertions.
type recordingObserver struct {
	enqueued          []string
	accepted          []string
	unconsumedExpired []string
}

func (o *recordingObserver) OnEnqueue(e Event)       { o.enqueued = append(o.enqueued, e.ID) }
func (o *recordingObserver) OnAccept(id, lid string) { o.accepted = append(o.accepted, id+"/"+lid) }
func (o *recordingObserver) OnUnconsumedExpired(t string) {
	o.unconsumedExpired = append(o.unconsumedExpired, t)
}

// evt builds a DEFAULT event: neither `at` nor `expiresAt` set. The core resolves
// both to its own ingest-now, so the event is BORN EXPIRED (INV-EVT-4) — offered
// once to every matching listener, then dropped. This is the default shape on
// purpose; a test that needs a retry or de-dup WINDOW asks for one with evtUntil.
func evt(id, typ string) Event {
	return Event{ID: id, Type: typ, Payload: map[string]any{"k": "v"}}
}

// evtUntil builds an event with an explicit absolute `expiresAt`. That is the one
// knob: it widens the re-offer window (INV-FAIL-1) and the de-duplication window
// (INV-EVT-3) together, because retention runs to the same instant (DEC-EVENT-1).
func evtUntil(id, typ string, expiresAt time.Time) Event {
	e := evt(id, typ)
	e.ExpiresAt = expiresAt
	return e
}

func mustEnqueue(t *testing.T, q *Queue, e Event) EnqueueResult {
	t.Helper()
	r, err := q.Enqueue(e)
	if err != nil {
		t.Fatalf("Enqueue(%s): %v", e.ID, err)
	}
	return r
}

func newQueue(t *testing.T, clk *mockClock, opts ...Option) *Queue {
	t.Helper()
	all := append([]Option{WithClock(clk.now)}, opts...)
	q, err := New(NewMemStore(), all...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return q
}

// --- ADR-0031 requirement coverage ---------------------------------------

// Req 1: events are processed in order.
func TestInOrderDelivery(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	l := newListener("h", "T")
	q.Register(l)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))
	mustEnqueue(t, q, evtUntil("e2", "T", clk.in(time.Hour)))
	mustEnqueue(t, q, evtUntil("e3", "T", clk.in(time.Hour)))
	for range 3 {
		q.Dispatch()
	}
	if got := l.accepted; !equal(got, []string{"e1", "e2", "e3"}) {
		t.Fatalf("delivery order = %v, want [e1 e2 e3]", got)
	}
}

// Req 2 + INV-EVT-3: de-dup by id across the RETAINED id set, including
// already-accepted ids. Retention is what bounds the window, and `expiresAt` is
// what bounds retention — so this test must ask for a window to have one.
func TestDedupWhileRetained(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	l := newListener("h", "T")
	q.Register(l)
	if r := mustEnqueue(t, q, evtUntil("dup", "T", clk.in(time.Hour))); r != Enqueued {
		t.Fatalf("first enqueue = %v, want Enqueued", r)
	}
	if r := mustEnqueue(t, q, evtUntil("dup", "T", clk.in(time.Hour))); r != Deduped {
		t.Fatalf("re-enqueue before accept = %v, want Deduped", r)
	}
	q.Dispatch() // accept it
	// INV-EVT-3: dedup covers already-accepted ids while they are still retained.
	if r := mustEnqueue(t, q, evtUntil("dup", "T", clk.in(time.Hour))); r != Deduped {
		t.Fatalf("re-enqueue after accept = %v, want Deduped", r)
	}
	if len(l.accepted) != 1 {
		t.Fatalf("accepted %d times, want exactly 1 (dedup)", len(l.accepted))
	}
}

// INV-EVT-3 under the DEFAULT: because the retained-id set lives exactly as long
// as the event, the born-expired default collapses the de-dup window to roughly
// ONE DISPATCH CYCLE. A re-emit before that cycle completes is still absorbed;
// one after it is a FRESH event, not a resurrection (DEC-EVENT-1).
func TestDedupWindowCollapsesUnderBornExpiredDefault(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	l := newListener("h", "T")
	l.neverAccept = true // busy forever: the event is never accepted, only attempted
	q.Register(l)
	mustEnqueue(t, q, evt("pull", "T"))
	// Still inside the one owed attempt: the id is retained, so this is a dupe.
	if r := mustEnqueue(t, q, evt("pull", "T")); r != Deduped {
		t.Fatalf("re-emit before the owed attempt = %v, want Deduped", r)
	}
	q.Dispatch() // the one attempt; expired at attempt time, so it is the last
	q.Expire()   // retention over -> the id leaves the retained set
	if r := mustEnqueue(t, q, evt("pull", "T")); r != Enqueued {
		t.Fatalf("next-trigger re-emit = %v, want Enqueued (re-emission, not resurrection)", r)
	}
}

// INV-EVT-4 + INV-DISP-3: the DEFAULT event is born expired, so its first attempt
// is also its LAST. A busy handler still gets that one opportunity (INV-EVT-1 —
// nothing is dropped un-offered), the decline settles the pair, and the event is
// then dropped unconsumed-expired.
func TestBornExpiredDefaultOfferedOnceThenDropped(t *testing.T) {
	clk := newClock()
	obs := &recordingObserver{}
	q := newQueue(t, clk, WithObserver(obs))
	l := newListener("h", "T")
	l.neverAccept = true // permanently busy: a pre-accept decline every time
	q.Register(l)
	mustEnqueue(t, q, evt("e1", "T"))

	// The event is already expired, yet it MUST be offered: expiry is checked at
	// attempt time, it does not suppress the attempt.
	q.Dispatch()
	if !equal(l.offered, []string{"e1"}) {
		t.Fatalf("offers = %v, want exactly one offer of e1 (the one owed attempt)", l.offered)
	}
	// That attempt was the last one owed, so nothing is re-offered...
	q.Dispatch()
	if !equal(l.offered, []string{"e1"}) {
		t.Fatalf("offers = %v, want NO re-offer after the final (post-expiry) attempt", l.offered)
	}
	// ...and with nothing further owed, the event is dropped unconditionally.
	if n := q.Expire(); n != 1 {
		t.Fatalf("expire dropped %d, want 1", n)
	}
	if !equal(obs.unconsumedExpired, []string{"T"}) {
		t.Fatalf("unconsumed-expired = %v, want [T] — a genuine miss (INV-DISP-3)", obs.unconsumedExpired)
	}
}

// The same default with a handler that HAS capacity: accepted on its single
// offer, then retired with NO unconsumed-expired count. "Offer once to every
// matching handler, then drop" is a working default, not a lossy one.
func TestBornExpiredDefaultAcceptedOnItsOneOffer(t *testing.T) {
	clk := newClock()
	obs := &recordingObserver{}
	q := newQueue(t, clk, WithObserver(obs))
	l := newListener("h", "T")
	q.Register(l)
	mustEnqueue(t, q, evt("e1", "T"))
	if n := q.Dispatch(); n != 1 {
		t.Fatalf("accepted = %d, want 1 (a born-expired event is still deliverable)", n)
	}
	if n := q.Expire(); n != 1 {
		t.Fatalf("expire dropped %d, want 1 (nothing further owed)", n)
	}
	if len(obs.unconsumedExpired) != 0 {
		t.Fatalf("unconsumed-expired = %v, want none: it WAS consumed", obs.unconsumedExpired)
	}
	if !equal(obs.accepted, []string{"e1/h"}) {
		t.Fatalf("recorded acceptances = %v, want [e1/h]", obs.accepted)
	}
}

// INV-EVT-1, the hard case: a born-expired event fanned out to SEVERAL matching
// handlers owes EACH of them an opportunity. It may not be dropped after the
// first pass merely because it is expired — retention holds it until every
// matching handler has had its one attempt.
func TestExpiredEventIsOfferedToEveryMatchingHandler(t *testing.T) {
	clk := newClock()
	obs := &recordingObserver{}
	q := newQueue(t, clk, WithObserver(obs))
	a := newListener("a", "T")
	b := newListener("b", "T")
	b.busyRemaining["e1"] = 1 // b declines its one attempt
	q.Register(a)
	q.Register(b)
	mustEnqueue(t, q, evt("e1", "T"))

	q.Dispatch() // one pass offers each listener its head
	if n := q.Expire(); n != 1 {
		t.Fatalf("expire dropped %d, want 1 (both attempts made)", n)
	}
	if !equal(a.offered, []string{"e1"}) || !equal(b.offered, []string{"e1"}) {
		t.Fatalf("offers a=%v b=%v; every matching handler is owed one (INV-EVT-1)", a.offered, b.offered)
	}
	// a accepted, so this is NOT an unconsumed-expired miss even though b declined.
	if len(obs.unconsumedExpired) != 0 {
		t.Fatalf("unconsumed-expired = %v, want none (a accepted it)", obs.unconsumedExpired)
	}
}

// The retention half of INV-EVT-1 stated as a drop rule: an event whose matching
// handler has NOT yet had its attempt is retained even though it is already
// expired. Sweeping on the expiry instant alone would drop it un-offered.
func TestExpiredEventRetainedUntilItsOwedAttemptIsMade(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	l := newListener("h", "T")
	l.neverAccept = true
	q.Register(l)
	mustEnqueue(t, q, evt("e1", "T"))
	if n := q.Expire(); n != 0 {
		t.Fatalf("expire dropped %d, want 0 — the one owed attempt is outstanding", n)
	}
	if q.DepthByType()["T"] != 1 {
		t.Fatalf("expired-but-unattempted event not retained: %v", q.DepthByType())
	}
	q.Dispatch()
	if n := q.Expire(); n != 1 {
		t.Fatalf("expire dropped %d, want 1 once the attempt was made", n)
	}
}

// INV-EVT-4 boundary: `expiresAt` IS the retry window. A decline BEFORE it is
// re-offered (INV-FAIL-1 unchanged); the first attempt made at or after it is
// final. One knob, both behaviors.
func TestExpiresAtIsTheRetryWindow(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	l := newListener("h", "T")
	l.neverAccept = true
	q.Register(l)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(10*time.Minute)))

	q.Dispatch()
	q.Dispatch()
	if !equal(l.offered, []string{"e1", "e1"}) {
		t.Fatalf("offers = %v, want e1 re-offered while unexpired (INV-FAIL-1)", l.offered)
	}
	clk.advance(11 * time.Minute) // past expiresAt
	q.Dispatch()                  // this attempt is the last one owed
	q.Dispatch()                  // ...so nothing more is offered
	if !equal(l.offered, []string{"e1", "e1", "e1"}) {
		t.Fatalf("offers = %v, want exactly one attempt past expiresAt then no more", l.offered)
	}
}

// INV-DISP-3: an event no configured binding matches is enqueued, offered to
// nobody, and dropped unconsumed-expired at its expiry — a visibility signal, not
// a rejection of the caller.
func TestNoBindingExpiresUnconsumed(t *testing.T) {
	clk := newClock()
	obs := &recordingObserver{}
	q := newQueue(t, clk, WithObserver(obs))
	// A listener that does NOT bind this type — the event has no handler.
	q.Register(newListener("h", "other"))
	mustEnqueue(t, q, evtUntil("e1", "orphan-type", clk.in(10*time.Minute)))
	q.Dispatch() // no match; nothing offered, nothing accepted
	if n := q.Expire(); n != 0 {
		t.Fatalf("premature expiry: dropped %d", n)
	}
	clk.advance(11 * time.Minute)
	if n := q.Expire(); n != 1 {
		t.Fatalf("expire dropped %d, want 1", n)
	}
	if !equal(obs.unconsumedExpired, []string{"orphan-type"}) {
		t.Fatalf("unconsumed-expired = %v, want [orphan-type]", obs.unconsumedExpired)
	}
	if len(q.DepthByType()) != 0 {
		t.Fatalf("queue not drained after expiry: %v", q.DepthByType())
	}
}

// A fresh event may reuse an id once the prior one's retention is over and it has
// been dropped (the dedup window is the retention window, not forever).
func TestDedupResetsAfterRetentionEnds(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	mustEnqueue(t, q, evtUntil("x", "T", clk.in(10*time.Minute)))
	clk.advance(11 * time.Minute)
	q.Expire()
	if r := mustEnqueue(t, q, evtUntil("x", "T", clk.in(10*time.Minute))); r != Enqueued {
		t.Fatalf("post-retirement re-use of id = %v, want Enqueued", r)
	}
}

// Enqueue's stale-replace path retires the old entry on the SAME terms Expire
// would have: the miss is still counted and the removal is still recorded
// durably, so WHICH of the two removes a retired entry cannot change what an
// observer or a replay sees.
func TestEnqueueStaleReplaceCountsTheMissAndRecordsTheEvict(t *testing.T) {
	clk := newClock()
	obs := &recordingObserver{}
	store := NewMemStore()
	q, err := New(store, WithClock(clk.now), WithObserver(obs))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// No listener binds it, so its retention is over the moment it expires.
	mustEnqueue(t, q, evtUntil("x", "T", clk.in(time.Minute)))
	clk.advance(2 * time.Minute)
	// Re-emit WITHOUT an intervening Expire: Enqueue itself retires the stale one.
	if r := mustEnqueue(t, q, evtUntil("x", "T", clk.in(time.Minute))); r != Enqueued {
		t.Fatalf("re-emit past retention = %v, want Enqueued", r)
	}
	if !equal(obs.unconsumedExpired, []string{"T"}) {
		t.Fatalf("unconsumed-expired = %v, want [T] — the replaced event was a genuine miss", obs.unconsumedExpired)
	}
	recs, err := store.Replay()
	if err != nil {
		t.Fatal(err)
	}
	var evicts int
	for _, r := range recs {
		if r.Op == opEvict {
			evicts++
		}
	}
	if evicts != 1 {
		t.Fatalf("durable evict records = %d, want 1 (the stale entry's removal)", evicts)
	}
}

// INV-CONC-1: per-listener cursors are independent; a listener stuck (busy) on
// its head does not block a different listener's stream (head-of-line blocking
// is per-listener).
func TestPerListenerCursorIndependence(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	fast := newListener("fast", "T")
	slow := newListener("slow", "T")
	slow.busyRemaining["e1"] = 100 // slow is stuck on its head e1
	q.Register(fast)
	q.Register(slow)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))
	mustEnqueue(t, q, evtUntil("e2", "T", clk.in(time.Hour)))
	for range 3 {
		q.Dispatch()
	}
	if !equal(fast.accepted, []string{"e1", "e2"}) {
		t.Fatalf("fast listener accepted %v, want [e1 e2] (independent cursor)", fast.accepted)
	}
	// slow must never have been offered e2 while stuck on e1 (head-of-line).
	if slices.Contains(slow.offered, "e2") {
		t.Fatalf("slow was offered e2 while stuck on head e1 (head-of-line violated)")
	}
}

// INV-FAIL-1: a pre-accept busy decline is re-offered while the event is
// unexpired (same head).
func TestPreAcceptBusyReoffer(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	l := newListener("h", "T")
	l.busyRemaining["e1"] = 2 // decline twice, accept on the third offer
	q.Register(l)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))
	for range 3 {
		q.Dispatch()
	}
	if !equal(l.offered, []string{"e1", "e1", "e1"}) {
		t.Fatalf("offers = %v, want e1 re-offered 3x", l.offered)
	}
	if !equal(l.accepted, []string{"e1"}) {
		t.Fatalf("accepted = %v, want [e1]", l.accepted)
	}
}

// ADR retention: an event is retained while unexpired regardless of consumer
// state (absent / down / disabled), and a listener that binds while it is still
// retained is a matching handler owed an attempt (INV-EVT-1), so it gets one.
//
// This pins the RETENTION RULE, not a catch-up guarantee: ADR 0031's
// "new-listener catch-up" benefit claim is WITHDRAWN by DEC-EVENT-1. Retention
// exists so every matching handler still owed an attempt gets one and so de-dup
// covers delivered ids — a late binder receiving the event is a consequence of
// that, not a promise, and under the born-expired default there is no window for
// it at all.
func TestRetentionIndependentOfConsumerState(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(30*time.Minute)))
	// No consumer yet. Advance while still unexpired; the event is still retained.
	clk.advance(10 * time.Minute)
	q.Expire()
	if q.DepthByType()["T"] != 1 {
		t.Fatalf("event not retained while consumer absent: %v", q.DepthByType())
	}
	// A consumer binds while it is retained and is owed its attempt.
	l := newListener("late", "T")
	q.Register(l)
	q.Dispatch()
	if !equal(l.accepted, []string{"e1"}) {
		t.Fatalf("late binder was not offered the retained event: %v", l.accepted)
	}
}

// A disabled consumer (binding absent for this run) leaves its events to expire
// unconsumed — not an error (INV-DISP-3).
func TestDisabledConsumerLeavesEventsToExpire(t *testing.T) {
	clk := newClock()
	obs := &recordingObserver{}
	q := newQueue(t, clk, WithObserver(obs))
	q.Register(newListener("disabled")) // binds nothing
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(5*time.Minute)))
	q.Dispatch()
	clk.advance(6 * time.Minute)
	q.Expire()
	if !equal(obs.unconsumedExpired, []string{"T"}) {
		t.Fatalf("expected unconsumed-expired [T], got %v", obs.unconsumedExpired)
	}
}

// ADR opt-in eviction: with early eviction on, an event is evicted only after
// EVERY bound listener has accepted it; before that it is retained.
func TestOptInEviction(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk, WithEarlyEviction())
	a := newListener("a", "T")
	b := newListener("b", "T")
	q.Register(a)
	q.Register(b)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))
	q.Dispatch() // a and b are both offered e1 in one pass; both accept
	// Both bound listeners accepted in the same pass -> evicted early.
	if q.DepthByType()["T"] != 0 {
		t.Fatalf("event not evicted after all bound listeners accepted: %v", q.DepthByType())
	}
}

// Without early eviction, an accepted event is retained until its expiry, so its
// id keeps bounding the de-dup window (INV-EVT-3).
func TestNoEvictionRetainsUntilExpiry(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	a := newListener("a", "T")
	q.Register(a)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))
	q.Dispatch()
	if q.DepthByType()["T"] != 1 {
		t.Fatalf("accepted event should be retained until its expiry: %v", q.DepthByType())
	}
}

// Regression (bead pg2-f8btt): with early eviction on, evicting an id MUST drop
// it from the FIFO spine (q.order), not leave a tombstone. Otherwise a re-emit
// of the evicted id BEFORE the next Expire() is treated as fresh and appends a
// SECOND spine entry — double-counting the id in DepthByType (INV-OBS-1 queue
// depth) and jumping the re-emitted event ahead of earlier events (ADR-0031
// req 1, FIFO).
func TestEvictedIdReEmitLeavesNoTombstone(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk, WithEarlyEviction())
	a := newListener("a", "T")
	q.Register(a)
	mustEnqueue(t, q, evtUntil("A", "T", clk.in(time.Hour)))
	mustEnqueue(t, q, evtUntil("B", "T", clk.in(time.Hour)))
	// One pass offers a its head A; a accepts; a is the only bound listener, so
	// A is evicted early. B is NOT offered this pass (one head per listener).
	q.Dispatch()
	if d := q.DepthByType()["T"]; d != 1 {
		t.Fatalf("after evicting A, depth[T] = %d, want 1 (only B retained)", d)
	}
	// Re-emit A BEFORE any Expire(). A is a genuinely fresh event now; it MUST
	// go to the FIFO tail (after B), not resurrect a stale spine position.
	if r := mustEnqueue(t, q, evtUntil("A", "T", clk.in(time.Hour))); r != Enqueued {
		t.Fatalf("re-emit of evicted A = %v, want Enqueued (fresh event)", r)
	}
	// INV-OBS-1: depth counts distinct retained events (B + fresh A) = 2. A
	// tombstoned A left in q.order would inflate this to 3.
	if d := q.DepthByType()["T"]; d != 2 {
		t.Fatalf("depth[T] = %d after re-emit, want 2 (B + fresh A); tombstone double-count", d)
	}
	// Drain. FIFO (ADR-0031 req 1): B (enqueued before the re-emit) is delivered
	// before the re-emitted A. With a tombstone the fresh A jumps to spine index
	// 0 and is delivered before B — the corruption this guards against.
	for range 5 {
		q.Dispatch()
	}
	if !equal(a.accepted, []string{"A", "B", "A"}) {
		t.Fatalf("delivery order = %v, want [A B A] (B before the re-emitted A; FIFO)", a.accepted)
	}
}

// Regression (bead pg2-f8btt): the same evict-leaves-tombstone defect on the
// durable replay path (opEvict). A WAL of enqueue A, enqueue B, evict A, then
// re-emit A (fresh) MUST replay to a tombstone-free spine — B before the
// re-emitted A, and A counted once.
func TestReplayEvictedIdReEmitLeavesNoTombstone(t *testing.T) {
	clk := newClock()
	store := NewMemStore()
	enq := func(id string) Record {
		return recordFromEvent(evtUntil(id, "T", clk.in(time.Hour)).Resolve(clk.now()), clk.now())
	}
	for _, r := range []Record{
		enq("A"),
		enq("B"),
		{Op: opEvict, EventID: "A"},
		enq("A"), // re-emit of the evicted id, still within its retention
	} {
		if err := store.Append(r); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	q, err := New(store, WithClock(clk.now), WithEarlyEviction())
	if err != nil {
		t.Fatalf("New(replay): %v", err)
	}
	if d := q.DepthByType()["T"]; d != 2 {
		t.Fatalf("post-replay depth[T] = %d, want 2 (B + fresh A); opEvict tombstone double-count", d)
	}
	a := newListener("a", "T")
	q.Register(a)
	for range 5 {
		q.Dispatch()
	}
	if !equal(a.accepted, []string{"B", "A"}) {
		t.Fatalf("post-replay delivery order = %v, want [B A] (FIFO; B before the re-emitted A)", a.accepted)
	}
}

// Fan-out: an event bound by several listeners is delivered to each, with
// acceptance tracked per (event, listener) (INV-EVT-1).
func TestFanOut(t *testing.T) {
	clk := newClock()
	obs := &recordingObserver{}
	q := newQueue(t, clk, WithObserver(obs))
	a := newListener("a", "T")
	b := newListener("b", "T")
	q.Register(a)
	q.Register(b)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))
	q.Dispatch()
	if !equal(a.accepted, []string{"e1"}) || !equal(b.accepted, []string{"e1"}) {
		t.Fatalf("fan-out failed: a=%v b=%v", a.accepted, b.accepted)
	}
	// Observer saw both (event, listener) acceptances.
	if !slices.Contains(obs.accepted, "e1/a") || !slices.Contains(obs.accepted, "e1/b") {
		t.Fatalf("per-(event,listener) accept not observed: %v", obs.accepted)
	}
}

// Unknown-type event is accepted into the queue and waits to expire unconsumed,
// never rejected at ingest (INV-DISP-3).
func TestUnknownTypeQueuedNotRejected(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	q.Register(newListener("h", "known"))
	if r := mustEnqueue(t, q, evtUntil("u1", "unknown", clk.in(5*time.Minute))); r != Enqueued {
		t.Fatalf("unknown-type enqueue = %v, want Enqueued (not rejected)", r)
	}
	if q.DepthByType()["unknown"] != 1 {
		t.Fatalf("unknown-type event not queued")
	}
}

// Malformed events are rejected at Enqueue (interfaces.md ingest `rejected`).
// An already-past `expiresAt` is NOT malformed — it is the default.
func TestEnqueueRejectsMalformed(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	if _, err := q.Enqueue(Event{Type: "T"}); err == nil {
		t.Fatalf("expected rejection of event missing id")
	}
	if _, err := q.Enqueue(evtUntil("past", "T", clk.in(-time.Hour))); err != nil {
		t.Fatalf("an expiresAt in the past was rejected: %v — the default event is born expired", err)
	}
}

// capturingListener binds everything, accepts everything, and keeps the Event it
// was OFFERED — so a test can assert what the queue actually hands a handler.
type capturingListener struct{ got map[string]Event }

func (c *capturingListener) ID() string         { return "cap" }
func (c *capturingListener) Matches(Event) bool { return true }
func (c *capturingListener) Offer(e Event) bool { c.got[e.ID] = e; return true }

// Enqueue RESOLVES the optional instants against the core's own clock, and the
// queue stores and OFFERS the resolved event — so a handler is handed concrete
// instants rather than having to re-derive the defaults itself (INV-EVT-1).
func TestEnqueueResolvesInstantsAgainstTheCoreClock(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	l := &capturingListener{got: map[string]Event{}}
	q.Register(l)

	stamped := evt("stamped", "T")
	stamped.At = clk.in(-time.Minute) // a source stamp, and no expiresAt
	mustEnqueue(t, q, stamped)
	mustEnqueue(t, q, evt("bare", "T")) // neither field
	q.Dispatch()
	q.Dispatch() // one head per listener per pass

	if got := l.got["bare"]; !got.At.Equal(clk.now()) || !got.ExpiresAt.Equal(clk.now()) {
		t.Fatalf("bare event offered as at=%s expiresAt=%s, want both = ingest-now %s",
			got.At, got.ExpiresAt, clk.now())
	}
	if got := l.got["stamped"]; !got.ExpiresAt.Equal(stamped.At) || !got.At.Equal(stamped.At) {
		t.Fatalf("stamped event offered as at=%s expiresAt=%s, want both = its own at %s",
			got.At, got.ExpiresAt, stamped.At)
	}
}

// INV-LIFE-1: run-until-idle drains and exits once every event is accepted or
// expired and no offer is outstanding.
func TestRunUntilIdleDrains(t *testing.T) {
	q, err := New(NewMemStore()) // real clock
	if err != nil {
		t.Fatal(err)
	}
	l := newListener("h", "T")
	q.Register(l)
	for _, id := range []string{"e1", "e2", "e3"} {
		mustEnqueue(t, q, evtUntil(id, "T", time.Now().Add(time.Minute)))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := q.RunUntilIdle(ctx, time.Millisecond); err != nil {
		t.Fatalf("RunUntilIdle: %v", err)
	}
	if !equal(l.accepted, []string{"e1", "e2", "e3"}) {
		t.Fatalf("run-until-idle did not drain: %v", l.accepted)
	}
}

// run-until-idle drains a DEFAULT (born-expired) workload in ONE pass: the offer
// every event is owed happens before the sweep, so the sweep in the same pass
// retires what the offer settled.
func TestRunUntilIdleDrainsBornExpiredWorkloadInOnePass(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk, WithSleeper(clk.after))
	l := newListener("h", "T")
	q.Register(l)
	mustEnqueue(t, q, evt("e1", "T"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := q.RunUntilIdle(ctx, time.Minute); err != nil {
		t.Fatalf("RunUntilIdle: %v", err)
	}
	// One pass: the clock never had to advance, so the sleeper never fired.
	if clk.now() != newClock().now() {
		t.Fatalf("clock advanced to %s; a born-expired workload must drain without waiting", clk.now())
	}
	if !equal(l.accepted, []string{"e1"}) {
		t.Fatalf("accepted = %v, want [e1]", l.accepted)
	}
	if len(q.DepthByType()) != 0 {
		t.Fatalf("queue not drained: %v", q.DepthByType())
	}
}

// run-until-idle also exits when an event simply expires with no consumer (drained
// by expiry), not only by acceptance.
func TestRunUntilIdleExitsOnExpiry(t *testing.T) {
	q, err := New(NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	q.Register(newListener("h", "other"))
	mustEnqueue(t, q, evtUntil("e1", "orphan", time.Now().Add(30*time.Millisecond)))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := q.RunUntilIdle(ctx, time.Millisecond); err != nil {
		t.Fatalf("RunUntilIdle: %v", err)
	}
	if len(q.DepthByType()) != 0 {
		t.Fatalf("event not drained by expiry: %v", q.DepthByType())
	}
}

// run-until-idle honors context cancellation while work is still pending: a busy
// handler on an UNEXPIRED event keeps its head re-offered forever, so the queue
// never becomes idle.
func TestRunUntilIdleCancel(t *testing.T) {
	q, err := New(NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	l := newListener("h", "T")
	l.neverAccept = true // stays busy forever -> never idle
	q.Register(l)
	mustEnqueue(t, q, evtUntil("e1", "T", time.Now().Add(time.Hour)))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := q.RunUntilIdle(ctx, time.Millisecond); err == nil {
		t.Fatalf("expected context error, got nil")
	}
}

// Fix 3 (clock seam): under a MOCK clock, RunUntilIdle terminates on expiry by
// driving its between-pass wait off the SAME clock seam (WithSleeper), advancing
// virtual time each pass. With the old real-time.After it would sleep real time
// while q.now stayed frozen — deadlines never advanced, so it could never expire
// the orphan and busy-looped until ctx. Here the orphan (expiring in 30m, no
// binding) must drain via virtual-time expiry, NOT via the real 5s ctx deadline.
func TestRunUntilIdleMockClockExpiresWithoutRealSleep(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk, WithSleeper(clk.after))
	q.Register(newListener("h", "other")) // binds nothing -> the event is an orphan
	mustEnqueue(t, q, evtUntil("e1", "orphan", clk.in(30*time.Minute)))

	start := time.Now()
	// A real 5s ceiling proves termination is by VIRTUAL expiry, not by this ctx: a
	// 1-minute tick advances virtual time 1m/pass, so ~30 passes expire the orphan
	// essentially instantly in real time.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := q.RunUntilIdle(ctx, time.Minute); err != nil {
		t.Fatalf("RunUntilIdle under mock clock: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("RunUntilIdle slept real time (%v); it must advance the mock clock", elapsed)
	}
	if len(q.DepthByType()) != 0 {
		t.Fatalf("orphan not drained by virtual-time expiry: %v", q.DepthByType())
	}
}

// --- durability of retirement --------------------------------------------

// A retired event MUST NOT come back. Because an expired event can still be owed
// an attempt, replay cannot re-derive retirement from the clock — so the removal
// is recorded durably (opEvict) and replay honours the log.
func TestRetirementIsDurableSoReplayDoesNotResurrect(t *testing.T) {
	clk := newClock()
	store := NewMemStore()
	q1, err := New(store, WithClock(clk.now))
	if err != nil {
		t.Fatal(err)
	}
	l1 := newListener("h", "T")
	q1.Register(l1)
	mustEnqueue(t, q1, evt("e1", "T")) // born expired
	q1.Dispatch()                      // its one attempt, accepted
	if n := q1.Expire(); n != 1 {
		t.Fatalf("expire dropped %d, want 1", n)
	}

	q2, err := New(store, WithClock(clk.now))
	if err != nil {
		t.Fatal(err)
	}
	l2 := newListener("h", "T")
	q2.Register(l2)
	q2.Dispatch()
	if len(l2.offered) != 0 {
		t.Fatalf("a retired event was resurrected by replay and re-offered: %v", l2.offered)
	}
}

// The converse, and the reason replay cannot filter on the clock: an event that is
// past its expiry but was NEVER offered (crash between the durable enqueue and
// the dispatch pass) MUST survive the restart and still get its attempt. Under
// the born-expired default that is EVERY un-dispatched event, so a past-expiry
// filter on replay would mean the durable queue survived no restart at all
// (INV-EVT-1: nothing is dropped un-offered).
func TestReplayRetainsPastExpiryEventSoItStillGetsItsAttempt(t *testing.T) {
	clk := newClock()
	store := NewMemStore()
	q1, err := New(store, WithClock(clk.now))
	if err != nil {
		t.Fatal(err)
	}
	mustEnqueue(t, q1, evt("e1", "T")) // durable, born expired, never dispatched

	clk.advance(time.Hour) // the core was down for an hour
	q2, err := New(store, WithClock(clk.now))
	if err != nil {
		t.Fatal(err)
	}
	l := newListener("h", "T")
	q2.Register(l)
	q2.Dispatch()
	if !equal(l.accepted, []string{"e1"}) {
		t.Fatalf("un-offered event was dropped across the restart: %v", l.accepted)
	}
}

// A failing evict-append is a durability degradation — the id replays as retained
// and is offered again — so it is SURFACED via a structured log rather than
// discarded, matching how Dispatch surfaces a failed accept-append.
func TestExpireSurfacesEvictAppendError(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	clk := newClock()
	q, err := New(&failEvictStore{inner: NewMemStore()}, WithClock(clk.now))
	if err != nil {
		t.Fatal(err)
	}
	mustEnqueue(t, q, evt("e1", "T")) // born expired, no binding -> retired at once
	if n := q.Expire(); n != 1 {
		t.Fatalf("expire dropped %d, want 1", n)
	}
	if !strings.Contains(buf.String(), "evict-append failed") {
		t.Fatalf("evict-append error was swallowed, not surfaced; log = %q", buf.String())
	}
}

// failEvictStore errors on the durable evict write, persisting every other op.
type failEvictStore struct{ inner Store }

func (s *failEvictStore) Append(r Record) error {
	if r.Op == opEvict {
		return errors.New("evict-append boom")
	}
	return s.inner.Append(r)
}
func (s *failEvictStore) Replay() ([]Record, error) { return s.inner.Replay() }
func (s *failEvictStore) Close() error              { return s.inner.Close() }

// --- small helpers --------------------------------------------------------

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
