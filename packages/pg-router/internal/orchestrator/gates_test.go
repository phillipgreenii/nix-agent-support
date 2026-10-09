package orchestrator

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/event"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/item"
	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// --- Gate Registry through the orchestrator (bead pg2-h63eu) ----------------

// timerTick is the event type the timer emitter fixture below produces.
const timerTick = "timer.tick"

// gatedOrch builds an Orchestrator whose sources are: a plain feedback source
// (blocks on every gate), a worker source exempt from ALPHA only, and a TIMER
// emitter — over a queue with one listener per role. The roles' own
// NonBlockingGates mirror the sources'.
func gatedOrch(t *testing.T, workerExempt []string) (*Orchestrator, *fakeHandler, *eventqueue.Queue) {
	t.Helper()
	feedbackEvts := []event.Event{event.NewItemEvent("feedback.ready", "t", item.Item{ID: "zr-f"})}
	workerEvts := []event.Event{event.NewItemEvent("work.ready", "t", item.Item{ID: "zr-w"})}
	sources := testQuerySet(feedbackEvts, workerEvts)
	sources[1].NonBlockingGates = workerExempt
	sources = append(sources, query.Source{
		Name:  "ticker",
		Query: query.TimerQuery{Meta: query.Meta{EmitTypes: []string{timerTick}}},
	})
	o := newOrch(fastCfg(t), sources)
	o.Reg = append(o.Reg, roles.Role{Name: "tick-listener", Enabled: true, Binds: []string{timerTick}})
	o.Reg[1].NonBlockingGates = workerExempt
	o.Bindings = core.NewBindings(o.Reg.DeclaredBindTypes()...)
	handler := o.Handler.(*fakeHandler)
	q := newTestQueue(t)
	ctx := context.Background()
	for _, r := range o.Reg {
		q.Register(o.NewListener(ctx, r))
	}
	return o, handler, q
}

func dispatchedRoles(h *fakeHandler) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, c := range h.calls {
		out = append(out, c.role.Name)
	}
	slices.Sort(out)
	return out
}

// A blocked emitter is not polled; an emitter that declared the TYPE
// non-blocking still is; the timer emitter is polled whatever gate is up.
func TestProduceTick_gatedEmittersAreNotPolled_exemptAndTimerStillRun(t *testing.T) {
	o, _, q := gatedOrch(t, []string{"ALPHA"})
	if _, err := q.SetGate(eventqueue.GateRequest{Type: "ALPHA"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.SetGate(eventqueue.GateRequest{Type: "BETA"}); err != nil {
		t.Fatal(err)
	}

	rpt, err := o.ProduceTick(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	// worker-source is exempt from ALPHA but BETA still blocks it; feedback
	// blocks on both; the timer ignores every gate.
	if rpt.Blocked["feedback-source"] != "ALPHA" || rpt.Blocked["worker-source"] != "BETA" {
		t.Fatalf("Blocked = %v, want feedback blocked by ALPHA and worker by BETA", rpt.Blocked)
	}
	if _, polled := rpt.LastTick["feedback-source"]; polled {
		t.Error("a blocked emitter must not be polled (LastTick set)")
	}
	if _, polled := rpt.LastTick["ticker"]; !polled {
		t.Error("the timer emitter must be polled while gates are active")
	}
	if d := q.DepthByType(); d["feedback.ready"] != 0 || d["work.ready"] != 0 || d[timerTick] != 1 {
		t.Fatalf("depth = %v, want only the timer tick enqueued", d)
	}

	// Clear BETA: worker (exempt from ALPHA) now runs; feedback still blocked.
	if _, _, err := q.ClearGate("BETA", "op"); err != nil {
		t.Fatal(err)
	}
	rpt, err = o.ProduceTick(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if _, polled := rpt.LastTick["worker-source"]; !polled {
		t.Error("worker-source declared ALPHA non-blocking and must run with only ALPHA active")
	}
	if rpt.Blocked["feedback-source"] != "ALPHA" {
		t.Errorf("feedback-source must stay blocked by ALPHA; Blocked = %v", rpt.Blocked)
	}

	// Clear everything: the previously blocked emitter fires on the very next
	// pass (its LastTick was never advanced while it was blocked).
	if _, _, err := q.ClearGate("ALPHA", "op"); err != nil {
		t.Fatal(err)
	}
	rpt, err = o.ProduceTick(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if _, polled := rpt.LastTick["feedback-source"]; !polled || len(rpt.Blocked) != 0 {
		t.Fatalf("after clearing, feedback-source must be polled immediately; LastTick=%v Blocked=%v", rpt.LastTick, rpt.Blocked)
	}
}

// A blocked listener is not dispatched to while an exempt one still is, and
// queued events wait for the gate to clear.
func TestDispatch_gatedListenerBlockedExemptListenerRuns(t *testing.T) {
	o, handler, q := gatedOrch(t, []string{"ALPHA"})
	// Unexpired events, so the blocked listener's event queues rather than
	// hitting its final attempt.
	exp := time.Now().Add(time.Hour)
	for _, e := range []eventqueue.Event{
		{ID: "f1", Type: "feedback.ready", Payload: map[string]any{"id": "zr-f"}, ExpiresAt: exp},
		{ID: "w1", Type: "work.ready", Payload: map[string]any{"id": "zr-w"}, ExpiresAt: exp},
	} {
		if _, err := q.Enqueue(e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := q.SetGate(eventqueue.GateRequest{Type: "ALPHA"}); err != nil {
		t.Fatal(err)
	}
	q.Dispatch()
	if got := dispatchedRoles(handler); !slices.Equal(got, []string{"worker"}) {
		t.Fatalf("dispatched to %v, want only the ALPHA-exempt worker while ALPHA is up", got)
	}
	if _, _, err := q.ClearGate("ALPHA", "op"); err != nil {
		t.Fatal(err)
	}
	q.Dispatch()
	if got := dispatchedRoles(handler); !slices.Equal(got, []string{"feedback", "worker"}) {
		t.Fatalf("after clearing dispatched to %v, want the queued feedback event delivered too", got)
	}
	_ = o
}

// roleListener exposes the role's registration-time exemptions to the queue.
func TestRoleListener_declaresRoleNonBlockingGates(t *testing.T) {
	o := newOrch(fastCfg(t), nil)
	role := roles.Role{Name: "r", Enabled: true, Binds: []string{"e"}, NonBlockingGates: []string{"X", "Y"}}
	ge, ok := o.NewListener(context.Background(), role).(eventqueue.GateExempter)
	if !ok {
		t.Fatal("roleListener must implement eventqueue.GateExempter")
	}
	if got := ge.NonBlockingGates(); !slices.Equal(got, []string{"X", "Y"}) {
		t.Fatalf("NonBlockingGates = %v", got)
	}
}

// The disk-space watchdog deployment shape (bead pg2-zwdwf): a timer-driven
// listener that declares LOW_DISK_USAGE non-blocking MUST keep being dispatched
// while that very gate is active -- otherwise the gate it sets would stop it
// from ever running again to clear it -- while an ordinary listener (blocking on
// every TYPE) is held back for the whole time the gate is up.
func TestDispatch_diskWatchdogStillRunsWhileLowDiskUsageIsActive(t *testing.T) {
	const diskTick = "disk.check"
	feedbackEvts := []event.Event{event.NewItemEvent("feedback.ready", "t", item.Item{ID: "zr-f"})}
	sources := testQuerySet(feedbackEvts, nil)[:1]
	sources = append(sources, query.Source{
		Name:  "disk-check-tick",
		Query: query.TimerQuery{Meta: query.Meta{EmitTypes: []string{diskTick}}},
	})
	o := newOrch(fastCfg(t), sources)
	o.Reg = roles.RoleSet{
		{Name: "feedback", Enabled: true, Binds: []string{"feedback.ready"}},
		{Name: "disk-watchdog", Enabled: true, Binds: []string{diskTick}, NonBlockingGates: []string{"LOW_DISK_USAGE"}},
	}
	o.Bindings = core.NewBindings(o.Reg.DeclaredBindTypes()...)
	handler := o.Handler.(*fakeHandler)
	q := newTestQueue(t)
	for _, r := range o.Reg {
		q.Register(o.NewListener(context.Background(), r))
	}

	// The watchdog's own gate is up (as it would be after a low-space check).
	if _, err := q.SetGate(eventqueue.GateRequest{Type: "LOW_DISK_USAGE", Owner: "pg-router-disk-watchdog", TTL: 7 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	rpt, err := o.ProduceTick(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if _, polled := rpt.LastTick["disk-check-tick"]; !polled {
		t.Fatalf("the timer emitter must keep ticking under LOW_DISK_USAGE; LastTick=%v Blocked=%v", rpt.LastTick, rpt.Blocked)
	}
	if rpt.Blocked["feedback-source"] != "LOW_DISK_USAGE" {
		t.Errorf("an ordinary emitter must be blocked by LOW_DISK_USAGE; Blocked=%v", rpt.Blocked)
	}

	q.Dispatch()
	if got := dispatchedRoles(handler); !slices.Equal(got, []string{"disk-watchdog"}) {
		t.Fatalf("dispatched to %v, want only the watchdog while LOW_DISK_USAGE is active", got)
	}
}

// The motivating deployment of per-participant gate opt-out (bead pg2-hbin1):
// a router gated on BEAD_SERVER_DOWN (an ordinary gate TYPE -- the core has no
// special handling of it) must still run the participants that declared that
// TYPE non-blocking, while every other participant is held. Through the real
// role-listener dispatch path: only a listener that exempts THIS TYPE runs; one
// that exempts a different TYPE, or none, is held with its event still queued
// and receives it once the gate clears.
func TestDispatch_beadServerDownGateRunsOnlyListenersThatExemptIt(t *testing.T) {
	const gate = "BEAD_SERVER_DOWN"
	const evtType = "work.ready"
	cases := []struct {
		name   string
		exempt []string
		// runsWhileGated: dispatched while the gate is up; otherwise held.
		runsWhileGated bool
	}{
		{name: "exempts-the-active-type", exempt: []string{gate}, runsWhileGated: true},
		{name: "exempts-among-several-types", exempt: []string{"LOW_DISK_USAGE", gate}, runsWhileGated: true},
		{name: "no-opt-out", exempt: nil, runsWhileGated: false},
		{name: "exempts-a-different-type", exempt: []string{"LOW_DISK_USAGE"}, runsWhileGated: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newOrch(fastCfg(t), nil)
			o.Reg = roles.RoleSet{
				{Name: "subject", Enabled: true, Binds: []string{evtType}, NonBlockingGates: tc.exempt},
			}
			o.Bindings = core.NewBindings(o.Reg.DeclaredBindTypes()...)
			handler := o.Handler.(*fakeHandler)
			q := newTestQueue(t)
			for _, r := range o.Reg {
				q.Register(o.NewListener(context.Background(), r))
			}

			// Unexpired, so a held event queues rather than hitting its final attempt.
			if _, err := q.Enqueue(eventqueue.Event{ID: "e1", Type: evtType, Payload: map[string]any{"id": "zr-w"}, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			if _, err := q.SetGate(eventqueue.GateRequest{Type: gate}); err != nil {
				t.Fatal(err)
			}

			q.Dispatch()
			want := []string(nil)
			if tc.runsWhileGated {
				want = []string{"subject"}
			}
			if got := dispatchedRoles(handler); !slices.Equal(got, want) {
				t.Fatalf("while %s is active dispatched to %v, want %v", gate, got, want)
			}

			// Clearing delivers the event to a listener that was held (it was
			// kept queued, not dropped) and does not redeliver to one that ran.
			if _, _, err := q.ClearGate(gate, "op"); err != nil {
				t.Fatal(err)
			}
			q.Dispatch()
			if got := dispatchedRoles(handler); !slices.Equal(got, []string{"subject"}) {
				t.Fatalf("after clearing %s dispatched to %v, want exactly one delivery to [subject]", gate, got)
			}
		})
	}
}

// The emitter-side counterpart under the same gate: with BEAD_SERVER_DOWN
// active, a query that exempts it is still polled, while one with no opt-out
// and one that exempts only a different TYPE are not.
func TestProduceTick_beadServerDownGateOnlyPollsEmittersThatExemptIt(t *testing.T) {
	const gate = "BEAD_SERVER_DOWN"
	src := func(name, emit string, exempt []string) query.Source {
		return query.Source{
			Name:             name,
			Query:            &fakeQuery{Meta: query.Meta{EmitTypes: []string{emit}}},
			NonBlockingGates: exempt,
		}
	}
	sources := query.SourceSet{
		src("exempt-source", "a.ready", []string{gate}),
		src("plain-source", "b.ready", nil),
		src("other-exempt-source", "c.ready", []string{"LOW_DISK_USAGE"}),
	}
	o := newOrch(fastCfg(t), sources)
	o.Reg = roles.RoleSet{{Name: "r", Enabled: true, Binds: []string{"a.ready", "b.ready", "c.ready"}}}
	o.Bindings = core.NewBindings(o.Reg.DeclaredBindTypes()...)
	q := newTestQueue(t)
	if _, err := q.SetGate(eventqueue.GateRequest{Type: gate}); err != nil {
		t.Fatal(err)
	}

	rpt, err := o.ProduceTick(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if _, polled := rpt.LastTick["exempt-source"]; !polled {
		t.Errorf("an emitter exempting %s must still be polled; LastTick=%v Blocked=%v", gate, rpt.LastTick, rpt.Blocked)
	}
	for _, name := range []string{"plain-source", "other-exempt-source"} {
		if _, polled := rpt.LastTick[name]; polled {
			t.Errorf("%s must not be polled while %s is active", name, gate)
		}
		if rpt.Blocked[name] != gate {
			t.Errorf("Blocked[%s] = %q, want %s", name, rpt.Blocked[name], gate)
		}
	}
}
