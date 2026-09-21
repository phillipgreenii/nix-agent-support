package eventqueue

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// --- Kick() test scenarios (this package's design doc, "decouple
// pg-router-core's tick loop from per-dispatch-pass completion" — Testing
// plan). Dispatch() itself is unchanged (proven by the rest of this
// package's ~50+ Dispatch()-based tests passing unmodified); everything
// below is new coverage for Kick(), a method with no prior tests to
// preserve.
//
// waitForCond is the ONE shared "wait until settled" helper every test below
// uses wherever a Dispatch()-based test used to rely on Dispatch's blocking
// return as its only synchronization signal. It is sound without making
// fakeListener (or any other existing LISTENER double) thread-safe: every
// cond here polls a SAFE, already-synchronized queue accessor —
// SessionsInFlight (locked under q.mu) or q.delivered.Load() (atomic) —
// never a double's own bare unsynchronized field directly. The underlying
// argument (this package's design doc): Offer() — which may append to a
// double's plain, unsynchronized fields — runs to completion BEFORE the
// same goroutine acquires q.mu for its own phase 3, which clears that
// offer's inFlight/custody entry and increments q.delivered BEFORE
// releasing the lock. A test goroutine that observes that clearing (via
// SessionsInFlight, or any other q.mu-guarded read, or q.delivered once it
// reaches the expected count) is guaranteed, by ordinary mutex
// happens-before, to see everything that offer's own goroutine did
// beforehand — including a LISTENER double's field writes made earlier in
// program order on that same goroutine, during Offer().
//
// This does NOT cover the Observer's own signals: q.fanOut runs strictly
// AFTER Kick's per-offer goroutine releases q.mu (the lock-order invariant
// on q.mu's own doc), i.e. AFTER — later in program order than — the very
// state this helper polls. Observing SessionsInFlight()==0 or
// q.delivered.Load() at its expected count does NOT guarantee the
// corresponding OnAccept/OnDeclined call has already happened; a test that
// needs to inspect what an Observer recorded uses syncObserver below
// instead, which signals over a channel (a real happens-before edge) AFTER
// its own write.
func waitForCond(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %v", timeout)
		}
		time.Sleep(time.Millisecond)
	}
}

// kickSequentialListener is Kick()'s test double for scenarios that need to
// (a) prove an outstanding offer keeps a listener SKIPPED by repeated
// Kick() calls (the Offer invocation count must stay pinned) and (b) later
// release that offer and observe the very NEXT, distinct Offer call —
// proving the per-listener FIFO cursor survives Kick()'s async settlement
// path. It mirrors this package's existing slowListener
// (concurrency_test.go, Task 6.1) in style — a mutex-guarded counter plus
// entered/proceed gating — but ALWAYS accepts once released (slowListener
// always declines) and supports being offered any number of DISTINCT
// events, one at a time, by re-arming its gate before each: INV-CONC-1's
// own one-outstanding-offer-per-listener guarantee already ensures no two
// of its own Offer calls ever overlap, so re-arming between calls is safe.
type kickSequentialListener struct {
	id    string
	binds map[string]bool

	mu      sync.Mutex
	offers  int
	entered chan struct{}
	proceed chan struct{}
}

func newKickSequentialListener(id string, types ...string) *kickSequentialListener {
	b := map[string]bool{}
	for _, t := range types {
		b[t] = true
	}
	return &kickSequentialListener{id: id, binds: b}
}

func (l *kickSequentialListener) ID() string           { return l.id }
func (l *kickSequentialListener) Matches(e Event) bool { return l.binds[e.Type] }

// arm prepares this listener for its NEXT Offer call, returning the
// entered channel the test waits on and a release func the test calls to
// let that Offer call return. Must be called BEFORE the Kick() call
// expected to reach this listener.
func (l *kickSequentialListener) arm() (entered <-chan struct{}, release func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := make(chan struct{})
	p := make(chan struct{})
	l.entered = e
	l.proceed = p
	return e, func() { close(p) }
}

func (l *kickSequentialListener) Offer(Offering) OfferResult {
	l.mu.Lock()
	l.offers++
	entered := l.entered
	proceed := l.proceed
	l.mu.Unlock()
	close(entered)
	<-proceed
	return OfferResult{Accepted: true, Decline: DeclineNone}
}

func (l *kickSequentialListener) offerCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.offers
}

// syncObserver is a thread-safe Observer double for Kick()'s tests that
// need to inspect what the Observer recorded. Unlike Dispatch() — whose
// blocking return already guarantees every hook for its pass fired before
// the caller resumes, which is why this package's existing
// recordingObserver (queue_test.go) never needed its own synchronization —
// Kick()'s fan-out (q.fanOut) runs on each offer's own DETACHED goroutine,
// strictly AFTER that goroutine releases q.mu. A test cannot use
// waitForCond (SessionsInFlight/q.delivered, both settled BEFORE the
// unlock) as a proxy for "the Observer hook already fired": this double is
// mutex-guarded for exactly that reason, and its onAccept channel gives a
// real happens-before edge (a channel send happens before the corresponding
// receive completes) a test can wait on instead.
type syncObserver struct {
	mu       sync.Mutex
	accepted []string
	onAccept chan struct{} // one send per OnAccept call
}

func newSyncObserver(buf int) *syncObserver {
	return &syncObserver{onAccept: make(chan struct{}, buf)}
}

func (o *syncObserver) OnEnqueue(Event)                   {}
func (o *syncObserver) OnUnconsumedExpired(string)        {}
func (o *syncObserver) OnDeclined(string, string, string) {}
func (o *syncObserver) OnDispatchFailure(string)          {}
func (o *syncObserver) OnDeduped(string)                  {}

func (o *syncObserver) OnAccept(id, lid string) {
	o.mu.Lock()
	o.accepted = append(o.accepted, id+"/"+lid)
	o.mu.Unlock()
	o.onAccept <- struct{}{}
}

func (o *syncObserver) Accepted() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.accepted...)
}

// Scenario 1: a stuck listener must not block a free listener's offer.
func TestKickStuckListenerDoesNotBlockFreeListener(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	stuck := &blockingListener{id: "stuck", binds: map[string]bool{"S": true}, proceed: make(chan struct{}), entered: make(chan struct{})}
	free := newListener("free", "F")
	q.Register(stuck)
	q.Register(free)
	mustEnqueue(t, q, evtUntil("es", "S", clk.in(time.Hour)))
	mustEnqueue(t, q, evtUntil("ef", "F", clk.in(time.Hour)))

	if launched := q.Kick(); launched != 2 {
		t.Fatalf("Kick() launched = %d, want 2", launched)
	}

	select {
	case <-stuck.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("stuck listener's offer was never even entered (timeout)")
	}

	// free's offer must complete (accepted, recorded, counted, observed)
	// with zero participation from stuck's eventual settlement: stuck is
	// still blocked (we have not closed its proceed channel), so the ONLY
	// way SessionsInFlight can drop to 1 is free's offer settling on its
	// own.
	waitForCond(t, 5*time.Second, func() bool { return q.SessionsInFlight() == 1 })
	if !equal(free.accepted, []string{"ef"}) {
		t.Fatalf("free listener accepted = %v, want [ef]", free.accepted)
	}

	close(stuck.proceed)
	waitForCond(t, 5*time.Second, func() bool { return q.SessionsInFlight() == 0 })
}

// Scenario 2: repeated Kick() calls against a still-busy listener must skip
// it every time (Offer count pinned at 1); the instant it settles, the
// NEXT Kick() must dispatch its second queued item — the per-listener FIFO
// cursor survives Kick()'s async settlement path.
func TestKickRepeatedKicksAgainstBusyListenerThenNextItem(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	l := newKickSequentialListener("h", "T")
	q.Register(l)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))
	mustEnqueue(t, q, evtUntil("e2", "T", clk.in(time.Hour)))

	entered1, release1 := l.arm()
	if launched := q.Kick(); launched != 1 {
		t.Fatalf("first Kick() launched = %d, want 1 (e1)", launched)
	}
	select {
	case <-entered1:
	case <-time.After(5 * time.Second):
		t.Fatal("listener never entered its first offer (e1) (timeout)")
	}

	for i := range 3 {
		if launched := q.Kick(); launched != 0 {
			t.Fatalf("Kick() #%d while e1 still outstanding launched = %d, want 0 (listener busy)", i, launched)
		}
	}
	if got := l.offerCount(); got != 1 {
		t.Fatalf("Offer invocation count = %d while e1 still outstanding, want 1", got)
	}

	// Arm the SECOND offer's gate before releasing the first, so there is no
	// window in which a re-offer could be entered with no gate armed.
	entered2, release2 := l.arm()
	release1()
	waitForCond(t, 5*time.Second, func() bool { return q.SessionsInFlight() == 0 })

	if launched := q.Kick(); launched != 1 {
		t.Fatalf("Kick() after e1 settled launched = %d, want 1 (e2)", launched)
	}
	select {
	case <-entered2:
	case <-time.After(5 * time.Second):
		t.Fatal("listener never entered its second offer (e2) (timeout)")
	}
	release2()
	waitForCond(t, 5*time.Second, func() bool { return q.SessionsInFlight() == 0 })
	if got := l.offerCount(); got != 2 {
		t.Fatalf("Offer invocation count = %d after both settled, want 2", got)
	}
}

// Scenario 3: two offers launched by the same Kick() call, started in
// overlapping windows, must each independently persist their own
// accept/counter/observer signal correctly regardless of which finishes
// first — proving no code assumes "everything launched by one Kick() call
// records together."
func TestKickPerOfferPhase3RecordsIndependentlyOutOfOrder(t *testing.T) {
	clk := newClock()
	obs := newSyncObserver(2)
	q := newQueue(t, clk, WithObserver(obs))
	l1 := newOnlyIDBlockingListener("l1", "T", "e1")
	l2 := newOnlyIDBlockingListener("l2", "T", "e2")
	q.Register(l1)
	q.Register(l2)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))
	mustEnqueue(t, q, evtUntil("e2", "T", clk.in(time.Hour)))

	if launched := q.Kick(); launched != 2 {
		t.Fatalf("Kick() launched = %d, want 2 (no serialize mark; both offers overlap)", launched)
	}

	select {
	case <-l1.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("l1 never entered its offer (e1) (timeout)")
	}
	select {
	case <-l2.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("l2 never entered its offer (e2) (timeout)")
	}

	// Release e2's offer FIRST even though e1 was launched first (and is
	// still blocked): it must be recorded independently and immediately.
	close(l2.proceed)
	select {
	case <-obs.onAccept:
	case <-time.After(5 * time.Second):
		t.Fatal("first OnAccept signal never fired (timeout)")
	}
	if got := obs.Accepted(); !equal(got, []string{"e2/l2"}) {
		t.Fatalf("accepted signals so far = %v, want [e2/l2] only (e1 still outstanding)", got)
	}

	close(l1.proceed)
	select {
	case <-obs.onAccept:
	case <-time.After(5 * time.Second):
		t.Fatal("second OnAccept signal never fired (timeout)")
	}
	if got := obs.Accepted(); !equal(got, []string{"e2/l2", "e1/l1"}) {
		t.Fatalf("final accepted signals = %v, want [e2/l2 e1/l1] (independent, out-of-order recording)", got)
	}
}

// Scenario 4 (must-fix from round-1 review): a Kick()-specific counterpart
// to TestSerializeOccupancyWithholdsSuccessorFromConcurrentPhase2
// (serialize_test.go). Additionally proves the property Dispatch() cannot
// exercise at all: a Kick() call issued WHILE the occupant is still
// unreleased (Kick() having already returned once without waiting) must
// still correctly withhold the successor, and a LATER Kick() call's phase 1
// must correctly see the occupant released once its own independent
// goroutine — not any Kick() call itself — actually settles it.
func TestKickSerializeOccupancyAcrossAsyncSettlement(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk, WithSerializeTypes("shutdown"))
	l1 := newOnlyIDBlockingListener("l1", "shutdown", "e1")
	l2 := newOnlyIDBlockingListener("l2", "shutdown", "e2")
	q.Register(l1)
	q.Register(l2)
	mustEnqueue(t, q, evtUntil("e1", "shutdown", clk.in(time.Hour)))
	mustEnqueue(t, q, evtUntil("e2", "shutdown", clk.in(time.Hour)))

	if launched := q.Kick(); launched != 1 {
		t.Fatalf("first Kick() launched = %d, want 1 (only l1/e1 — e2 withheld by occupancy)", launched)
	}
	select {
	case <-l1.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("l1 never entered its offer (e1) (timeout)")
	}

	// e1 (the occupant) is still unreleased: a Kick() call issued now must
	// launch nothing for l2/e2.
	if launched := q.Kick(); launched != 0 {
		t.Fatalf("second Kick() while e1 unreleased launched = %d, want 0", launched)
	}
	select {
	case <-l2.entered:
		t.Fatal("l2 was entered while e1 (the occupant) was still unreleased")
	case <-time.After(200 * time.Millisecond):
		// expected: l2 has not been entered
	}

	close(l1.proceed)
	waitForCond(t, 5*time.Second, func() bool { return q.SessionsInFlight() == 0 })

	// e1 has now settled on its OWN independent goroutine's schedule — never
	// synchronously before any Kick() call returned. A fresh Kick() call's
	// phase 1 must correctly re-derive occupancy from that live state and
	// launch l2/e2.
	if launched := q.Kick(); launched != 1 {
		t.Fatalf("third Kick() launched = %d, want 1 (e2, now the occupant)", launched)
	}
	select {
	case <-l2.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("l2 was never offered e2 even after e1 released (timeout)")
	}
	close(l2.proceed)
	waitForCond(t, 5*time.Second, func() bool { return q.SessionsInFlight() == 0 })
}

// Scenario 5 (b): the shutdown-ordering primitive (WaitForInFlightDrain)
// actually gates correctly. Half (a) — context cancellation unwinding a
// genuinely stuck subprocess call mid-Offer — belongs to the listener
// implementation that owns that ctx (roleListener/wireclient in
// internal/orchestrator and cmd/pg-router), not to this package:
// Listener.Offer takes no context argument at all, so there is nothing for
// this package's own tests to cancel. See this package's design doc,
// "Shutdown ordering."
func TestWaitForInFlightDrainReturnsTrueOnceOfferSettles(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	l := &blockingListener{id: "h", binds: map[string]bool{"T": true}, proceed: make(chan struct{}), entered: make(chan struct{})}
	q.Register(l)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))

	if launched := q.Kick(); launched != 1 {
		t.Fatalf("Kick() launched = %d, want 1", launched)
	}
	select {
	case <-l.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("offer never entered (timeout)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	drainDone := make(chan bool, 1)
	go func() { drainDone <- q.WaitForInFlightDrain(ctx, time.Millisecond) }()

	// Must not report drained while the offer is still blocked.
	select {
	case <-drainDone:
		t.Fatal("WaitForInFlightDrain returned before the in-flight offer settled")
	case <-time.After(50 * time.Millisecond):
		// expected: still waiting
	}

	close(l.proceed)
	select {
	case ok := <-drainDone:
		if !ok {
			t.Fatal("WaitForInFlightDrain returned false; want true once the offer settled")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForInFlightDrain never returned after the offer settled (timeout)")
	}
	if got := q.SessionsInFlight(); got != 0 {
		t.Fatalf("SessionsInFlight() = %d after drain returned true, want 0", got)
	}
}

// Scenario 5 (b), the other outcome: if the offer never settles within the
// bound, WaitForInFlightDrain must report false rather than claim drained —
// this is exactly what tells a caller "do not close the store yet," so a
// caller correctly gating storeClose() on this primitive never writes after
// close.
func TestWaitForInFlightDrainTimesOutWhileOfferOutstanding(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	l := &blockingListener{id: "h", binds: map[string]bool{"T": true}, proceed: make(chan struct{}), entered: make(chan struct{})}
	q.Register(l)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))
	t.Cleanup(func() { close(l.proceed) }) // release the blocked goroutine so it doesn't leak past this test

	if launched := q.Kick(); launched != 1 {
		t.Fatalf("Kick() launched = %d, want 1", launched)
	}
	select {
	case <-l.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("offer never entered (timeout)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if got := q.WaitForInFlightDrain(ctx, time.Millisecond); got {
		t.Fatal("WaitForInFlightDrain = true, want false: the offer never settled within the bound")
	}
	// The offer's own accept write must NOT have happened yet — proceeding
	// to storeClose() on a FALSE return is exactly the write-after-close
	// hazard this primitive exists to let a caller avoid being blind to.
	if got := q.delivered.Load(); got != 0 {
		t.Fatalf("q.delivered = %d, want 0 (offer never settled within the bound)", got)
	}
}

// Scenario 6: a Kick() counterpart to
// TestCrashWindowRedeliversAcceptedEventAtLeastOnce (crash_test.go),
// confirming at-least-once redelivery still holds when a LONG-RUNNING
// in-flight offer was dispatched via Kick() and was still outstanding when
// "crash" (loss of the durable accept record, dropAcceptStore) struck.
func TestKickCrashWindowRedeliversLongRunningInFlightOfferAtLeastOnce(t *testing.T) {
	mem := NewMemStore()
	store := &dropAcceptStore{inner: mem}

	q1, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	l1 := &blockingListener{id: "h", binds: map[string]bool{"T": true}, proceed: make(chan struct{}), entered: make(chan struct{})}
	q1.Register(l1)
	mustEnqueue(t, q1, evtUntil("e1", "T", time.Now().Add(time.Hour)))

	if launched := q1.Kick(); launched != 1 {
		t.Fatalf("Kick() launched = %d, want 1", launched)
	}
	select {
	case <-l1.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("offer never entered (timeout) — this is the long-running in-flight offer this test needs")
	}

	// Release it: the offer accepts, but its durable accept record is lost
	// to the crash window (dropAcceptStore) — the same crash-window shape
	// TestCrashWindowRedeliversAcceptedEventAtLeastOnce exercises via
	// Dispatch(), here exercised via a Kick()-launched offer instead.
	close(l1.proceed)
	waitForCond(t, 5*time.Second, func() bool { return q1.SessionsInFlight() == 0 })

	// Simulate restart: a fresh queue replays the SAME durable log (which
	// holds the enqueue record but not the lost accept record).
	q2, err := New(mem)
	if err != nil {
		t.Fatal(err)
	}
	if q2.DepthByType()["T"] != 1 {
		t.Fatalf("accepted event was LOST across restart: %v", q2.DepthByType())
	}
	l2 := newListener("h", "T")
	q2.Register(l2)
	q2.Dispatch() // the one redelivery for this crash window
	q2.Dispatch() // must NOT re-offer again within this restart (re-accepted)
	if !equal(l2.offered, []string{"e1"}) {
		t.Fatalf("redelivery count = %v, want one re-offer of e1 for this crash window", l2.offered)
	}
}

// Scenario 7: driven through Kick() the way runRunUntilIdle will be
// (cmd/pg-router/run.go, out of this package's scope) — one listener
// stuck, several others with backlog — drain must fully process every
// non-stuck listener's backlog without waiting on the stuck one, and must
// never report Idle() while the stuck offer is outstanding.
func TestKickDrainReachesIdleWithoutWaitingOnStuckListener(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	stuck := &blockingListener{id: "stuck", binds: map[string]bool{"S": true}, proceed: make(chan struct{}), entered: make(chan struct{})}
	q.Register(stuck)
	mustEnqueue(t, q, evtUntil("stuckEvt", "S", clk.in(time.Hour)))

	const n = 5
	for i := range n {
		typ := fmt.Sprintf("T%d", i)
		q.Register(newListener(fmt.Sprintf("f%d", i), typ))
		mustEnqueue(t, q, evt(fmt.Sprintf("e%d", i), typ)) // born-expired: retires promptly once accepted
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		q.Kick()
		q.Expire()
		if q.Idle() {
			t.Fatal("Idle() reported true while the stuck listener's offer is still outstanding")
		}
		if q.delivered.Load() == int64(n) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("drain did not fully process the fast listeners' backlog in time (delivered=%d, want %d)", q.delivered.Load(), n)
		}
		time.Sleep(time.Millisecond)
	}

	select {
	case <-stuck.entered:
	default:
		t.Fatal("the stuck listener was never even entered")
	}
	if q.Idle() {
		t.Fatal("Idle() reported true even though the stuck listener's offer is still outstanding")
	}
	close(stuck.proceed)
	waitForCond(t, 5*time.Second, func() bool { return q.SessionsInFlight() == 0 })
}

// Scenario 9: Kick()'s return value means "launched," not "accepted."
func TestKickReturnValueMeansLaunchedNotAccepted(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	fast := newListener("fast", "T")
	slow := &blockingListener{id: "slow", binds: map[string]bool{"T": true}, proceed: make(chan struct{}), entered: make(chan struct{})}
	q.Register(fast)
	q.Register(slow)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))

	launched := q.Kick()
	if launched != 2 {
		t.Fatalf("Kick() launched = %d, want 2 (fast+slow, fanned out over the same event)", launched)
	}
	// slow's offer has not settled yet (its proceed channel is still open),
	// so the accepted count must be strictly less than the launched count —
	// they are not the same number, which is Kick's whole point.
	if got := q.delivered.Load(); got >= int64(launched) {
		t.Fatalf("q.delivered = %d immediately after Kick() returned %d launched; want delivered < launched", got, launched)
	}

	select {
	case <-slow.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("slow listener's offer was never even entered (timeout)")
	}
	if got := q.delivered.Load(); got >= int64(launched) {
		t.Fatalf("q.delivered = %d while slow's offer is still blocked; want < %d", got, launched)
	}

	close(slow.proceed)
	waitForCond(t, 5*time.Second, func() bool { return q.delivered.Load() == int64(launched) })
}

// Baseline sanity check (not one of the design doc's 9 numbered scenarios,
// but useful coverage of the ordinary, non-concurrent accept path through
// the newly-shared settleOfferLocked helper): a single listener, single
// event, accepted via Kick() — proves Kick() produces the SAME settlement
// outcome Dispatch() would for the simple case.
func TestKickAcceptsSingleOfferAndRecordsSettlement(t *testing.T) {
	clk := newClock()
	obs := newSyncObserver(1)
	q := newQueue(t, clk, WithObserver(obs))
	l := newListener("h", "T")
	q.Register(l)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))

	if launched := q.Kick(); launched != 1 {
		t.Fatalf("Kick() launched = %d, want 1", launched)
	}
	select {
	case <-obs.onAccept:
	case <-time.After(5 * time.Second):
		t.Fatal("OnAccept signal never fired (timeout)")
	}
	waitForCond(t, 5*time.Second, func() bool { return q.SessionsInFlight() == 0 })
	if !equal(l.accepted, []string{"e1"}) {
		t.Fatalf("accepted = %v, want [e1]", l.accepted)
	}
	if got := obs.Accepted(); !equal(got, []string{"e1/h"}) {
		t.Fatalf("observer accepted signals = %v, want [e1/h]", got)
	}
	if got := q.delivered.Load(); got != 1 {
		t.Fatalf("q.delivered = %d, want 1", got)
	}
}
