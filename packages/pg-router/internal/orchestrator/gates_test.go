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
	o := newOrch(fastCfg(), sources)
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
	o := newOrch(fastCfg(), nil)
	role := roles.Role{Name: "r", Enabled: true, Binds: []string{"e"}, NonBlockingGates: []string{"X", "Y"}}
	ge, ok := o.NewListener(context.Background(), role).(eventqueue.GateExempter)
	if !ok {
		t.Fatal("roleListener must implement eventqueue.GateExempter")
	}
	if got := ge.NonBlockingGates(); !slices.Equal(got, []string{"X", "Y"}) {
		t.Fatalf("NonBlockingGates = %v", got)
	}
}
