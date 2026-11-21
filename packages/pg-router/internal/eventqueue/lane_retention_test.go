package eventqueue

import (
	"testing"
	"time"
)

func laneEvt(c *mockClock, id, typ string) Event {
	return Event{ID: id, Type: typ, AckLane: "L", AckToken: "t-" + id, At: c.now(), ExpiresAt: c.in(time.Hour)}
}

// An accepted lane event keeps its id retained past its own expiry while its lane
// frontier has not passed it, so the source's re-offer is absorbed (INV-EVT-3);
// once the frontier passes it the id is released.
func TestLaneRetention_AcceptedIDHeldUntilFrontierPasses(t *testing.T) {
	c := newClock()
	held := map[string]bool{"e1": true}
	q, err := New(NewMemStore(), WithClock(c.now), WithLaneRetention(func(id string) bool { return held[id] }))
	if err != nil {
		t.Fatal(err)
	}
	a := newListener("a", "x")
	q.Register(a)
	mustEnqueue(t, q, laneEvt(c, "e1", "x"))
	q.Dispatch()
	if len(a.accepted) != 1 {
		t.Fatalf("accepted = %v", a.accepted)
	}
	c.advance(2 * time.Hour)
	if n := q.Expire(); n != 0 {
		t.Fatalf("Expire dropped %d entries; the held id must stay retained", n)
	}
	if r := mustEnqueue(t, q, laneEvt(c, "e1", "x")); r != Deduped {
		t.Fatalf("re-offer of a held accepted event = %v, want Deduped", r)
	}
	if len(a.offered) != 1 {
		t.Fatalf("the accepted handler was re-run: offered = %v", a.offered)
	}
	held["e1"] = false // the frontier passed it (or the hold limit abandoned the oldest)
	if n := q.Expire(); n != 1 {
		t.Fatalf("Expire dropped %d entries after release, want 1", n)
	}
	if r := mustEnqueue(t, q, laneEvt(c, "e1", "x")); r != Enqueued {
		t.Fatalf("re-emit after release = %v, want Enqueued", r)
	}
}

// An event without an ack lane is never held, whatever the hook says.
func TestLaneRetention_EventWithoutLaneIsNotHeld(t *testing.T) {
	c := newClock()
	q, _ := New(NewMemStore(), WithClock(c.now), WithLaneRetention(func(string) bool { return true }))
	a := newListener("a", "x")
	q.Register(a)
	mustEnqueue(t, q, Event{ID: "p", Type: "x", At: c.now(), ExpiresAt: c.in(time.Hour)})
	q.Dispatch()
	c.advance(2 * time.Hour)
	if n := q.Expire(); n != 1 {
		t.Fatalf("Expire dropped %d, want 1: a plain event is not lane-held", n)
	}
}

// Fan-out: one handler accepted, the other expired without accepting. A duplicate
// hit re-opens attempts, with a new window, only for the handler that has not
// accepted; the accepting handler is not re-run.
func TestFanOutReopen_ReopensOnlyTheHandlerThatHasNotAccepted(t *testing.T) {
	c := newClock()
	store := NewMemStore()
	q, err := New(store, WithClock(c.now), WithLaneRetention(func(string) bool { return true }))
	if err != nil {
		t.Fatal(err)
	}
	a := newListener("a", "x")
	b := newListener("b", "x")
	b.neverAccept = true
	q.Register(a)
	q.Register(b)
	mustEnqueue(t, q, laneEvt(c, "e1", "x"))
	q.Dispatch()
	c.advance(2 * time.Hour)
	q.Dispatch() // b's final attempt, after expiry
	bBefore := len(b.offered)
	q.Dispatch()
	if len(b.offered) != bBefore {
		t.Fatalf("b was offered again before any re-open: %v", b.offered)
	}
	if q.AckSettled("e1") {
		t.Fatal("e1 must be unsettled: b never accepted")
	}
	dup := laneEvt(c, "e1", "x") // the source's re-offer: new at and expiresAt
	if r := mustEnqueue(t, q, dup); r != Deduped {
		t.Fatalf("duplicate hit = %v, want Deduped", r)
	}
	q.mu.Lock()
	gotExp := q.entries["e1"].evt.ExpiresAt
	q.mu.Unlock()
	if !gotExp.Equal(dup.ExpiresAt) {
		t.Fatalf("re-opened expiresAt = %v, want %v", gotExp, dup.ExpiresAt)
	}
	q.Dispatch()
	if len(b.offered) != bBefore+1 {
		t.Fatalf("b offers = %d, want %d: the re-open owes it a new attempt", len(b.offered), bBefore+1)
	}
	if len(a.offered) != 1 {
		t.Fatalf("a was re-run: offered = %v", a.offered)
	}
	// The re-open is durable: a restart restores the new window.
	q2, err := New(store, WithClock(c.now))
	if err != nil {
		t.Fatal(err)
	}
	q2.mu.Lock()
	rexp := q2.entries["e1"].evt.ExpiresAt
	accA := q2.entries["e1"].accepted["a"]
	q2.mu.Unlock()
	if !rexp.Equal(dup.ExpiresAt) || !accA {
		t.Fatalf("replayed entry: expiresAt=%v (want %v) acceptedA=%v", rexp, dup.ExpiresAt, accA)
	}
}

// A duplicate whose own window is already over still re-opens, with a window of
// the original length starting now; a settled event is left alone.
func TestFanOutReopen_StaleWindowAndSettledEvent(t *testing.T) {
	c := newClock()
	q, _ := New(NewMemStore(), WithClock(c.now), WithLaneRetention(func(string) bool { return true }))
	b := newListener("b", "x")
	b.neverAccept = true
	q.Register(b)
	mustEnqueue(t, q, laneEvt(c, "e1", "x"))
	c.advance(2 * time.Hour)
	q.Dispatch()
	stale := Event{ID: "e1", Type: "x", AckLane: "L", AckToken: "t", At: c.in(-3 * time.Hour), ExpiresAt: c.in(-2 * time.Hour)}
	if r := mustEnqueue(t, q, stale); r != Deduped {
		t.Fatalf("= %v", r)
	}
	q.mu.Lock()
	exp := q.entries["e1"].evt.ExpiresAt
	q.mu.Unlock()
	if want := c.in(time.Hour); !exp.Equal(want) {
		t.Fatalf("expiresAt = %v, want %v (original window length from now)", exp, want)
	}

	a := newListener("a", "y")
	q.Register(a)
	mustEnqueue(t, q, laneEvt(c, "e2", "y"))
	q.Dispatch()
	before := len(a.offered)
	mustEnqueue(t, q, laneEvt(c, "e2", "y"))
	q.Dispatch()
	if len(a.offered) != before {
		t.Fatalf("a settled event was re-opened: %v", a.offered)
	}
}
