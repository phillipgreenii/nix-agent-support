package eventqueue

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// Tests for Queue.PendingByListener (epic pg2-u1cxx0, DEC-OBS-12): per
// registered listener, how many retained matching events it has not SETTLED
// and the age of the oldest, from one lock acquisition and one pass.

func wantPending(t *testing.T, q *Queue, want map[string]ListenerPending) {
	t.Helper()
	got := q.PendingByListener()
	if len(got) != len(want) {
		t.Fatalf("PendingByListener = %v, want %v", got, want)
	}
	for id, w := range want {
		if g, ok := got[id]; !ok || g != w {
			t.Fatalf("PendingByListener[%s] = %v (present=%v), want %v (all: %v)", id, g, ok, w, got)
		}
	}
}

// Case 1: caught up reads {0,0}; one event reads {1, age}; accepted reads {0,0}
// although the event is still retained.
func TestPendingByListenerCaughtUpAndAcceptedWhileRetained(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	q.Register(newListener("h", "A"))
	wantPending(t, q, map[string]ListenerPending{"h": {}})
	mustEnqueue(t, q, evtUntil("a1", "A", clk.in(time.Hour)))
	clk.advance(7 * time.Second)
	wantPending(t, q, map[string]ListenerPending{"h": {Count: 1, OldestAge: 7 * time.Second}})
	q.Dispatch()
	wantPending(t, q, map[string]ListenerPending{"h": {}})
	if got := q.DepthByType()["A"]; got != 1 {
		t.Fatalf("retained depth = %d, want 1 (handled but retained)", got)
	}
}

// Case 2: two listeners on one type, one settles and the other does not.
func TestPendingByListenerCountsDifferPerListener(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	a := newListener("a", "T")
	b := newListener("b", "T")
	b.neverAccept = true
	q.Register(a)
	q.Register(b)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))
	mustEnqueue(t, q, evtUntil("e2", "T", clk.in(time.Hour)))
	clk.advance(3 * time.Second)
	q.Dispatch() // one event per pass per listener
	q.Dispatch()
	wantPending(t, q, map[string]ListenerPending{
		"a": {},
		"b": {Count: 2, OldestAge: 3 * time.Second},
	})
}

// Case 3: a listener registered AFTER N retained events reads N at once and
// decreases as it settles them.
func TestPendingByListenerLateRegisteredReadsRetainedBacklog(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	q.Register(newListener("early", "T"))
	for i := 0; i < 3; i++ {
		mustEnqueue(t, q, evtUntil(fmt.Sprintf("e%d", i), "T", clk.in(time.Hour)))
		clk.advance(time.Second)
	}
	q.Dispatch()
	q.Dispatch()
	q.Dispatch()
	q.Register(newListener("late", "T"))
	wantPending(t, q, map[string]ListenerPending{
		"early": {},
		"late":  {Count: 3, OldestAge: 3 * time.Second},
	})
	q.Dispatch()
	got := q.PendingByListener()["late"]
	if got.Count >= 3 {
		t.Fatalf("late listener count did not decrease after a dispatch pass: %v", got)
	}
}

// Case 4: Matches false contributes nothing.
func TestPendingByListenerUnmatchedTypeContributesNothing(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	q.Register(newListener("a", "A"))
	q.Register(newListener("b", "B"))
	mustEnqueue(t, q, evtUntil("a1", "A", clk.in(time.Hour)))
	wantPending(t, q, map[string]ListenerPending{"a": {Count: 1}, "b": {}})
}

// Case 5: evicted and retired entries are excluded; an expired-but-unsettled
// retained entry (born expired, never offered yet) IS pending.
func TestPendingByListenerEvictedExcludedExpiredUnsettledIncluded(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk, WithEarlyEviction())
	q.Register(newListener("h", "T"))
	// Born expired: ExpiresAt defaults to At, yet it is owed.
	mustEnqueue(t, q, evt("born-expired", "T"))
	wantPending(t, q, map[string]ListenerPending{"h": {Count: 1}})
	q.Dispatch() // accepted and early-evicted: gone from the queue
	wantPending(t, q, map[string]ListenerPending{"h": {}})
	if got := q.DepthByType()["T"]; got != 0 {
		t.Fatalf("early eviction left depth %d, want 0", got)
	}

	// Retired by the sweep: an event nobody matches never counts, before or after.
	mustEnqueue(t, q, evtUntil("u1", "U", clk.in(time.Minute)))
	wantPending(t, q, map[string]ListenerPending{"h": {}})
	clk.advance(2 * time.Minute)
	q.Expire()
	wantPending(t, q, map[string]ListenerPending{"h": {}})
}

// Case 6: settle semantics.
func TestPendingByListenerSettleSemantics(t *testing.T) {
	t.Run("decline before expiry stays pending, after expiry settles", func(t *testing.T) {
		clk := newClock()
		q := newQueue(t, clk)
		l := newListener("h", "T")
		l.neverAccept = true
		q.Register(l)
		mustEnqueue(t, q, evtUntil("e", "T", clk.in(time.Hour)))
		q.Dispatch() // pre-expiry decline
		if got := q.PendingByListener()["h"].Count; got != 1 {
			t.Fatalf("after pre-expiry decline count = %d, want 1", got)
		}
		clk.advance(2 * time.Hour)
		q.Dispatch() // the single post-expiry attempt, declined: settled
		if got := q.PendingByListener()["h"].Count; got != 0 {
			t.Fatalf("after post-expiry decline count = %d, want 0 (settled)", got)
		}
	})
	t.Run("in-flight offer is pending until Offer returns", func(t *testing.T) {
		clk := newClock()
		q := newQueue(t, clk)
		l := &blockingListener{id: "h", binds: map[string]bool{"T": true}, proceed: make(chan struct{}), entered: make(chan struct{})}
		q.Register(l)
		mustEnqueue(t, q, evtUntil("e", "T", clk.in(time.Hour)))
		done := make(chan struct{})
		go func() { defer close(done); q.Dispatch() }()
		<-l.entered
		if got := q.PendingByListener()["h"].Count; got != 1 {
			t.Fatalf("mid-offer count = %d, want 1", got)
		}
		close(l.proceed)
		<-done
		if got := q.PendingByListener()["h"].Count; got != 0 {
			t.Fatalf("after Offer returned count = %d, want 0", got)
		}
	})
}

// Case 7: restart replay.
func TestPendingByListenerRestartReplay(t *testing.T) {
	clk := newClock()
	mem := NewMemStore()
	q1, err := New(mem, WithClock(clk.now))
	if err != nil {
		t.Fatal(err)
	}
	q1.Register(newListener("ok", "T"))
	declined := newListener("declined", "D")
	declined.neverAccept = true
	q1.Register(declined)
	mustEnqueue(t, q1, evtUntil("a", "T", clk.in(time.Hour)))
	mustEnqueue(t, q1, evtUntil("d", "D", clk.in(time.Hour)))
	clk.advance(4 * time.Second)
	q1.Dispatch() // accepts a (durable accept record)
	clk.advance(2 * time.Hour)
	q1.Dispatch() // d declined after expiry: settled in memory only
	if got := q1.PendingByListener(); got["ok"].Count != 0 || got["declined"].Count != 0 {
		t.Fatalf("pre-restart = %v, want both settled", got)
	}

	// Restart: same durable log, fresh queue, same clock.
	q2, err := New(mem, WithClock(clk.now))
	if err != nil {
		t.Fatal(err)
	}
	q2.Register(newListener("ok", "T"))
	d2 := newListener("declined", "D")
	d2.neverAccept = true
	q2.Register(d2)
	got := q2.PendingByListener()
	if got["ok"].Count != 0 {
		t.Fatalf("accepted event pending again after replay: %v", got["ok"])
	}
	// Documented: settlement by a post-expiry decline is not persisted.
	if got["declined"].Count != 1 {
		t.Fatalf("post-expiry-declined event after replay = %v, want pending again (documented)", got["declined"])
	}
	// Age survives: At is restored from the log (enqueued 2h4s before now).
	if want := 2*time.Hour + 4*time.Second; got["declined"].OldestAge != want {
		t.Fatalf("age after replay = %v, want %v", got["declined"].OldestAge, want)
	}
}

// Case 8: concurrency under -race.
func TestPendingByListenerConcurrentWithDispatchAndExpire(t *testing.T) {
	q, err := New(NewMemStore(), WithEarlyEviction())
	if err != nil {
		t.Fatal(err)
	}
	q.Register(&statelessAcceptListener{id: "h", typ: "T"})
	q.Register(&statelessAcceptListener{id: "g", typ: "U"})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					q.PendingByListener()
				}
			}
		}()
	}
	for i := 0; i < 300; i++ {
		if _, err := q.Enqueue(Event{ID: fmt.Sprintf("e%d", i), Type: "T"}); err != nil {
			t.Fatal(err)
		}
		q.Dispatch()
		q.Expire()
	}
	close(stop)
	wg.Wait()
	if got := q.PendingByListener(); got["h"].Count != 0 || got["g"].Count != 0 {
		t.Fatalf("after drain = %v, want all zero", got)
	}
}

// neverOfferedListener matches one type and declines; it is never offered in
// the allocation test (nothing dispatches).
type neverOfferedListener struct{ id, typ string }

func (l *neverOfferedListener) ID() string                 { return l.id }
func (l *neverOfferedListener) Matches(e Event) bool       { return e.Type == l.typ }
func (l *neverOfferedListener) Offer(Offering) OfferResult { return OfferResult{Decline: DeclineBusy} }

// Case 9: allocation budget. The pass MUST NOT allocate per retained entry: the
// result map, sized by listeners, is the only growth. Enforced in the unit tier
// (BenchmarkPendingByListenerAtTenK is informational only).
func TestPendingByListenerAllocBudget(t *testing.T) {
	measure := func(n int) float64 {
		clk := newClock()
		q := newQueue(t, clk)
		for _, id := range []string{"a", "b", "c"} {
			q.Register(&neverOfferedListener{id: id, typ: "T"})
		}
		for i := 0; i < n; i++ {
			mustEnqueue(t, q, evtUntil(fmt.Sprintf("e%d", i), "T", clk.in(time.Hour)))
		}
		return testing.AllocsPerRun(50, func() { q.PendingByListener() })
	}
	small, large := measure(10), measure(1000)
	t.Logf("allocs/call: 10 entries=%v, 1000 entries=%v", small, large)
	if large > small {
		t.Fatalf("allocations grow with retained entries: %v (10) vs %v (1000)", small, large)
	}
	const budget = 10.0
	if large > budget {
		t.Fatalf("allocs/call = %v, want <= %v", large, budget)
	}
}
