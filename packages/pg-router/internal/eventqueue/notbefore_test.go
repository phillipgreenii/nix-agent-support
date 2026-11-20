package eventqueue

import (
	"testing"
	"time"
)

// An event with a future notBefore is not offered before it and is offered
// after; the gate is durable across a restart (ADR 0121 D3).
func TestNotBefore_GatesOfferUntilInstant(t *testing.T) {
	clk := newClock()
	mem := NewMemStore()
	q, err := New(mem, WithClock(clk.now))
	if err != nil {
		t.Fatal(err)
	}
	l := newListener("h", "T")
	q.Register(l)
	e := evtUntil("a", "T", clk.in(3*time.Hour))
	e.NotBefore = clk.in(time.Hour)
	e.Generation = 1
	mustEnqueue(t, q, e)

	q.Dispatch()
	if len(l.offered) != 0 {
		t.Fatalf("offered before notBefore: %v", l.offered)
	}
	if q.Idle() {
		t.Fatal("queue idle while a gated, unexpired event waits")
	}

	// Restart: the gate and generation come back from the log.
	q2, err := New(mem, WithClock(clk.now))
	if err != nil {
		t.Fatal(err)
	}
	l2 := newListener("h", "T")
	q2.Register(l2)
	clk.advance(30 * time.Minute)
	q2.Dispatch()
	if len(l2.offered) != 0 {
		t.Fatalf("offered before notBefore after replay: %v", l2.offered)
	}
	clk.advance(30 * time.Minute) // now == notBefore
	q2.Dispatch()
	if len(l2.offered) != 1 || l2.offered[0] != "a" {
		t.Fatalf("offered = %v, want [a] at notBefore", l2.offered)
	}
}

// A gated event does not head-of-line block later events for the same listener.
func TestNotBefore_DoesNotBlockLaterEvents(t *testing.T) {
	clk := newClock()
	q, err := New(NewMemStore(), WithClock(clk.now))
	if err != nil {
		t.Fatal(err)
	}
	l := newListener("h", "T")
	q.Register(l)
	gated := evtUntil("gated", "T", clk.in(3*time.Hour))
	gated.NotBefore = clk.in(time.Hour)
	mustEnqueue(t, q, gated)
	mustEnqueue(t, q, evtUntil("plain", "T", clk.in(3*time.Hour)))
	clk.advance(4 * time.Second)
	q.Dispatch()
	if len(l.offered) != 1 || l.offered[0] != "plain" {
		t.Fatalf("offered = %v, want [plain]", l.offered)
	}
}

// expiresAt still bounds a gated event: one whose window closes before its
// notBefore is dropped without ever being offered.
func TestNotBefore_ExpiryStillBounds(t *testing.T) {
	clk := newClock()
	q, err := New(NewMemStore(), WithClock(clk.now))
	if err != nil {
		t.Fatal(err)
	}
	l := newListener("h", "T")
	q.Register(l)
	e := evtUntil("a", "T", clk.in(time.Minute))
	e.NotBefore = clk.in(time.Hour)
	mustEnqueue(t, q, e)
	clk.advance(2 * time.Minute)
	q.Dispatch()
	if dropped := q.Expire(); dropped != 1 {
		t.Fatalf("Expire dropped %d, want 1", dropped)
	}
	clk.advance(2 * time.Hour)
	q.Dispatch()
	if len(l.offered) != 0 {
		t.Fatalf("expired gated event was offered: %v", l.offered)
	}
}

// An event with no notBefore is unchanged: born-expired default still gets its
// one offer.
func TestNotBefore_AbsentIsUnchanged(t *testing.T) {
	clk := newClock()
	q, err := New(NewMemStore(), WithClock(clk.now))
	if err != nil {
		t.Fatal(err)
	}
	l := newListener("h", "T")
	q.Register(l)
	mustEnqueue(t, q, evt("a", "T"))
	q.Dispatch()
	if len(l.offered) != 1 {
		t.Fatalf("offered = %v, want one", l.offered)
	}
}

func TestEvent_NotYetEligible(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	if (Event{}).NotYetEligible(now) {
		t.Fatal("zero notBefore must not gate")
	}
	if !(Event{NotBefore: now.Add(time.Second)}).NotYetEligible(now) {
		t.Fatal("future notBefore must gate")
	}
	if (Event{NotBefore: now}).NotYetEligible(now) {
		t.Fatal("notBefore == now must be eligible")
	}
}

func TestWire_NotBeforeAndGenerationRoundTrip(t *testing.T) {
	raw := `{"id":"e","type":"t","notBefore":"2026-07-16T13:00:00Z","generation":1}`
	ev, err := DecodeEvent([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Generation != 1 || !ev.NotBefore.Equal(time.Date(2026, 7, 16, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("decoded = %+v", ev)
	}
	out, err := EncodeEvent(ev)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeEvent(out)
	if err != nil || back.Generation != 1 || !back.NotBefore.Equal(ev.NotBefore) {
		t.Fatalf("round trip = %+v, %v (%s)", back, err, out)
	}
	// Older producers: neither field is emitted when unset.
	plain, _ := EncodeEvent(Event{ID: "e", Type: "t"})
	if s := string(plain); containsAny(s, "notBefore", "generation") {
		t.Fatalf("unset fields emitted: %s", s)
	}
	for _, bad := range []string{
		`{"id":"e","type":"t","notBefore":"nope"}`,
		`{"id":"e","type":"t","generation":-1}`,
	} {
		if _, err := DecodeEvent([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		for i := 0; i+len(x) <= len(s); i++ {
			if s[i:i+len(x)] == x {
				return true
			}
		}
	}
	return false
}
