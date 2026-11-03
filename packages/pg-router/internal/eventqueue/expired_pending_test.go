package eventqueue

import (
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"
)

// Tests for ExpiredPendingObserver (DEC-OBS-13): one OnExpiredPending per
// (event, listener) whose retention ended without that listener accepting.

type expiredPendingObserver struct {
	noopObserver
	mu   sync.Mutex
	seen []string // "listener/type"
}

func (o *expiredPendingObserver) OnExpiredPending(evtType, listenerID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen = append(o.seen, listenerID+"/"+evtType)
}

func (o *expiredPendingObserver) got() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := append([]string(nil), o.seen...)
	sort.Strings(out)
	return out
}

// runToRetirement dispatches once before expiry, lets the event expire, runs the
// post-expiry attempt (which settles every listener) and sweeps.
func runToRetirement(q *Queue, clk *mockClock) {
	q.Dispatch()
	clk.advance(2 * time.Hour)
	q.Dispatch()
	q.Expire()
}

func TestExpiredPendingOneListenerAcceptedOneNot(t *testing.T) {
	clk := newClock()
	obs := &expiredPendingObserver{}
	q := newQueue(t, clk, WithObserver(obs))
	q.Register(newListener("ok", "T"))
	bad := newListener("bad", "T")
	bad.neverAccept = true
	q.Register(bad)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))
	runToRetirement(q, clk)
	if got, want := obs.got(), []string{"bad/T"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expired pending = %v, want %v (only the listener that never accepted)", got, want)
	}
	// The partial case is NOT an unconsumed-expired miss: ok accepted it.
	q.Expire()
	if got := obs.got(); len(got) != 1 {
		t.Fatalf("a second sweep recounted: %v", got)
	}
}

func TestExpiredPendingNoneAccepted(t *testing.T) {
	clk := newClock()
	obs := &expiredPendingObserver{}
	q := newQueue(t, clk, WithObserver(obs))
	for _, id := range []string{"a", "b"} {
		l := newListener(id, "T")
		l.neverAccept = true
		q.Register(l)
	}
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))
	runToRetirement(q, clk)
	if got, want := obs.got(), []string{"a/T", "b/T"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expired pending = %v, want %v", got, want)
	}
}

func TestExpiredPendingAllAcceptedCountsZero(t *testing.T) {
	clk := newClock()
	obs := &expiredPendingObserver{}
	q := newQueue(t, clk, WithObserver(obs))
	q.Register(newListener("a", "T"))
	q.Register(newListener("b", "T"))
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))
	runToRetirement(q, clk)
	if got := obs.got(); len(got) != 0 {
		t.Fatalf("expired pending = %v, want none", got)
	}
}

func TestExpiredPendingUnboundTypeCountsForNobody(t *testing.T) {
	clk := newClock()
	obs := &expiredPendingObserver{}
	q := newQueue(t, clk, WithObserver(obs))
	l := newListener("a", "T")
	l.neverAccept = true
	q.Register(l)
	mustEnqueue(t, q, evtUntil("u1", "U", clk.in(time.Hour)))
	runToRetirement(q, clk)
	if got := obs.got(); len(got) != 0 {
		t.Fatalf("expired pending = %v, want none for a type no listener matches", got)
	}
}

func TestExpiredPendingStaleReemitCountsOnce(t *testing.T) {
	clk := newClock()
	obs := &expiredPendingObserver{}
	q := newQueue(t, clk, WithObserver(obs))
	l := newListener("bad", "T")
	l.neverAccept = true
	q.Register(l)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))
	q.Dispatch()
	clk.advance(2 * time.Hour)
	q.Dispatch() // post-expiry attempt: settled, retention over, not yet swept
	// A re-emit of the id retires the stale entry on Expire's terms.
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(5*time.Hour)))
	q.Expire() // the fresh entry is retained: nothing more is counted
	if got, want := obs.got(), []string{"bad/T"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expired pending = %v, want %v exactly once", got, want)
	}
}

func TestExpiredPendingNotRecountedAfterRestart(t *testing.T) {
	clk := newClock()
	mem := NewMemStore()
	obs1 := &expiredPendingObserver{}
	q1, err := New(mem, WithClock(clk.now), WithObserver(obs1))
	if err != nil {
		t.Fatal(err)
	}
	l := newListener("bad", "T")
	l.neverAccept = true
	q1.Register(l)
	mustEnqueue(t, q1, evtUntil("e1", "T", clk.in(time.Hour)))
	runToRetirement(q1, clk)
	if len(obs1.got()) != 1 {
		t.Fatalf("before restart = %v, want one", obs1.got())
	}

	obs2 := &expiredPendingObserver{}
	q2, err := New(mem, WithClock(clk.now), WithObserver(obs2))
	if err != nil {
		t.Fatal(err)
	}
	l2 := newListener("bad", "T")
	l2.neverAccept = true
	q2.Register(l2)
	q2.Dispatch()
	q2.Expire()
	if got := obs2.got(); len(got) != 0 {
		t.Fatalf("restart recounted an already-retired event: %v", got)
	}
}
