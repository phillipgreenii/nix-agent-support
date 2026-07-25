package eventqueue

import (
	"context"
	"slices"
	"testing"
	"time"
)

// --- test doubles ---------------------------------------------------------

// mockClock is a deterministic, advanceable clock for ttl tests.
type mockClock struct{ t time.Time }

func newClock() *mockClock {
	return &mockClock{t: time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)}
}
func (c *mockClock) now() time.Time          { return c.t }
func (c *mockClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// fakeListener records every offer and can be configured to decline (busy) a
// given number of times per event id before accepting, or to never bind / never
// accept — enough to drive every INV-CONC-1 / INV-FAIL-1 branch.
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

func evt(id, typ string, ttl time.Duration) Event {
	return Event{ID: id, Type: typ, TTL: ttl, Payload: map[string]any{"k": "v"}}
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
	mustEnqueue(t, q, evt("e1", "T", time.Hour))
	mustEnqueue(t, q, evt("e2", "T", time.Hour))
	mustEnqueue(t, q, evt("e3", "T", time.Hour))
	for range 3 {
		q.Dispatch()
	}
	if got := l.accepted; !equal(got, []string{"e1", "e2", "e3"}) {
		t.Fatalf("delivery order = %v, want [e1 e2 e3]", got)
	}
}

// Req 2 + INV-EVT-3: de-dup by id within ttl, including already-accepted ids.
func TestDedupWithinTTL(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	l := newListener("h", "T")
	q.Register(l)
	if r := mustEnqueue(t, q, evt("dup", "T", time.Hour)); r != Enqueued {
		t.Fatalf("first enqueue = %v, want Enqueued", r)
	}
	if r := mustEnqueue(t, q, evt("dup", "T", time.Hour)); r != Deduped {
		t.Fatalf("re-enqueue before accept = %v, want Deduped", r)
	}
	q.Dispatch() // accept it
	// INV-EVT-3: dedup covers already-accepted ids still within ttl.
	if r := mustEnqueue(t, q, evt("dup", "T", time.Hour)); r != Deduped {
		t.Fatalf("re-enqueue after accept = %v, want Deduped", r)
	}
	if len(l.accepted) != 1 {
		t.Fatalf("accepted %d times, want exactly 1 (dedup)", len(l.accepted))
	}
}

// Req 3 + INV-DISP-3: an event expires at ttl; expiring with no acceptance is
// unconsumed-expired (reported to the observer).
func TestTTLExpiryUnconsumedExpired(t *testing.T) {
	clk := newClock()
	obs := &recordingObserver{}
	q := newQueue(t, clk, WithObserver(obs))
	// A listener that does NOT bind this type — the event has no handler.
	q.Register(newListener("h", "other"))
	mustEnqueue(t, q, evt("e1", "orphan-type", 10*time.Minute))
	q.Dispatch() // no match; nothing accepted
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

// A fresh event may reuse an id once the prior one has expired and been dropped
// (the dedup window is the retention window, not forever).
func TestDedupResetsAfterExpiry(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	mustEnqueue(t, q, evt("x", "T", 10*time.Minute))
	clk.advance(11 * time.Minute)
	q.Expire()
	if r := mustEnqueue(t, q, evt("x", "T", 10*time.Minute)); r != Enqueued {
		t.Fatalf("post-expiry re-use of id = %v, want Enqueued", r)
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
	mustEnqueue(t, q, evt("e1", "T", time.Hour))
	mustEnqueue(t, q, evt("e2", "T", time.Hour))
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

// INV-FAIL-1: a pre-accept busy decline is re-offered within ttl (same head).
func TestPreAcceptBusyReoffer(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	l := newListener("h", "T")
	l.busyRemaining["e1"] = 2 // decline twice, accept on the third offer
	q.Register(l)
	mustEnqueue(t, q, evt("e1", "T", time.Hour))
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

// ADR retention: an event is retained until ttl regardless of consumer state
// (absent / down / disabled), and a listener that binds LATER within the ttl
// still receives it (new-listener catch-up).
func TestRetentionIndependentOfConsumer(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	mustEnqueue(t, q, evt("e1", "T", 30*time.Minute))
	// No consumer yet. Advance within ttl; the event is still retained.
	clk.advance(10 * time.Minute)
	q.Expire()
	if q.DepthByType()["T"] != 1 {
		t.Fatalf("event not retained while consumer absent: %v", q.DepthByType())
	}
	// A consumer binds within the ttl and catches up.
	l := newListener("late", "T")
	q.Register(l)
	q.Dispatch()
	if !equal(l.accepted, []string{"e1"}) {
		t.Fatalf("late listener did not catch up: %v", l.accepted)
	}
}

// A disabled consumer (binding absent for this run) leaves its events to expire
// unconsumed — not an error (INV-DISP-3).
func TestDisabledConsumerLeavesEventsToExpire(t *testing.T) {
	clk := newClock()
	obs := &recordingObserver{}
	q := newQueue(t, clk, WithObserver(obs))
	q.Register(newListener("disabled")) // binds nothing
	mustEnqueue(t, q, evt("e1", "T", 5*time.Minute))
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
	mustEnqueue(t, q, evt("e1", "T", time.Hour))
	q.Dispatch() // a and b are both offered e1 in one pass; both accept
	// Both bound listeners accepted in the same pass -> evicted early.
	if q.DepthByType()["T"] != 0 {
		t.Fatalf("event not evicted after all bound listeners accepted: %v", q.DepthByType())
	}
}

// Without early eviction, an event is retained until ttl even after all accept.
func TestNoEvictionRetainsUntilTTL(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	a := newListener("a", "T")
	q.Register(a)
	mustEnqueue(t, q, evt("e1", "T", time.Hour))
	q.Dispatch()
	if q.DepthByType()["T"] != 1 {
		t.Fatalf("event should be retained until ttl by default: %v", q.DepthByType())
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
	mustEnqueue(t, q, evt("A", "T", time.Hour))
	mustEnqueue(t, q, evt("B", "T", time.Hour))
	// One pass offers a its head A; a accepts; a is the only bound listener, so
	// A is evicted early. B is NOT offered this pass (one head per listener).
	q.Dispatch()
	if d := q.DepthByType()["T"]; d != 1 {
		t.Fatalf("after evicting A, depth[T] = %d, want 1 (only B retained)", d)
	}
	// Re-emit A BEFORE any Expire(). A is a genuinely fresh event now; it MUST
	// go to the FIFO tail (after B), not resurrect a stale spine position.
	if r := mustEnqueue(t, q, evt("A", "T", time.Hour)); r != Enqueued {
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
	enq := func(id string) Record { return recordFromEvent(evt(id, "T", time.Hour), clk.now()) }
	for _, r := range []Record{
		enq("A"),
		enq("B"),
		{Op: opEvict, EventID: "A"},
		enq("A"), // re-emit of the evicted id, still within ttl
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
	mustEnqueue(t, q, evt("e1", "T", time.Hour))
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
	if r := mustEnqueue(t, q, evt("u1", "unknown", 5*time.Minute)); r != Enqueued {
		t.Fatalf("unknown-type enqueue = %v, want Enqueued (not rejected)", r)
	}
	if q.DepthByType()["unknown"] != 1 {
		t.Fatalf("unknown-type event not queued")
	}
}

// Malformed events are rejected at Enqueue (interfaces.md ingest `rejected`).
func TestEnqueueRejectsMalformed(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	if _, err := q.Enqueue(Event{Type: "T", TTL: time.Minute}); err == nil {
		t.Fatalf("expected rejection of event missing id")
	}
}

// INV-LIFE-1: run-until-idle drains and exits once every event is accepted or
// expired and no offer is outstanding.
func TestRunUntilIdleDrains(t *testing.T) {
	q, err := New(NewMemStore()) // real clock; short ttls
	if err != nil {
		t.Fatal(err)
	}
	l := newListener("h", "T")
	q.Register(l)
	for _, id := range []string{"e1", "e2", "e3"} {
		mustEnqueue(t, q, evt(id, "T", time.Minute))
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

// run-until-idle also exits when an event simply expires with no consumer
// (queue-drained by TTL), not only by acceptance.
func TestRunUntilIdleExitsOnExpiry(t *testing.T) {
	q, err := New(NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	q.Register(newListener("h", "other"))
	mustEnqueue(t, q, evt("e1", "orphan", 30*time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := q.RunUntilIdle(ctx, time.Millisecond); err != nil {
		t.Fatalf("RunUntilIdle: %v", err)
	}
	if len(q.DepthByType()) != 0 {
		t.Fatalf("event not drained by expiry: %v", q.DepthByType())
	}
}

// run-until-idle honors context cancellation while work is still pending.
func TestRunUntilIdleCancel(t *testing.T) {
	q, err := New(NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	l := newListener("h", "T")
	l.neverAccept = true // stays busy forever -> never idle
	q.Register(l)
	mustEnqueue(t, q, evt("e1", "T", time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := q.RunUntilIdle(ctx, time.Millisecond); err == nil {
		t.Fatalf("expected context error, got nil")
	}
}

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
