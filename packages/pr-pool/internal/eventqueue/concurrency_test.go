package eventqueue

import (
	"fmt"
	"sync"
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
func (l *callbackListener) Offer(e Event) bool {
	l.offered = append(l.offered, e.ID)
	if l.onOffer != nil {
		l.onOffer(e)
	}
	l.accepted = append(l.accepted, e.ID)
	return true
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
func (l *concurrentListener) Offer(e Event) bool {
	l.mu.Lock()
	l.got[e.ID]++
	l.mu.Unlock()
	return true
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

// TEST B (concurrency, run under -race). Concurrent Enqueue while several
// Dispatch goroutines run must not deadlock or race, and every enqueued event
// must be delivered at least once (INV-EVT-1). Duplicate offers under concurrent
// dispatch are tolerated (idempotent-listener contract, INV-EVT-2); the queue
// records each acceptance at most once. Guarded by a timeout.
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
		// Producer: concurrent Enqueue while dispatchers run.
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < n; i++ {
				if _, err := q.Enqueue(evtUntil(fmt.Sprintf("e%d", i), "T", time.Now().Add(time.Hour))); err != nil {
					t.Errorf("Enqueue: %v", err)
				}
			}
		}()
		// Dispatchers + read-only observers hammering the queue concurrently.
		for d := 0; d < 3; d++ {
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
		}
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

// An entry that leaves the queue DURING the (now unlocked) Offer — here a
// re-entrant Dispatch accepts it and, with early eviction on, EVICTS it before the
// outer pass re-acquires to record — has the OUTER acceptance SKIPPED (the
// documented mid-dispatch eviction decision): no second durable opAccept, no
// second observer accept, nothing to redeliver. The listener already took
// responsibility when Offer returned true.
//
// Eviction (not expiry) is what removes it, and that is forced by the contract
// rather than chosen for convenience: under INV-EVT-4 a merely EXPIRED entry is
// still RETAINED while the listener being offered it is owed that very attempt, so
// a re-entrant Expire mid-offer cannot remove it — only completing every owed
// attempt, or early eviction, can.
func TestDispatchEntryEvictedMidOfferSkipsRecord(t *testing.T) {
	clk := newClock()
	obs := &recordingObserver{}
	q := newQueue(t, clk, WithObserver(obs), WithEarlyEviction())
	l := &callbackListener{id: "h", binds: map[string]bool{"T": true}}
	reentered := false
	l.onOffer = func(e Event) {
		if !reentered {
			reentered = true
			// The nested pass accepts the same head; it is the only bound listener,
			// so maybeEvict removes the entry before the outer record phase runs.
			q.Dispatch()
		}
	}
	q.Register(l)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))

	if n := q.Dispatch(); n != 0 {
		t.Fatalf("outer accepted count = %d, want 0 (entry evicted mid-dispatch; acceptance skipped)", n)
	}
	if len(obs.accepted) != 1 {
		t.Fatalf("recorded acceptances = %v, want exactly one (the nested pass's)", obs.accepted)
	}
	if q.DepthByType()["T"] != 0 {
		t.Fatalf("evicted entry still retained: %v", q.DepthByType())
	}
}

// A re-entrant Dispatch from inside Offer records the same head first; when the
// outer pass re-acquires to record it must see the entry already accepted by this
// listener and SKIP — preserving at-most-once acceptance per (event, listener)
// even though the listener was offered the head twice (INV-EVT-2 idempotency).
func TestDispatchReentrantDispatchAtMostOnceAccept(t *testing.T) {
	clk := newClock()
	obs := &recordingObserver{}
	q := newQueue(t, clk, WithObserver(obs))
	l := &callbackListener{id: "h", binds: map[string]bool{"T": true}}
	reentered := false
	l.onOffer = func(e Event) {
		if !reentered {
			reentered = true
			q.Dispatch() // nested pass offers the same head and records it first
		}
	}
	q.Register(l)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))

	q.Dispatch()

	if !equal(l.offered, []string{"e1", "e1"}) {
		t.Fatalf("offered = %v, want [e1 e1] (outer + nested offer of the same head)", l.offered)
	}
	if len(obs.accepted) != 1 {
		t.Fatalf("recorded acceptances = %v, want exactly one e1/h (at-most-once)", obs.accepted)
	}
}
