package eventqueue

import (
	"errors"
	"slices"
	"testing"
	"time"
)

// gateExemptListener is a fakeListener that declares non-blocking gate TYPEs at
// registration (GateExempter).
type gateExemptListener struct {
	*fakeListener
	exempt []string
}

func (g gateExemptListener) NonBlockingGates() []string { return g.exempt }

// recordingGateObserver captures GateObserver callbacks.
type recordingGateObserver struct {
	sets     []string // "TYPE/renewal=bool"
	cleared  []string
	expired  []string
	blocked  []string // "participant/kind/TYPE"
	drops    []string // "evtType/listener/TYPE"
	heldLast time.Duration
}

func (o *recordingGateObserver) OnGateSet(t string, renewal bool) {
	o.sets = append(o.sets, t+"/"+map[bool]string{true: "renewal", false: "new"}[renewal])
}

func (o *recordingGateObserver) OnGateCleared(t string, held time.Duration) {
	o.cleared = append(o.cleared, t)
	o.heldLast = held
}

func (o *recordingGateObserver) OnGateExpired(t string, held time.Duration) {
	o.expired = append(o.expired, t)
	o.heldLast = held
}

func (o *recordingGateObserver) OnGateBlocked(p, k, t string) {
	o.blocked = append(o.blocked, p+"/"+k+"/"+t)
}

func (o *recordingGateObserver) OnGateDrop(e, l, t string) {
	o.drops = append(o.drops, e+"/"+l+"/"+t)
}

func newGateQueue(t *testing.T, store Store, c *mockClock, extra ...Option) (*Queue, *recordingGateObserver) {
	t.Helper()
	obs := &recordingGateObserver{}
	opts := append([]Option{WithClock(c.now), WithGateObserver(obs)}, extra...)
	q, err := New(store, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return q, obs
}

func mustSet(t *testing.T, q *Queue, req GateRequest) Gate {
	t.Helper()
	g, err := q.SetGate(req)
	if err != nil {
		t.Fatalf("SetGate(%+v): %v", req, err)
	}
	return g
}

func gateTypes(gs []Gate) []string {
	var out []string
	for _, g := range gs {
		out = append(out, g.Type)
	}
	return out
}

func TestValidateGateType(t *testing.T) {
	for _, ok := range []string{"SYSTEM_PAUSE", "LOW_DISK_USAGE", "X", "A1_B2"} {
		if err := ValidateGateType(ok); err != nil {
			t.Errorf("ValidateGateType(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "lower", "Mixed", "1LEAD", "HAS SPACE", "DASH-ED", "_LEAD", "A" + string(make([]byte, 70))} {
		err := ValidateGateType(bad)
		if !errors.Is(err, ErrInvalidGate) {
			t.Errorf("ValidateGateType(%q) = %v, want ErrInvalidGate", bad, err)
		}
	}
}

func TestSetGate_RejectsInvalidType(t *testing.T) {
	q, _ := newGateQueue(t, NewMemStore(), newClock())
	if _, err := q.SetGate(GateRequest{Type: "nope"}); !errors.Is(err, ErrInvalidGate) {
		t.Fatalf("SetGate(lower) = %v, want ErrInvalidGate", err)
	}
	if _, _, err := q.ClearGate("nope", "x"); !errors.Is(err, ErrInvalidGate) {
		t.Fatalf("ClearGate(lower) = %v, want ErrInvalidGate", err)
	}
	if len(q.ActiveGates()) != 0 {
		t.Fatal("an invalid set must not create a gate")
	}
}

func TestSetGate_LastWriterWinsPerType(t *testing.T) {
	c := newClock()
	q, obs := newGateQueue(t, NewMemStore(), c)
	mustSet(t, q, GateRequest{Type: "ALPHA", Description: "first", Owner: "alice"})
	c.advance(time.Minute)
	g := mustSet(t, q, GateRequest{Type: "ALPHA", Description: "second", Owner: "bob"})
	mustSet(t, q, GateRequest{Type: "BETA"})

	active := q.ActiveGates()
	if got := gateTypes(active); !slices.Equal(got, []string{"ALPHA", "BETA"}) {
		t.Fatalf("active = %v, want [ALPHA BETA] (one per TYPE, sorted)", got)
	}
	if active[0].Description != "second" || active[0].Owner != "bob" {
		t.Fatalf("ALPHA = %+v, want the last writer's description/owner", active[0])
	}
	if !g.SetAt.Equal(c.now()) {
		t.Fatalf("SetAt = %v, want the overwrite's instant %v", g.SetAt, c.now())
	}
	if want := []string{"ALPHA/new", "ALPHA/renewal", "BETA/new"}; !slices.Equal(obs.sets, want) {
		t.Fatalf("OnGateSet = %v, want %v", obs.sets, want)
	}
}

func TestClearGate_AnyCallerAndIdempotent(t *testing.T) {
	c := newClock()
	q, obs := newGateQueue(t, NewMemStore(), c)
	mustSet(t, q, GateRequest{Type: "ALPHA", Owner: "alice"})
	c.advance(90 * time.Second)

	// A DIFFERENT caller than the setter clears it: owner is debug only.
	g, cleared, err := q.ClearGate("ALPHA", "mallory")
	if err != nil || !cleared {
		t.Fatalf("ClearGate = (%+v, %v, %v), want cleared", g, cleared, err)
	}
	if g.Owner != "alice" {
		t.Fatalf("cleared gate's owner = %q, want the setter's", g.Owner)
	}
	if len(q.ActiveGates()) != 0 {
		t.Fatal("gate still active after clear")
	}
	if obs.heldLast != 90*time.Second {
		t.Fatalf("held = %v, want 90s", obs.heldLast)
	}
	// Idempotent: clearing again is a no-op success that writes nothing.
	st := NewMemStore()
	q2, _ := newGateQueue(t, st, c)
	if _, cleared, err := q2.ClearGate("ALPHA", "x"); err != nil || cleared {
		t.Fatalf("clearing an unset gate = (%v, %v), want (false, nil)", cleared, err)
	}
	if recs, _ := st.Replay(); len(recs) != 0 {
		t.Fatalf("clearing an unset gate wrote %d record(s), want 0", len(recs))
	}
}

func TestGateRecords_AreAppendedThroughTheStore(t *testing.T) {
	c := newClock()
	st := NewMemStore()
	q, _ := newGateQueue(t, st, c)
	mustSet(t, q, GateRequest{Type: "ALPHA", Description: "d", Owner: "o", TTL: time.Minute})
	mustSet(t, q, GateRequest{Type: "ALPHA", Description: "d2", Owner: "o", TTL: time.Minute}) // renewal
	if _, _, err := q.ClearGate("ALPHA", "op"); err != nil {
		t.Fatal(err)
	}
	mustSet(t, q, GateRequest{Type: "BETA", TTL: time.Second})
	c.advance(time.Minute)
	q.Expire()

	recs, _ := st.Replay()
	var ops []opKind
	for _, r := range recs {
		ops = append(ops, r.Op)
	}
	want := []opKind{opGateSet, opGateSet, opGateCleared, opGateSet, opGateExpired}
	if !slices.Equal(ops, want) {
		t.Fatalf("log ops = %v, want %v (each renewal is another GateSet)", ops, want)
	}
	if recs[0].GateType != "ALPHA" || recs[0].Description != "d" || recs[0].Owner != "o" || recs[0].ExpiresAt.IsZero() {
		t.Fatalf("first GateSet record = %+v", recs[0])
	}
}

func TestGate_TTLLeaseLapsesAndRenews(t *testing.T) {
	c := newClock()
	q, obs := newGateQueue(t, NewMemStore(), c)
	mustSet(t, q, GateRequest{Type: "LEASED", TTL: 5 * time.Minute})
	mustSet(t, q, GateRequest{Type: "FOREVER"})

	c.advance(4 * time.Minute)
	if got := gateTypes(q.ActiveGates()); !slices.Equal(got, []string{"FOREVER", "LEASED"}) {
		t.Fatalf("active before lapse = %v", got)
	}
	// A renewal (another GateSet) carries the new expiry.
	mustSet(t, q, GateRequest{Type: "LEASED", TTL: 5 * time.Minute})
	c.advance(4 * time.Minute) // 8m since first set, 4m since renewal
	if _, ok := q.Gate("LEASED"); !ok {
		t.Fatal("renewed lease must still be active")
	}
	c.advance(2 * time.Minute) // renewal's lease lapsed
	if _, ok := q.Gate("LEASED"); ok {
		t.Fatal("a lapsed TTL must count as inactive even before the sweep")
	}
	if got := gateTypes(q.ActiveGates()); !slices.Equal(got, []string{"FOREVER"}) {
		t.Fatalf("active after lapse = %v, want only the lease-less gate", got)
	}
	// The sweep retires it exactly once.
	q.Expire()
	q.Expire()
	if !slices.Equal(obs.expired, []string{"LEASED"}) {
		t.Fatalf("OnGateExpired = %v, want exactly one for LEASED", obs.expired)
	}
}

func TestGate_ReplaySurvivesRestartAndHonorsTTL(t *testing.T) {
	c := newClock()
	st := NewMemStore()
	q, _ := newGateQueue(t, st, c)
	mustSet(t, q, GateRequest{Type: "KEPT", Description: "stays", Owner: "ops"})
	mustSet(t, q, GateRequest{Type: "GONE"})
	mustSet(t, q, GateRequest{Type: "LAPSES", TTL: time.Minute})
	mustSet(t, q, GateRequest{Type: "SWEPT", TTL: time.Second})
	if _, _, err := q.ClearGate("GONE", "op"); err != nil {
		t.Fatal(err)
	}
	c.advance(10 * time.Second)
	q.Expire() // writes SWEPT's gate_expired

	// "Restart": a fresh queue over the SAME store replays the log.
	q2, _ := newGateQueue(t, st, c)
	if got := gateTypes(q2.ActiveGates()); !slices.Equal(got, []string{"KEPT", "LAPSES"}) {
		t.Fatalf("after restart active = %v, want [KEPT LAPSES]: a cleared and an expired gate stay gone", got)
	}
	g, _ := q2.Gate("KEPT")
	if g.Description != "stays" || g.Owner != "ops" {
		t.Fatalf("replayed gate = %+v, want description/owner preserved", g)
	}
	// The TTL is honored against the clock AFTER the restart too.
	c.advance(2 * time.Minute)
	if _, ok := q2.Gate("LAPSES"); ok {
		t.Fatal("a replayed lease must lapse on schedule")
	}
	if _, ok := q2.Gate("KEPT"); !ok {
		t.Fatal("a lease-less gate must outlive any amount of time")
	}
}

// A gate's state is a log record, not a queue entry: retiring every event (the
// delivery TTL) can never un-set it.
func TestGate_StateDoesNotDependOnEventRetention(t *testing.T) {
	c := newClock()
	q, _ := newGateQueue(t, NewMemStore(), c)
	l := newListener("L", "work")
	q.Register(l)
	mustSet(t, q, GateRequest{Type: "ALPHA"})
	if _, err := q.Enqueue(Event{ID: "e1", Type: "work"}); err != nil {
		t.Fatal(err)
	}
	q.Dispatch()
	c.advance(time.Hour)
	if dropped := q.Expire(); dropped != 1 {
		t.Fatalf("Expire dropped %d, want 1 (the event)", dropped)
	}
	if _, ok := q.Gate("ALPHA"); !ok {
		t.Fatal("a queue drop must not touch a gate")
	}
}

func TestGate_RecordsAreRoutableEvents(t *testing.T) {
	c := newClock()
	q, _ := newGateQueue(t, NewMemStore(), c)
	sub := newListener("sub", GateEventSet, GateEventCleared, GateEventExpired)
	other := newListener("other", "work")
	q.Register(sub)
	q.Register(other)

	mustSet(t, q, GateRequest{Type: "ALPHA", Description: "why", Owner: "me", TTL: time.Second})
	q.Dispatch()
	if _, _, err := q.ClearGate("ALPHA", "op"); err != nil {
		t.Fatal(err)
	}
	q.Dispatch()
	mustSet(t, q, GateRequest{Type: "BETA", TTL: time.Second})
	q.Dispatch()
	c.advance(time.Minute)
	q.Expire()
	q.Dispatch()

	if len(sub.accepted) != 4 {
		t.Fatalf("subscriber accepted %d gate events, want 4 (set, cleared, set, expired): %v", len(sub.accepted), sub.accepted)
	}
	if len(other.accepted) != 0 || len(other.offered) != 0 {
		t.Fatal("a listener not bound to gate.* must never be offered a gate record")
	}
}

func TestGate_NoSubscriberMeansNoRoutedEventButStillLogged(t *testing.T) {
	st := NewMemStore()
	q, _ := newGateQueue(t, st, newClock())
	q.Register(newListener("L", "work"))
	mustSet(t, q, GateRequest{Type: "ALPHA"})
	recs, _ := st.Replay()
	if len(recs) != 1 || recs[0].Op != opGateSet {
		t.Fatalf("records = %+v, want exactly the gate_set", recs)
	}
	if len(q.DepthByType()) != 0 {
		t.Fatalf("depth = %v, want no routed event when nothing subscribes", q.DepthByType())
	}
}

func TestGate_GateEventsBypassGatingForDelivery(t *testing.T) {
	c := newClock()
	q, _ := newGateQueue(t, NewMemStore(), c)
	// "sub" blocks on every gate type (default) AND subscribes to gate events.
	sub := newListener("sub", GateEventSet, GateEventCleared)
	q.Register(sub)

	mustSet(t, q, GateRequest{Type: "ALPHA"})
	q.Dispatch()
	if len(sub.accepted) != 1 {
		t.Fatalf("blocked subscriber accepted %d gate events, want 1: it must learn the gate was set", len(sub.accepted))
	}
	// And — the reason for the bypass — it must learn the gate was CLEARED.
	if _, _, err := q.ClearGate("ALPHA", "op"); err != nil {
		t.Fatal(err)
	}
	q.Dispatch()
	if len(sub.accepted) != 2 {
		t.Fatalf("subscriber accepted %d gate events, want 2 (set + cleared)", len(sub.accepted))
	}
}

func TestGate_BlockedListenerIsNotDispatchedUnblockedStillIs(t *testing.T) {
	c := newClock()
	obs := &recordingGateObserver{}
	q, err := New(NewMemStore(), WithClock(c.now), WithGateObserver(obs))
	if err != nil {
		t.Fatal(err)
	}
	blocked := newListener("blocked", "work")
	exempt := gateExemptListener{newListener("exempt", "work"), []string{"ALPHA"}}
	q.Register(blocked)
	q.Register(exempt)

	mustSet(t, q, GateRequest{Type: "ALPHA"})
	// Unexpired work, so it stays queued rather than hitting its final attempt.
	if _, err := q.Enqueue(Event{ID: "e1", Type: "work", ExpiresAt: c.in(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	q.Dispatch()

	if len(blocked.offered) != 0 {
		t.Fatalf("a gate-blocked listener was offered %v; it must not be dispatched to", blocked.offered)
	}
	if !slices.Equal(exempt.accepted, []string{"e1"}) {
		t.Fatalf("exempt listener accepted %v, want [e1]: an UNBLOCKED listener still receives queued events", exempt.accepted)
	}
	if !slices.Contains(obs.blocked, "blocked/dispatch/ALPHA") {
		t.Fatalf("OnGateBlocked = %v, want blocked/dispatch/ALPHA", obs.blocked)
	}
	if slices.ContainsFunc(obs.blocked, func(s string) bool { return len(s) >= 6 && s[:6] == "exempt" }) {
		t.Fatalf("exempt listener was reported blocked: %v", obs.blocked)
	}
	// The event stayed QUEUED for the blocked listener: clearing the gate delivers it.
	if _, _, err := q.ClearGate("ALPHA", "op"); err != nil {
		t.Fatal(err)
	}
	q.Dispatch()
	if !slices.Equal(blocked.accepted, []string{"e1"}) {
		t.Fatalf("after clear, blocked listener accepted %v, want [e1] (events act as queued, not dropped)", blocked.accepted)
	}
}

func TestGate_ExemptionIsPerType(t *testing.T) {
	c := newClock()
	q, _ := newGateQueue(t, NewMemStore(), c)
	l := gateExemptListener{newListener("L", "work"), []string{"ALPHA"}}
	q.Register(l)
	mustSet(t, q, GateRequest{Type: "ALPHA"})
	mustSet(t, q, GateRequest{Type: "BETA"})
	if _, err := q.Enqueue(Event{ID: "e1", Type: "work", ExpiresAt: c.in(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	q.Dispatch()
	if len(l.offered) != 0 {
		t.Fatal("exempt from ALPHA only: BETA must still block it")
	}
	if _, _, err := q.ClearGate("BETA", "op"); err != nil {
		t.Fatal(err)
	}
	q.Dispatch()
	if len(l.accepted) != 1 {
		t.Fatalf("with only the exempted gate left the listener must run; accepted %v", l.accepted)
	}
}

func TestGate_FinalAttemptLostWhileBlockedAndCounted(t *testing.T) {
	c := newClock()
	q, obs := newGateQueue(t, NewMemStore(), c)
	blocked := newListener("blocked", "work")
	free := gateExemptListener{newListener("free", "work"), []string{"ALPHA"}}
	q.Register(blocked)
	q.Register(free)
	mustSet(t, q, GateRequest{Type: "ALPHA"})

	// The default event is BORN EXPIRED: its first attempt is its last.
	if _, err := q.Enqueue(Event{ID: "e1", Type: "work"}); err != nil {
		t.Fatal(err)
	}
	q.Dispatch()
	if len(blocked.offered) != 0 {
		t.Fatalf("blocked listener offered %v", blocked.offered)
	}
	if !slices.Equal(free.accepted, []string{"e1"}) {
		t.Fatalf("unblocked listener accepted %v, want [e1]", free.accepted)
	}
	if !slices.Equal(obs.drops, []string{"work/blocked/ALPHA"}) {
		t.Fatalf("OnGateDrop = %v, want one drop for the blocked listener", obs.drops)
	}
	// "Goes away for it": the event is gone, not waiting for the gate to clear.
	if dropped := q.Expire(); dropped != 1 {
		t.Fatalf("Expire dropped %d, want 1: the blocked listener's final attempt was lost", dropped)
	}
	if _, _, err := q.ClearGate("ALPHA", "op"); err != nil {
		t.Fatal(err)
	}
	q.Dispatch()
	if len(blocked.offered) != 0 {
		t.Fatal("a dropped event must not reappear when the gate clears")
	}
	// Counted once, not once per pass.
	if len(obs.drops) != 1 {
		t.Fatalf("drops = %v, want exactly 1", obs.drops)
	}
}

func TestGate_PushStillAcceptedAndAckStillProcessed(t *testing.T) {
	c := newClock()
	st := NewMemStore()
	q, _ := newGateQueue(t, st, c)
	var l *midOfferListener
	l = &midOfferListener{fakeListener: newListener("L", "work"), onOffer: func() {
		// The gate is set WHILE the offer is outstanding: the in-flight offer's
		// acceptance (the ack) must still be processed and recorded.
		mustSet(t, q, GateRequest{Type: "ALPHA"})
	}}
	q.Register(l)
	if _, err := q.Enqueue(Event{ID: "e1", Type: "work", ExpiresAt: c.in(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if accepted := q.Dispatch(); accepted != 1 {
		t.Fatalf("Dispatch accepted %d, want 1: an ack under a gate is still processed", accepted)
	}
	recs, _ := st.Replay()
	var sawAccept bool
	for _, r := range recs {
		if r.Op == opAccept && r.EventID == "e1" {
			sawAccept = true
		}
	}
	if !sawAccept {
		t.Fatal("the acceptance was not persisted")
	}
	// A push is still ACCEPTED (enqueued) while the gate is up.
	res, err := q.Enqueue(Event{ID: "e2", Type: "work", ExpiresAt: c.in(time.Hour)})
	if err != nil || res != Enqueued {
		t.Fatalf("push under a gate = (%v, %v), want Enqueued", res, err)
	}
	if q.DepthByType()["work"] != 2 {
		t.Fatalf("depth = %v, want both events retained", q.DepthByType())
	}
	// And it is NOT delivered to the blocked listener meanwhile.
	q.Dispatch()
	if len(l.accepted) != 1 {
		t.Fatalf("blocked listener accepted %v; the pushed event must wait", l.accepted)
	}
}

type midOfferListener struct {
	*fakeListener
	onOffer func()
}

func (m *midOfferListener) Offer(o Offering) OfferResult {
	if m.onOffer != nil {
		m.onOffer()
		m.onOffer = nil
	}
	return m.fakeListener.Offer(o)
}

func TestGate_CheckPullRejectsBlockedListenerNamingTheGate(t *testing.T) {
	q, obs := newGateQueue(t, NewMemStore(), newClock())
	if err := q.CheckPull("L", nil); err != nil {
		t.Fatalf("no gate: pull = %v, want nil", err)
	}
	mustSet(t, q, GateRequest{Type: "ALPHA"})
	err := q.CheckPull("L", nil)
	var gb *GateBlockedError
	if !errors.As(err, &gb) || gb.Gate != "ALPHA" || gb.Participant != "L" {
		t.Fatalf("pull = %v, want a GateBlockedError naming ALPHA", err)
	}
	if !errors.Is(err, ErrGateBlocked) {
		t.Fatal("GateBlockedError must match ErrGateBlocked")
	}
	if err := q.CheckPull("L", ExemptSet([]string{"ALPHA"})); err != nil {
		t.Fatalf("exempt pull = %v, want nil", err)
	}
	if !slices.Contains(obs.blocked, "L/pull/ALPHA") {
		t.Fatalf("OnGateBlocked = %v, want L/pull/ALPHA", obs.blocked)
	}
}

func TestGate_EmitterBlockedBy(t *testing.T) {
	q, obs := newGateQueue(t, NewMemStore(), newClock())
	if _, blocked := q.EmitterBlockedBy("src", nil); blocked {
		t.Fatal("no gate: emitter must not be blocked")
	}
	mustSet(t, q, GateRequest{Type: "BETA"})
	mustSet(t, q, GateRequest{Type: "ALPHA"})
	if g, blocked := q.EmitterBlockedBy("src", nil); !blocked || g != "ALPHA" {
		t.Fatalf("EmitterBlockedBy = (%q, %v), want (ALPHA, true): deterministic by TYPE", g, blocked)
	}
	if g, blocked := q.EmitterBlockedBy("src", ExemptSet([]string{"ALPHA"})); !blocked || g != "BETA" {
		t.Fatalf("exempt from ALPHA: (%q, %v), want BETA", g, blocked)
	}
	if _, blocked := q.EmitterBlockedBy("src", ExemptSet([]string{"ALPHA", "BETA"})); blocked {
		t.Fatal("exempt from every active gate: not blocked")
	}
	if !slices.Contains(obs.blocked, "src/poll/ALPHA") {
		t.Fatalf("OnGateBlocked = %v", obs.blocked)
	}
}

func TestGate_GateEventsNeverCountAsUnconsumedMisses(t *testing.T) {
	c := newClock()
	rec := &recordingObserver{}
	q, err := New(NewMemStore(), WithClock(c.now), WithObserver(rec))
	if err != nil {
		t.Fatal(err)
	}
	// A subscriber that never accepts: the routed event ends with no taker.
	sub := newListener("sub", GateEventSet)
	sub.neverAccept = true
	q.Register(sub)
	mustSet(t, q, GateRequest{Type: "ALPHA"})
	q.Dispatch()
	c.advance(time.Minute)
	q.Dispatch()
	q.Expire()
	if len(rec.unconsumedExpired) != 0 {
		t.Fatalf("unconsumed-expired = %v: gate records are control-plane, never a miss", rec.unconsumedExpired)
	}
}

func TestGateRecordsDoNotDisturbEventReplay(t *testing.T) {
	c := newClock()
	st := NewMemStore()
	q, _ := newGateQueue(t, st, c)
	q.Register(newListener("L", "work"))
	mustSet(t, q, GateRequest{Type: "ALPHA"})
	if _, err := q.Enqueue(Event{ID: "e1", Type: "work", ExpiresAt: c.in(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	q2, _ := newGateQueue(t, st, c)
	if q2.DepthByType()["work"] != 1 {
		t.Fatalf("replayed depth = %v, want the event restored alongside the gate", q2.DepthByType())
	}
	if _, ok := q2.Gate("ALPHA"); !ok {
		t.Fatal("gate lost on replay")
	}
}
