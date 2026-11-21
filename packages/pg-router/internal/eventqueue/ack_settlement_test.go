package eventqueue

import (
	"sync"
	"testing"
	"time"
)

func ackEvt(id, typ string) Event {
	return Event{ID: id, Type: typ, AckLane: "lane-a", AckToken: "tok-" + id}
}

func TestAckSettled_NoMatchingHandlerIsSettled(t *testing.T) {
	q, err := New(NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(ackEvt("e1", "x")); err != nil {
		t.Fatal(err)
	}
	if !q.AckSettled("e1") {
		t.Fatal("an event no handler matches owes nothing and is settled")
	}
	if q.AckSettled("never-enqueued") {
		t.Fatal("an id the queue never held (refused at ingest) is not settled")
	}
}

func TestAckSettled_AcceptedBySettledExpiredWithoutAcceptanceNot(t *testing.T) {
	clock := &mockClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	var mu sync.Mutex
	var hooked []string
	q, err := New(NewMemStore(), WithClock(clock.now), WithAcceptHook(func(id string) {
		mu.Lock()
		hooked = append(hooked, id)
		mu.Unlock()
	}))
	if err != nil {
		t.Fatal(err)
	}
	a := newListener("a", "x")
	b := newListener("b", "x")
	b.neverAccept = true
	q.Register(a)
	q.Register(b)
	if _, err := q.Enqueue(ackEvt("e1", "x")); err != nil {
		t.Fatal(err)
	}
	q.Dispatch()
	// a accepted, b declined: not settled, no hook.
	if q.AckSettled("e1") || len(hooked) != 0 {
		t.Fatalf("settled with b outstanding: settled=%v hooked=%v", q.AckSettled("e1"), hooked)
	}
	// b's final attempt (born-expired default) settles b's pair by expiry, NOT by acceptance.
	q.Dispatch()
	if q.AckSettled("e1") {
		t.Fatal("an event that expired without acceptance must not be settled")
	}
	if len(hooked) != 0 {
		t.Fatalf("hook fired for an unsettled event: %v", hooked)
	}
}

func TestAckSettled_HookFiresWhenLastHandlerAccepts(t *testing.T) {
	var mu sync.Mutex
	var hooked []string
	q, _ := New(NewMemStore(), WithAcceptHook(func(id string) {
		mu.Lock()
		hooked = append(hooked, id)
		mu.Unlock()
	}))
	a := newListener("a", "x")
	q.Register(a)
	_, _ = q.Enqueue(ackEvt("e1", "x"))
	_, _ = q.Enqueue(Event{ID: "plain", Type: "x"}) // no ack handle: never hooks
	q.Dispatch()
	q.Dispatch()
	if len(hooked) != 1 || hooked[0] != "e1" {
		t.Fatalf("hooked = %v, want [e1]", hooked)
	}
	if !q.AckSettled("e1") {
		t.Fatal("e1 must be settled")
	}
}

func TestAckHandleIsDurableWithTheQueuedEvent(t *testing.T) {
	store := NewMemStore()
	q, _ := New(store)
	if _, err := q.Enqueue(Event{ID: "e1", Type: "x", AckLane: "L", AckToken: "T", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	q2, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	q2.mu.Lock()
	e := q2.entries["e1"]
	q2.mu.Unlock()
	if e == nil || e.evt.AckLane != "L" || e.evt.AckToken != "T" {
		t.Fatalf("replayed entry lost its ack handle: %+v", e)
	}
}
