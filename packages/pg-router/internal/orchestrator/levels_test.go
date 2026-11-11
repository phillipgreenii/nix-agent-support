package orchestrator

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/event"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/item"
	"github.com/phillipgreenii/pg-router/internal/levels"
	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// timerTick is the event type the timer emitter fixture below produces.
const timerTick = "timer.tick"

// stubLevels is a mutable level source for the queue under test.
type stubLevels struct{ cur map[string]string }

func (s *stubLevels) snapshot() levels.Snapshot {
	out := levels.Snapshot{Now: time.Now(), Levels: map[string]levels.Level{}}
	for r, v := range s.cur {
		out.Levels[r] = levels.Level{Value: v, State: levels.StateHeld, Source: "sensor", Owner: "s"}
	}
	return out
}

func orchRegistry() levels.Registry {
	return levels.Registry{"disk": {Levels: []string{"PLENTY", "OK", "LOW"}, Unknown: "PLENTY"}}
}

func requireDiskOK(t *testing.T) config.Compiled {
	t.Helper()
	return config.Compiled{Requirement: levels.Requirement{Clauses: []levels.Clause{
		{Text: "disk >= OK", Resource: "disk", Allowed: []string{"PLENTY", "OK"}},
	}}}
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

func levelQueue(t *testing.T, src *stubLevels) *eventqueue.Queue {
	t.Helper()
	q, err := eventqueue.New(eventqueue.NewMemStore(), eventqueue.WithLevelSource(src.snapshot), eventqueue.WithRegistry(orchRegistry()))
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// leveledOrch builds an Orchestrator with a feedback source (its role requires
// disk >= OK), a worker source (its role requires nothing) and a TIMER emitter
// whose listener requires nothing, registered on a level-aware queue.
func leveledOrch(t *testing.T, src *stubLevels) (*Orchestrator, *fakeHandler, *eventqueue.Queue) {
	t.Helper()
	feedbackEvts := []event.Event{event.NewItemEvent("feedback.ready", "t", item.Item{ID: "zr-f"})}
	workerEvts := []event.Event{event.NewItemEvent("work.ready", "t", item.Item{ID: "zr-w"})}
	sources := testQuerySet(feedbackEvts, workerEvts)
	sources = append(sources, query.Source{
		Name:  "ticker",
		Query: query.TimerQuery{Meta: query.Meta{EmitTypes: []string{timerTick}}},
	})
	cfg := fastCfg(t)
	cfg.Requirements = map[string]config.Compiled{"feedback": requireDiskOK(t)}
	o := newOrch(cfg, sources)
	o.Cfg.Requirements = cfg.Requirements
	o.Reg = append(o.Reg, roles.Role{Name: "tick-listener", Enabled: true, Binds: []string{timerTick}})
	o.Bindings = core.NewBindings(o.Reg.DeclaredBindTypes()...)
	q := levelQueue(t, src)
	for _, r := range o.Reg {
		q.Register(o.NewListener(context.Background(), r))
	}
	o.RegisterLevels(q)
	return o, o.Handler.(*fakeHandler), q
}

// A source whose only consumer is blocked is not polled; a source with an
// unconstrained consumer, and the timer, still run; recovery polls at once.
func TestProduceTick_levelBlockedEmitterIsNotPolled_othersAndTimerStillRun(t *testing.T) {
	src := &stubLevels{cur: map[string]string{"disk": "LOW"}}
	o, _, q := leveledOrch(t, src)
	rpt, err := o.ProduceTick(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if rpt.Blocked["feedback-source"] != "disk" {
		t.Fatalf("Blocked = %v, want feedback-source blocked by disk", rpt.Blocked)
	}
	if _, polled := rpt.LastTick["feedback-source"]; polled {
		t.Error("a blocked emitter must not be polled (LastTick set)")
	}
	for _, name := range []string{"worker-source", "ticker"} {
		if _, polled := rpt.LastTick[name]; !polled {
			t.Errorf("%s has an unconstrained consumer (or is the timer) and must be polled; Blocked=%v", name, rpt.Blocked)
		}
	}
	src.cur["disk"] = "OK"
	rpt, err = o.ProduceTick(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if _, polled := rpt.LastTick["feedback-source"]; !polled || len(rpt.Blocked) != 0 {
		t.Fatalf("after recovery feedback-source must be polled immediately; LastTick=%v Blocked=%v", rpt.LastTick, rpt.Blocked)
	}
}

// A blocked listener is not dispatched to while an unconstrained one still is,
// and queued events wait for the level to recover.
func TestDispatch_levelBlockedListenerWaitsUnconstrainedListenerRuns(t *testing.T) {
	src := &stubLevels{cur: map[string]string{"disk": "LOW"}}
	_, handler, q := leveledOrch(t, src)
	exp := time.Now().Add(time.Hour)
	for _, e := range []eventqueue.Event{
		{ID: "f1", Type: "feedback.ready", Payload: map[string]any{"id": "zr-f"}, ExpiresAt: exp},
		{ID: "w1", Type: "work.ready", Payload: map[string]any{"id": "zr-w"}, ExpiresAt: exp},
	} {
		if _, err := q.Enqueue(e); err != nil {
			t.Fatal(err)
		}
	}
	q.Dispatch()
	if got := dispatchedRoles(handler); !slices.Equal(got, []string{"worker"}) {
		t.Fatalf("dispatched to %v, want only the unconstrained worker while disk is LOW", got)
	}
	src.cur["disk"] = "PLENTY"
	q.Dispatch()
	if got := dispatchedRoles(handler); !slices.Equal(got, []string{"feedback", "worker"}) {
		t.Fatalf("after recovery dispatched to %v, want the queued feedback event delivered too", got)
	}
}

// The disk-watchdog deployment shape: a listener with NO requirement keeps being
// dispatched while disk is LOW (otherwise it could never run to fix the
// shortage) while an ordinary role with a requirement is held back.
func TestDispatch_registrationWithoutRequirementStillRunsAtLowDisk(t *testing.T) {
	const diskTick = "disk.check"
	src := &stubLevels{cur: map[string]string{"disk": "LOW"}}
	feedbackEvts := []event.Event{event.NewItemEvent("feedback.ready", "t", item.Item{ID: "zr-f"})}
	sources := testQuerySet(feedbackEvts, nil)[:1]
	sources = append(sources, query.Source{
		Name:  "disk-check-tick",
		Query: query.TimerQuery{Meta: query.Meta{EmitTypes: []string{diskTick}}},
	})
	cfg := fastCfg(t)
	cfg.Requirements = map[string]config.Compiled{"feedback": requireDiskOK(t)}
	o := newOrch(cfg, sources)
	o.Cfg.Requirements = cfg.Requirements
	o.Reg = roles.RoleSet{
		{Name: "feedback", Enabled: true, Binds: []string{"feedback.ready"}},
		{Name: "disk-watchdog", Enabled: true, Binds: []string{diskTick}},
	}
	o.Bindings = core.NewBindings(o.Reg.DeclaredBindTypes()...)
	q := levelQueue(t, src)
	for _, r := range o.Reg {
		q.Register(o.NewListener(context.Background(), r))
	}
	o.RegisterLevels(q)
	rpt, err := o.ProduceTick(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if _, polled := rpt.LastTick["disk-check-tick"]; !polled {
		t.Fatalf("the timer emitter must keep ticking at LOW disk; LastTick=%v Blocked=%v", rpt.LastTick, rpt.Blocked)
	}
	if rpt.Blocked["feedback-source"] != "disk" {
		t.Errorf("the constrained emitter must be blocked by disk; Blocked=%v", rpt.Blocked)
	}
	q.Dispatch()
	if got := dispatchedRoles(o.Handler.(*fakeHandler)); !slices.Equal(got, []string{"disk-watchdog"}) {
		t.Fatalf("dispatched to %v, want only the watchdog at LOW disk", got)
	}
}

// RegisterLevels: a disabled consumer does not keep an emitter polled, and a
// role's requirement reaches the queue under its role name.
func TestRegisterLevels_disabledConsumerDoesNotKeepAnEmitterPolled(t *testing.T) {
	src := &stubLevels{cur: map[string]string{"disk": "LOW"}}
	cfg := fastCfg(t)
	cfg.Requirements = map[string]config.Compiled{"a": requireDiskOK(t), "b": {}}
	sources := query.SourceSet{{Name: "only-source", Query: &fakeQuery{Meta: query.Meta{EmitTypes: []string{"x.ready"}}}}}
	o := newOrch(cfg, sources)
	o.Cfg.Requirements = cfg.Requirements
	o.Reg = roles.RoleSet{
		{Name: "a", Enabled: true, Binds: []string{"x.ready"}},
		{Name: "b", Enabled: false, Binds: []string{"x.ready"}}, // no requirement, but disabled
	}
	q := levelQueue(t, src)
	o.RegisterLevels(q)
	if r, blocked := q.EmitterBlockedBy("only-source"); !blocked || r != "disk" {
		t.Fatalf("EmitterBlockedBy = (%q, %v), want (disk, true): only the enabled, blocked consumer counts", r, blocked)
	}
	if err := q.CheckPull("a"); err == nil {
		t.Fatal("role a's requirement must reach the queue under its role name")
	}
	if err := q.CheckPull("b"); err != nil {
		t.Fatalf("role b has no requirement: %v", err)
	}
}
