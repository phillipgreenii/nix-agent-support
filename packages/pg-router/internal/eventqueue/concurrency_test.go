package eventqueue

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- re-entrancy / concurrency test doubles -------------------------------

// callbackListener runs a hook from inside Offer (the accept path) and then
// accepts. It drives the re-entrancy cases the pg2-56186 fix enables: because
// Dispatch now offers with q.mu RELEASED, the hook may legally call back into
// the queue (Enqueue / Dispatch / Expire) from within Offer.
type callbackListener struct {
	id       string
	binds    map[string]bool
	onOffer  func(e Event) // runs before accepting; MAY re-enter the queue
	offered  []string
	accepted []string
}

func (l *callbackListener) ID() string           { return l.id }
func (l *callbackListener) Matches(e Event) bool { return l.binds[e.Type] }
func (l *callbackListener) Offer(o Offering) OfferResult {
	e := o.Event
	l.offered = append(l.offered, e.ID)
	if l.onOffer != nil {
		l.onOffer(e)
	}
	l.accepted = append(l.accepted, e.ID)
	return OfferResult{Accepted: true, Decline: DeclineNone}
}

// concurrentListener is a mutex-guarded listener safe to Offer from several
// goroutines at once (the -race concurrency test dispatches concurrently).
type concurrentListener struct {
	id  string
	mu  sync.Mutex
	got map[string]int // event id -> times offered/accepted
}

func (l *concurrentListener) ID() string           { return l.id }
func (l *concurrentListener) Matches(e Event) bool { return true } // binds all
func (l *concurrentListener) Offer(o Offering) OfferResult {
	l.mu.Lock()
	l.got[o.Event.ID]++
	l.mu.Unlock()
	return OfferResult{Accepted: true, Decline: DeclineNone}
}

// --- pg2-56186: lock released across Offer + accept write -----------------

// TEST A (RED on the old code). A synchronous listener whose accept path
// re-enters the queue (Enqueue a follow-on event from inside Offer) must NOT
// deadlock. The old Dispatch held the non-reentrant q.mu across Offer, so this
// Enqueue blocked forever; releasing the lock across Offer fixes it. Guarded by
// a timeout so a regression fails fast instead of hanging the suite.
func TestDispatchReentrantEnqueueNoDeadlock(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	l := &callbackListener{id: "h", binds: map[string]bool{"T": true}}
	l.onOffer = func(e Event) {
		if e.ID == "trigger" {
			// The acceptance path injects a follow-on event back into the queue —
			// the classic re-entry (push-inject / ingest-event on accept). Old code:
			// self-deadlock on q.mu here.
			if _, err := q.Enqueue(evtUntil("followon", "T", clk.in(time.Hour))); err != nil {
				t.Errorf("re-entrant Enqueue from inside Offer: %v", err)
			}
		}
	}
	q.Register(l)
	mustEnqueue(t, q, evtUntil("trigger", "T", clk.in(time.Hour)))

	done := make(chan struct{})
	go func() {
		defer close(done)
		q.Dispatch() // OLD CODE DEADLOCKS HERE on the re-entrant Enqueue
		q.Dispatch() // deliver the follow-on event injected on accept
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Dispatch deadlocked on a re-entrant Enqueue from inside Offer (timeout)")
	}
	if !equal(l.accepted, []string{"trigger", "followon"}) {
		t.Fatalf("accepted = %v, want [trigger followon] (follow-on injected on accept, delivered next pass)", l.accepted)
	}
}

// TEST B (concurrency, run under -race). Concurrent Enqueue while a Dispatch
// goroutine runs must not deadlock or race, and every enqueued event must be
// delivered at least once (INV-EVT-1). NARROWED from 3 dispatcher goroutines
// to exactly 1 (Task 6.1, operator ruling 2026-09-16): this test's own
// introducing commit (bead pg2-56186, the fix for Dispatch holding q.mu across
// the listener Offer callback — a deadlock/serialization bug) was about
// Enqueue racing Dispatch, not about multiple concurrent Dispatch() callers;
// the extra dispatcher goroutines were an incidental broadening past what
// that bug required, never a deliberate assertion that multiple concurrent
// Dispatch() callers must be supported. This test's actual assertion
// (at-least-once delivery under concurrent Enqueue) is unaffected by the
// dispatcher count. A genuinely-concurrent Dispatch() collision against a
// shared listener is now Queue.inFlight's job to guard (Task 6.1, bead
// pg2-84o3m.31 — see TestDispatchConcurrentPassesSkipListenerAlreadyInFlight
// below), not this test's concern. Guarded by a timeout.
func TestConcurrentEnqueueDuringDispatchNoRace(t *testing.T) {
	q, err := New(NewMemStore()) // real clock; a far-future expiresAt so nothing expires mid-test
	if err != nil {
		t.Fatal(err)
	}
	l := &concurrentListener{id: "h", got: map[string]int{}}
	q.Register(l)

	const n = 300
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		// Producer: concurrent Enqueue while the dispatcher runs.
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < n; i++ {
				if _, err := q.Enqueue(evtUntil(fmt.Sprintf("e%d", i), "T", time.Now().Add(time.Hour))); err != nil {
					t.Errorf("Enqueue: %v", err)
				}
			}
		}()
		// One dispatcher + read-only observers hammering the queue concurrently.
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < n; i++ {
				q.Dispatch()
				q.Expire()
				_ = q.DepthByType()
				_ = q.Idle()
			}
		}()
		wg.Wait()
		// Deterministic final drain (single-threaded now): dispatch until idle.
		for i := 0; i < n+10; i++ {
			if q.Idle() {
				break
			}
			q.Dispatch()
		}
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent Enqueue during Dispatch deadlocked (timeout)")
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.got) != n {
		t.Fatalf("delivered %d distinct events, want %d (at-least-once for every enqueue)", len(l.got), n)
	}
}

// An entry that leaves the queue DURING the (now unlocked) Offer used to be
// exercised here via a re-entrant Dispatch that accepted it and, with early
// eviction on, EVICTED it before the outer pass re-acquired to record — the
// documented mid-dispatch eviction decision. Task 6.1 (operator ruling,
// 2026-09-16, reverting an intervening panic-based ruling from 2026-09-15)
// supersedes that: the nested Dispatch call now targets the SAME listener
// ("h") whose offer the outer pass has not yet settled, so Queue.inFlight
// (INV-CONC-1) SKIPS it in phase 1 — no headFor call, no Offer call — before
// the nested pass can race the outer pass to accept/evict the entry first.
// REWRITTEN (not left unmodified, per this bead's ruling) to assert the
// nested Dispatch call is silently skipped (0 accepted, no Offer call) and
// the OUTER pass's own offer completes normally, instead of the old
// "acceptance skipped by mid-offer eviction" assertions, which described a
// code path that no longer exists.
func TestDispatchEntryEvictedMidOfferSkipsRecord(t *testing.T) {
	clk := newClock()
	obs := &recordingObserver{}
	q := newQueue(t, clk, WithObserver(obs), WithEarlyEviction())
	l := &callbackListener{id: "h", binds: map[string]bool{"T": true}}
	reentered := false
	var nestedAccepted int
	l.onOffer = func(e Event) {
		if !reentered {
			reentered = true
			// The nested pass finds "h" already in-flight (the outer offer
			// is still outstanding) and is skipped in phase 1 — no headFor
			// call, no Offer call, no acceptance.
			nestedAccepted = q.Dispatch()
		}
	}
	q.Register(l)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))

	if n := q.Dispatch(); n != 1 {
		t.Fatalf("outer accepted count = %d, want 1 (the nested pass was skipped; the outer offer completes normally)", n)
	}
	if nestedAccepted != 0 {
		t.Fatalf("nested Dispatch accepted count = %d, want 0 (listener was busy, skipped this pass)", nestedAccepted)
	}
	if !equal(l.offered, []string{"e1"}) {
		t.Fatalf("offered = %v, want [e1] (the nested pass never reached Offer; only the outer offer happened)", l.offered)
	}
	if len(obs.accepted) != 1 {
		t.Fatalf("recorded acceptances = %v, want exactly one (the outer pass's — the nested pass never reached Offer)", obs.accepted)
	}
	if q.DepthByType()["T"] != 0 {
		t.Fatalf("accepted entry still retained (should have early-evicted after the outer accept): %v", q.DepthByType())
	}
}

// A re-entrant Dispatch from inside Offer used to be exercised here racing the
// outer pass to record the same head first, with the outer pass then SKIPPING
// on re-acquire (already-accepted, INV-EVT-2 idempotency). Task 6.1 (operator
// ruling, 2026-09-16, reverting an intervening panic-based ruling from
// 2026-09-15) supersedes that: the nested pass now targets the same listener
// ("h") the outer pass has not yet settled, so it is SKIPPED in phase 1
// (INV-CONC-1) before it ever reaches Offer — it never gets a chance to race
// the outer pass to record first. REWRITTEN (not left unmodified) to assert
// the nested Dispatch call is silently skipped (0 accepted, no Offer call);
// at-most-once acceptance still holds (obs.accepted has exactly one entry),
// now because the nested offer never happens at all rather than because a
// duplicate is idempotently absorbed.
func TestDispatchReentrantDispatchAtMostOnceAccept(t *testing.T) {
	clk := newClock()
	obs := &recordingObserver{}
	q := newQueue(t, clk, WithObserver(obs))
	l := &callbackListener{id: "h", binds: map[string]bool{"T": true}}
	reentered := false
	var nestedAccepted int
	l.onOffer = func(e Event) {
		if !reentered {
			reentered = true
			nestedAccepted = q.Dispatch() // nested pass: skipped in phase 1, never reaches Offer
		}
	}
	q.Register(l)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))

	q.Dispatch()

	if !equal(l.offered, []string{"e1"}) {
		t.Fatalf("offered = %v, want [e1] (the nested pass was skipped in phase 1 before ever reaching Offer, so only the outer offer happened)", l.offered)
	}
	if nestedAccepted != 0 {
		t.Fatalf("nested Dispatch accepted count = %d, want 0 (listener was busy, skipped this pass)", nestedAccepted)
	}
	if len(obs.accepted) != 1 {
		t.Fatalf("recorded acceptances = %v, want exactly one e1/h (at-most-once, now because the nested offer never happens rather than an idempotent skip)", obs.accepted)
	}
}

// --- Task 2.2: custody pin -------------------------------------------------

// blockingListener blocks inside Offer until told to proceed, closing
// `entered` the instant it starts blocking — the design's own pinned
// acceptance test for Task 2.2 needs an offer that is PROVABLY still
// outstanding in phase 2 while the test reads status-shaped state.
type blockingListener struct {
	id      string
	binds   map[string]bool
	proceed chan struct{}
	entered chan struct{}
}

func (l *blockingListener) ID() string           { return l.id }
func (l *blockingListener) Matches(e Event) bool { return l.binds[e.Type] }
func (l *blockingListener) Offer(Offering) OfferResult {
	close(l.entered)
	<-l.proceed
	return OfferResult{Accepted: true, Decline: DeclineNone}
}

// TestDispatch_CustodyPinnedDuringBlockingOffer is Task 2.2's own pinned
// acceptance test: a listener double that blocks inside Offer while the test
// issues a status-shaped read asserts SessionsInFlight() == 1 and
// len(custody) == 1 — custody is read LIVE under q.mu, never cached in a
// tickSnapshot, so it must reflect the offer that is still outstanding right
// now. Once the offer is unblocked and the pass concludes, both drop back to
// zero (custody deleted in phase 3, "accept is not settle" notwithstanding —
// THIS pass's offer is over either way).
func TestDispatch_CustodyPinnedDuringBlockingOffer(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	l := &blockingListener{id: "h", binds: map[string]bool{"T": true}, proceed: make(chan struct{}), entered: make(chan struct{})}
	q.Register(l)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))

	done := make(chan struct{})
	go func() {
		defer close(done)
		q.Dispatch()
	}()

	select {
	case <-l.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Offer never entered its blocking wait (timeout)")
	}

	if got := q.SessionsInFlight(); got != 1 {
		t.Fatalf("SessionsInFlight() = %d while an offer is still blocked mid-pass, want 1", got)
	}
	q.mu.Lock()
	gotCustody := len(q.custody)
	q.mu.Unlock()
	if gotCustody != 1 {
		t.Fatalf("len(custody) = %d while an offer is still blocked mid-pass, want 1", gotCustody)
	}

	close(l.proceed)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Dispatch did not return after Offer unblocked (timeout)")
	}

	if got := q.SessionsInFlight(); got != 0 {
		t.Fatalf("SessionsInFlight() = %d after Dispatch returned, want 0 (custody deleted in phase 3)", got)
	}
}

// boundedCustodyListener declines (busy) every offer and, from INSIDE its own
// Offer call, asserts that custody never exceeds the registered listener
// count — a pass-boundary read would see it vacuously at 0 (before the pass)
// or at the pass's full size (after phase 1, before phase 3), so the bound
// only means something asserted from mid-phase-2, exactly like this.
type boundedCustodyListener struct {
	id string
	q  *Queue
	t  *testing.T
	n  int // len(q.listeners) at test-setup time
}

func (l *boundedCustodyListener) ID() string           { return l.id }
func (l *boundedCustodyListener) Matches(e Event) bool { return true }
func (l *boundedCustodyListener) Offer(Offering) OfferResult {
	if got := l.q.SessionsInFlight(); got > l.n {
		l.t.Fatalf("custody size %d exceeded listener count %d mid-phase-2", got, l.n)
	}
	return OfferResult{Accepted: false, Decline: DeclineBusy}
}

// TestDispatch_CustodyBoundedByListenerCountUnderDeclineHeavyLoad proves the
// <= len(q.listeners) custody bound under a fully decline-heavy pass (every
// registered listener declines busy), asserted from INSIDE phase 2 for every
// one of them.
func TestDispatch_CustodyBoundedByListenerCountUnderDeclineHeavyLoad(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	const n = 5
	for i := 0; i < n; i++ {
		q.Register(&boundedCustodyListener{id: fmt.Sprintf("h%d", i), q: q, t: t, n: n})
	}
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))

	q.Dispatch()
}

// --- Task 2.3, Step 2.3.5: hooks fire with NO queue lock held --------------

// reentrantHookObserver is the proof instrument for Step 2.3.5: EVERY hook
// re-enters the queue (q.DepthByType() + q.Enqueue(...)) exactly once. Under
// the OLD code (a hook firing while q.mu is still held) this self-deadlocks
// on the very first hook call, since sync.Mutex is not reentrant — a
// deadlock/timeout, not a clean assertion failure, is the documented RED for
// this test. depth bounds the re-entry to ONE level: the nested Enqueue's
// own hook call would otherwise refire this same re-entry forever. The
// strict-leaf rule this proves (no queue re-entry from a hook, synchronously
// or via any registered callback) binds the PRODUCTION composite
// (metrics.Emitter) only — this double is deliberately the opposite, on
// purpose, as the instrument that proves the lock really is released.
type reentrantHookObserver struct {
	q     *Queue
	depth int32

	mu    sync.Mutex
	fired map[string]int
}

func (o *reentrantHookObserver) record(name string) {
	o.mu.Lock()
	o.fired[name]++
	o.mu.Unlock()
}

func (o *reentrantHookObserver) reenter() {
	if atomic.AddInt32(&o.depth, 1) > 1 {
		atomic.AddInt32(&o.depth, -1)
		return
	}
	defer atomic.AddInt32(&o.depth, -1)
	_ = o.q.DepthByType()
	_, _ = o.q.Enqueue(evtUntil("reentrant-probe", "T", time.Now().Add(time.Hour)))
}

func (o *reentrantHookObserver) OnEnqueue(Event) {
	o.record("enqueue")
	o.reenter()
}

func (o *reentrantHookObserver) OnAccept(string, string) {
	o.record("accept")
	o.reenter()
}

func (o *reentrantHookObserver) OnUnconsumedExpired(string) {
	o.record("expired")
	o.reenter()
}

func (o *reentrantHookObserver) OnDeclined(string, string, string) {
	o.record("declined")
	o.reenter()
}

func (o *reentrantHookObserver) OnDeduped(string) {
	o.record("deduped")
	o.reenter()
}

func (o *reentrantHookObserver) OnDispatchFailure(string) {
	o.record("dispatch_failed")
	o.reenter()
}

// TestObserverHooksFireWithQueueUnlocked is Task 2.3's required RED test
// (Step 2.3.5), modeled on TestDispatchReentrantEnqueueNoDeadlock above: a
// TEST-ONLY observer whose every hook calls q.DepthByType() and
// q.Enqueue(...) must complete under timeout with -race, driving a sequence
// that hits every one of the four digest:perf-F1 sites — Enqueue's OnEnqueue
// exit, Enqueue's Deduped exit, Enqueue's own stale-retire exit, Dispatch
// phase 3 (both OnAccept and OnDeclined), and Expire's sweep.
func TestObserverHooksFireWithQueueUnlocked(t *testing.T) {
	obs := &reentrantHookObserver{fired: map[string]int{}}
	q, err := New(NewMemStore(), WithObserver(obs)) // real clock: reenter() uses time.Now()
	if err != nil {
		t.Fatal(err)
	}
	obs.q = q
	accepting := newListener("h1", "T")
	q.Register(accepting)
	declining := newListener("h2", "U")
	declining.neverAccept = true
	q.Register(declining)

	done := make(chan struct{})
	go func() {
		defer close(done)
		future := time.Now().Add(time.Hour)
		if _, err := q.Enqueue(evtUntil("e1", "T", future)); err != nil { // OnEnqueue
			t.Errorf("Enqueue e1: %v", err)
		}
		if _, err := q.Enqueue(evtUntil("e1", "T", future)); err != nil { // dup -> OnDeduped
			t.Errorf("Enqueue e1 dup: %v", err)
		}
		if _, err := q.Enqueue(evt("orphan", "U")); err != nil { // born expired -> OnEnqueue
			t.Errorf("Enqueue orphan: %v", err)
		}
		q.Dispatch() // e1 -> accept (OnAccept); orphan -> terminal decline (OnDeclined), settles
		// Re-emit the now-settled-but-not-yet-swept "orphan" id BEFORE Expire
		// runs: Enqueue's OWN stale-retire path (distinct from Expire's sweep
		// below) fires OnUnconsumedExpired too.
		if _, err := q.Enqueue(evt("orphan", "U")); err != nil {
			t.Errorf("Enqueue orphan re-emit: %v", err)
		}
		q.Expire() // sweeps the freshly re-emitted, still-unconsumed "orphan" -> OnUnconsumedExpired
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("observer hook re-entry deadlocked with q.mu still held (timeout)")
	}

	obs.mu.Lock()
	defer obs.mu.Unlock()
	for _, hook := range []string{"enqueue", "deduped", "declined", "accept", "expired"} {
		if obs.fired[hook] == 0 {
			t.Errorf("hook %q never fired during the sequence; fired=%v", hook, obs.fired)
		}
	}
}

// --- Task 2.3, Step 2.3.6: global delivery counters -------------------------

// TestGlobalCounters_ConcurrentIncrementNoRace is Task 2.3's required RED
// test (Step 2.3.6): the pool-wide atomic delivered counter, read WITHOUT
// q.mu, must survive concurrent Dispatch passes under -race and end up
// exactly right — every enqueued event is accepted EXACTLY ONCE (INV-EVT-2).
// NARROWED from 4 dispatcher goroutines to exactly 1 (Task 6.1, operator
// ruling 2026-09-16): this test's own introducing commit (Task 2.3, bead
// pg2-84o3m.22, adding the pool-wide atomic delivered/declined counters) used
// "N goroutines calling Dispatch()" purely as a mechanism to generate
// concurrent WRITES to the new atomic.Int64 counters for -race to exercise —
// its original form (commit 0d7d1993) has no Enqueue-during-Dispatch concern
// at all and never needed multiple dispatchers, only *some* concurrent
// activity hitting the counters. This test's actual assertion (exact global
// counter correctness) is unaffected by the dispatcher count.
func TestGlobalCounters_ConcurrentIncrementNoRace(t *testing.T) {
	q, err := New(NewMemStore()) // real clock; far-future expiresAt so nothing expires mid-test
	if err != nil {
		t.Fatal(err)
	}
	l := &concurrentListener{id: "h", got: map[string]int{}}
	q.Register(l)

	const n = 200
	for i := 0; i < n; i++ {
		if _, err := q.Enqueue(evtUntil(fmt.Sprintf("g%d", i), "T", time.Now().Add(time.Hour))); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < n; i++ {
				q.Dispatch()
			}
		}()
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent Dispatch deadlocked (timeout)")
	}

	if got := q.delivered.Load(); got != int64(n) {
		t.Fatalf("global delivered = %d, want %d (each event accepted exactly once, INV-EVT-2)", got, n)
	}
}

// --- Task 6.1: per-listener in-flight tracking (bead pg2-84o3m.31) ---------

// slowListener blocks inside Offer until its proceed channel is closed, and
// counts how many times Offer runs — the instrument for the duplicate-offer
// race this task closes: two concurrent Dispatch passes racing against the
// SAME still-outstanding offer must invoke Offer only once between them, not
// once each. entered closes the first time an Offer call starts blocking, so
// the test can observe the race while it is still outstanding.
type slowListener struct {
	id      string
	binds   map[string]bool
	proceed chan struct{}
	entered chan struct{}

	mu     sync.Mutex
	offers int
}

func (l *slowListener) ID() string           { return l.id }
func (l *slowListener) Matches(e Event) bool { return l.binds[e.Type] }
func (l *slowListener) Offer(Offering) OfferResult {
	l.mu.Lock()
	l.offers++
	first := l.offers == 1
	l.mu.Unlock()
	if first {
		close(l.entered)
	}
	<-l.proceed
	return OfferResult{Accepted: false, Decline: DeclineBusy}
}

func (l *slowListener) offerCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.offers
}

// TestDispatchConcurrentPassesSkipListenerAlreadyInFlight is this task's
// pinned RED-first test (bead pg2-84o3m.31, INV-CONC-1: "one outstanding
// offer per handler" at a time; operator ruling 2026-09-16, reverting an
// intervening panic-based ruling from 2026-09-15). Before this task, Queue
// tracked outstanding offers only by per-attempt dispatch id (custody) —
// nothing indexed by LISTENER — so two concurrent Dispatch passes could each
// snapshot the same (listener, event) pair in phase 1 before either recorded
// an outcome in phase 3, offering that listener the same event twice while
// its first offer was still outstanding. Queue.inFlight (keyed by listener
// id) closes that: phase 1 now SKIPS a listener already present in inFlight
// — no headFor call, no pendingOffer minted, no Offer call — so the
// genuinely-concurrent second pass is a no-op for that listener this pass
// instead of quietly double-offering; the listener is naturally offered
// again on a later pass once its first offer settles.
func TestDispatchConcurrentPassesSkipListenerAlreadyInFlight(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	l := &slowListener{id: "h", binds: map[string]bool{"T": true}, proceed: make(chan struct{}), entered: make(chan struct{})}
	q.Register(l)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		q.Dispatch() // blocks in l.Offer until l.proceed closes below
	}()

	select {
	case <-l.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first Dispatch pass never reached the slow listener's Offer (timeout)")
	}

	// A second, genuinely concurrent pass: its phase 1 runs in a different
	// goroutine while the first pass's offer for "h" is still outstanding, so
	// it must SKIP that listener (no Offer call, 0 accepted) rather than
	// being offered the same event a second time.
	secondAcceptedCh := make(chan int, 1)
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		secondAcceptedCh <- q.Dispatch()
	}()

	select {
	case <-secondDone:
	case <-time.After(5 * time.Second):
		t.Fatal("second concurrent Dispatch pass never returned (timeout)")
	}

	if secondAccepted := <-secondAcceptedCh; secondAccepted != 0 {
		t.Fatalf("second concurrent Dispatch pass accepted count = %d, want 0 (busy listener skipped this pass while the first pass's offer was outstanding)", secondAccepted)
	}

	if got := l.offerCount(); got != 1 {
		t.Fatalf("Offer invoked %d times while the first pass's offer was outstanding, want exactly 1 (second pass skipped instead of offering again)", got)
	}

	close(l.proceed)
	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("first Dispatch pass never returned after being unblocked (timeout)")
	}

	if got := l.offerCount(); got != 1 {
		t.Fatalf("Offer invoked %d times once the first pass returned, want exactly 1 (still no duplicate offer)", got)
	}

	// inFlight is deleted in phase 3 regardless of outcome (same point custody
	// is deleted); once the decline's retry cooldown (INV-FAIL-2) elapses, a
	// later pass offers the same head again — inFlight no longer withholds it
	// once cleared.
	clk.advance(10 * time.Second)
	q.Dispatch()
	if got := l.offerCount(); got != 2 {
		t.Fatalf("Offer invoked %d times after a subsequent pass ran once inFlight cleared, want 2 (offered again)", got)
	}
}
