package command_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
)

func TestStartCycleSnapshotsTitleAndMinutes(t *testing.T) {
	tests := []struct {
		name    string
		typ     string
		minutes *int
		title   string
		planned int
	}{
		{"the type's own minutes", deepWork, nil, deepWorkTitle, 50},
		{"an override", deepWork, intPtr(15), deepWorkTitle, 15},
		{"another type", review, nil, reviewTitle, 25},
		{"a type outside the active profile", "page-response", nil, "Page response", 25},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := envOf(t, bootstrapped(t), at(10))
			p := mustPlan(t, env, command.StartCycle{Type: tc.typ, Minutes: tc.minutes})
			e := only(t, p)
			got := e.Payload.(event.CycleStarted)
			want := event.CycleStarted{CycleID: got.CycleID, Type: tc.typ, Title: tc.title, PlannedMinutes: tc.planned}
			if got != want {
				t.Errorf("payload = %+v, want %+v", got, want)
			}
			if want := env.Config.CycleMinutes(tc.typ, tc.minutes); got.PlannedMinutes != want {
				t.Errorf("planned_minutes = %d, CycleMinutes says %d", got.PlannedMinutes, want)
			}
			if _, err := event.ParseID(string(got.CycleID)); err != nil || event.ID(got.CycleID) == e.ID {
				t.Errorf("cycle id %q is not a fresh ULID apart from the event id %s (%v)", got.CycleID, e.ID, err)
			}
			if c, ok := p.Candidate.Running(); !ok || c.ID != got.CycleID {
				t.Errorf("candidate focus = %v, %v, want the new cycle", c.ID, ok)
			}
		})
	}

	t.Run("an unknown type", func(t *testing.T) {
		r := mustReject(t, envOf(t, bootstrapped(t), at(10)), command.StartCycle{Type: "nothing"}, command.ReasonUnknownCycleType)
		if r.Reason.Status() != 400 || !strings.Contains(r.Message, `"nothing"`) {
			t.Errorf("status %d, message %q", r.Reason.Status(), r.Message)
		}
	})

	t.Run("a client id is the event's, never the cycle's", func(t *testing.T) {
		e := only(t, mustPlan(t, envOf(t, bootstrapped(t), at(10)), command.StartCycle{ID: clientID, Type: deepWork}))
		if e.ID != clientID || event.ID(e.Payload.(event.CycleStarted).CycleID) == clientID {
			t.Errorf("event id %s, cycle id %s", e.ID, e.Payload.(event.CycleStarted).CycleID)
		}
	})

	t.Run("a blank type", func(t *testing.T) {
		mustReject(t, envOf(t, bootstrapped(t), at(10)), command.StartCycle{}, command.ReasonInvalidRequest)
	})
}

func TestStartWhileRunningSetsInterruptsFromRunningAtEffectiveAt(t *testing.T) {
	t.Run("the running cycle is interrupted by one start event", func(t *testing.T) {
		b := bootstrapped(t)
		b.add(0, startOf(cycleA, deepWork))
		p := mustPlan(t, envOf(t, b, at(10)), command.StartCycle{Type: review})
		e := only(t, p)
		if got := e.Payload.(event.CycleStarted).Interrupts; got != cycleA {
			t.Errorf("interrupts = %q, want %s", got, cycleA)
		}
		if got := status(t, p.Candidate, cycleA, at(10)); got != projection.Paused {
			t.Errorf("A at the start = %s, want paused", got)
		}
	})

	t.Run("a backdated start interrupts the cycle running then", func(t *testing.T) {
		b := bootstrapped(t)
		b.add(0, startOf(cycleA, deepWork))
		b.add(30, stopOf(cycleA))
		p := mustPlan(t, envOf(t, b, at(40)), command.StartCycle{Type: review, EffectiveAt: ptr(at(10))})
		if got := only(t, p).Payload.(event.CycleStarted).Interrupts; got != cycleA {
			t.Errorf("interrupts = %q, want %s, the cycle running at the start's instant", got, cycleA)
		}
	})

	t.Run("nothing running, nothing interrupted", func(t *testing.T) {
		b := bootstrapped(t)
		b.add(0, startOf(cycleA, deepWork))
		b.add(5, pauseOf(cycleA))
		p := mustPlan(t, envOf(t, b, at(10)), command.StartCycle{Type: review})
		if got := only(t, p).Payload.(event.CycleStarted).Interrupts; got != "" {
			t.Errorf("interrupts = %q, want none", got)
		}
	})
}

// outcome is what a cell of the state table expects: the event types a yes
// cell plans, a no-op, or a rejection.
type outcome struct {
	types  []event.Type
	noop   bool
	reason command.Reason
}

func yes(types ...event.Type) outcome { return outcome{types: types} }
func reject(r command.Reason) outcome { return outcome{reason: r} }
func noop() outcome                   { return outcome{noop: true} }
func typesOf(p command.Plan) []event.Type {
	return slicesMap(p.Events, func(e event.Event) event.Type { return e.Type })
}

func slicesMap[T, U any](in []T, f func(T) U) []U {
	var out []U
	for _, v := range in {
		out = append(out, f(v))
	}
	return out
}

func checkOutcome(t *testing.T, env command.Env, c command.Command, want outcome) command.Plan {
	t.Helper()
	switch {
	case want.reason != "":
		mustReject(t, env, c, want.reason)
		return command.Plan{}
	case want.noop:
		mustNoOp(t, env, c)
		return command.Plan{}
	}
	p := mustPlan(t, env, c)
	if got := typesOf(p); !slices.Equal(got, want.types) {
		t.Errorf("events %v, want %v", got, want.types)
	}
	return p
}

func TestCycleStateTable(t *testing.T) {
	// Cycle A starts at 0 and, for the paused and stopped states, is paused or
	// stopped at 10. Where a cell needs another cycle running, B starts at 20.
	// The request is at 30.
	setup := map[projection.CycleStatus]func(b *logb){
		projection.Running: func(b *logb) { b.add(0, startOf(cycleA, deepWork)) },
		projection.Paused:  func(b *logb) { b.add(0, startOf(cycleA, deepWork)); b.add(10, pauseOf(cycleA)) },
		projection.Stopped: func(b *logb) { b.add(0, startOf(cycleA, deepWork)); b.add(10, stopOf(cycleA)) },
	}
	pause := command.PauseCycle{CycleID: cycleA}
	resume := command.ResumeCycle{CycleID: cycleA}
	boost := command.BoostCycle{CycleID: cycleA, Minutes: 5}
	stop := command.StopCycle{CycleID: cycleA}
	annotate := command.AnnotateCycle{CycleID: cycleA, Note: "done"}
	switchTo := command.SwitchCycle{To: cycleA}
	switchTypes := []event.Type{event.TypeCyclePaused, event.TypeCycleResumed, event.TypeBatchCommitted}

	cells := []struct {
		state   projection.CycleStatus
		another bool // B runs at the request's instant
		cmd     command.Command
		want    outcome
	}{
		{projection.Running, false, pause, yes(event.TypeCyclePaused)},
		{projection.Running, false, resume, noop()},
		{projection.Running, false, boost, yes(event.TypeCycleBoosted)},
		{projection.Running, false, stop, yes(event.TypeCycleStopped)},
		{projection.Running, false, annotate, yes(event.TypeCycleAnnotated)},
		{projection.Running, false, switchTo, noop()},

		{projection.Paused, false, pause, noop()},
		{projection.Paused, false, resume, yes(event.TypeCycleResumed)},
		{projection.Paused, true, resume, reject(command.ReasonAnotherCycleRunning)},
		{projection.Paused, false, boost, yes(event.TypeCycleBoosted)},
		{projection.Paused, false, stop, yes(event.TypeCycleStopped)},
		{projection.Paused, false, annotate, yes(event.TypeCycleAnnotated)},
		{projection.Paused, true, switchTo, yes(switchTypes...)},

		{projection.Stopped, false, pause, reject(command.ReasonCycleStopped)},
		{projection.Stopped, false, resume, reject(command.ReasonCycleStopped)},
		{projection.Stopped, false, boost, reject(command.ReasonCycleStopped)},
		{projection.Stopped, false, stop, noop()},
		{projection.Stopped, false, annotate, yes(event.TypeCycleAnnotated)},
		{projection.Stopped, true, switchTo, reject(command.ReasonCycleStopped)},
	}
	for _, cell := range cells {
		name := string(cell.state) + " " + cell.cmd.Name()
		if cell.another {
			name += " while another runs"
		}
		t.Run(name, func(t *testing.T) {
			b := bootstrapped(t)
			setup[cell.state](b)
			if cell.another {
				b.add(20, startOf(cycleB, review))
			}
			env := envOf(t, b, at(30))
			if got := status(t, env.Model, cycleA, at(30)); got != cell.state {
				t.Fatalf("setup: A is %s, want %s", got, cell.state)
			}
			checkOutcome(t, env, cell.cmd, cell.want)
			if cell.want.reason == command.ReasonAnotherCycleRunning {
				r := mustReject(t, env, cell.cmd, cell.want.reason)
				if !slices.Contains(cycleIDsOf(r), cycleB) {
					t.Errorf("Cycles = %v, want the running cycle %s named", cycleIDsOf(r), cycleB)
				}
			}
		})
	}
}

func TestRepeatedStateCommandsAreJudgedAtTheEffectiveInstant(t *testing.T) {
	// A starts at 0, pauses at 10, resumes at 20; the request is read at 100.
	base := func(t *testing.T) *logb {
		b := bootstrapped(t)
		b.add(0, startOf(cycleA, deepWork))
		b.add(10, pauseOf(cycleA))
		b.add(20, resumeOf(cycleA))
		return b
	}
	t.Run("a pause when it was already paused is a no-op although it runs now", func(t *testing.T) {
		mustNoOp(t, envOf(t, base(t), at(100)), command.PauseCycle{CycleID: cycleA, EffectiveAt: ptr(at(15))})
	})
	t.Run("a pause when it was running, with a later pause recorded, overlaps", func(t *testing.T) {
		b := base(t)
		b.add(30, pauseOf(cycleA))
		mustReject(t, envOf(t, b, at(100)), command.PauseCycle{CycleID: cycleA, EffectiveAt: ptr(at(25))}, command.ReasonCycleSegmentsOverlap)
	})
	t.Run("a stop later than the cycle's stop is a no-op", func(t *testing.T) {
		b := base(t)
		b.add(60, stopOf(cycleA))
		mustNoOp(t, envOf(t, b, at(100)), command.StopCycle{CycleID: cycleA, EffectiveAt: ptr(at(70))})
	})
	t.Run("a stop earlier than the cycle's stop is cycle_stopped, pointing at that stop", func(t *testing.T) {
		b := base(t)
		stopped := b.add(60, stopOf(cycleA))
		r := mustReject(t, envOf(t, b, at(100)), command.StopCycle{CycleID: cycleA, EffectiveAt: ptr(at(50))}, command.ReasonCycleStopped)
		if !strings.Contains(r.Message, "correct") || !strings.Contains(r.Message, string(stopped)) {
			t.Errorf("Message %q does not say to correct the time of stop %s", r.Message, stopped)
		}
		if !slices.Contains(r.Events, stopped) {
			t.Errorf("Events = %v, want the stop %s", r.Events, stopped)
		}
	})
	t.Run("a stop at the instant of the last resume is an empty segment", func(t *testing.T) {
		mustReject(t, envOf(t, base(t), at(100)), command.StopCycle{CycleID: cycleA, EffectiveAt: ptr(at(20))}, command.ReasonEmptyRunningSegment)
	})
	t.Run("a stop at the instant of the first start", func(t *testing.T) {
		mustReject(t, envOf(t, base(t), at(100)), command.StopCycle{CycleID: cycleA, EffectiveAt: ptr(at(0))}, command.ReasonStopNotAfterStart)
	})
	t.Run("a no-op stores nothing even with an id", func(t *testing.T) {
		p := mustPlan(t, envOf(t, base(t), at(100)), command.ResumeCycle{ID: clientID, CycleID: cycleA})
		if !p.NoOp || len(p.Events) != 0 || p.Candidate != nil || p.BatchID != "" {
			t.Errorf("plan = %+v, want a bare no-op", p)
		}
	})
}

func TestStatusAtBoundariesDecideNoOps(t *testing.T) {
	// A starts at 10, pauses at 20, resumes at 30, stops at 40.
	b := bootstrapped(t)
	b.add(10, startOf(cycleA, deepWork))
	b.add(20, pauseOf(cycleA))
	b.add(30, resumeOf(cycleA))
	b.add(40, stopOf(cycleA))
	env := envOf(t, b, at(100))
	eff := func(n int) *time.Time { return ptr(at(n)) }

	t.Run("before the start nothing is a no-op", func(t *testing.T) {
		mustReject(t, env, command.PauseCycle{CycleID: cycleA, EffectiveAt: eff(5)}, command.ReasonCycleEventBeforeStart)
		mustReject(t, env, command.StopCycle{CycleID: cycleA, EffectiveAt: eff(5)}, command.ReasonCycleEventBeforeStart)
		mustReject(t, env, command.ResumeCycle{CycleID: cycleA, EffectiveAt: eff(5)}, command.ReasonCycleEventBeforeStart)
	})
	t.Run("at the start instant it is running", func(t *testing.T) {
		mustNoOp(t, env, command.ResumeCycle{CycleID: cycleA, EffectiveAt: eff(10)})
	})
	t.Run("at a resume instant it is running", func(t *testing.T) {
		mustNoOp(t, env, command.ResumeCycle{CycleID: cycleA, EffectiveAt: eff(30)})
	})
	t.Run("at a pause instant it is paused", func(t *testing.T) {
		mustNoOp(t, env, command.PauseCycle{CycleID: cycleA, EffectiveAt: eff(20)})
	})
	t.Run("at the stop instant it is stopped", func(t *testing.T) {
		mustNoOp(t, env, command.StopCycle{CycleID: cycleA, EffectiveAt: eff(40)})
	})
}

func TestNoOpNoteNamesTheCycleAndSince(t *testing.T) {
	// A, deep work, starts at 08:00 New York and is paused at 10:00 (14:00Z).
	b := bootstrapped(t)
	b.add(0, startOf(cycleA, deepWork))
	b.add(120, pauseOf(cycleA))
	b.add(130, startOf(cycleB, review))
	b.add(140, pauseOf(cycleB))
	b.add(150, resumeOf(cycleB))
	stopped := bootstrapped(t)
	stopped.add(0, startOf(cycleA, deepWork))
	stopped.add(120, stopOf(cycleA))

	tests := []struct {
		name string
		b    *logb
		cmd  command.Command
		want string
	}{
		{"paused", b, command.PauseCycle{CycleID: cycleA}, "Deep work cycle has been paused since 2026-10-07T14:00:00.000Z (10:00 America/New_York)."},
		{"running", b, command.ResumeCycle{CycleID: cycleB}, "Review cycle has been running since 2026-10-07T14:30:00.000Z (10:30 America/New_York)."},
		{"stopped", stopped, command.StopCycle{CycleID: cycleA}, "Deep work cycle has been stopped since 2026-10-07T14:00:00.000Z (10:00 America/New_York)."},
		{"the focus", b, command.SwitchCycle{To: cycleB}, "Review cycle has been running since 2026-10-07T14:30:00.000Z (10:30 America/New_York), so it is already the focus."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustNoOp(t, envOf(t, tc.b, at(200)), tc.cmd); got != tc.want {
				t.Errorf("Note = %q, want %q", got, tc.want)
			}
		})
	}
}

// targetOf is the cycle the one event of a plan is about.
func targetOf(t *testing.T, p command.Plan) event.CycleID {
	t.Helper()
	switch pl := only(t, p).Payload.(type) {
	case event.CyclePaused:
		return pl.CycleID
	case event.CycleBoosted:
		return pl.CycleID
	case event.CycleStopped:
		return pl.CycleID
	case event.CycleResumed:
		return pl.CycleID
	case event.CycleAnnotated:
		return pl.CycleID
	}
	t.Fatalf("not a cycle event: %T", p.Events[0].Payload)
	return ""
}

func TestOmittedCycleIDForPauseBoostAndStop(t *testing.T) {
	t.Run("the cycle running at the request's instant", func(t *testing.T) {
		b := bootstrapped(t)
		b.add(0, startOf(cycleA, deepWork))
		b.add(10, interruptOf(cycleB, cycleA, review))
		env := envOf(t, b, at(30))
		for _, c := range []command.Command{command.PauseCycle{}, command.BoostCycle{Minutes: 5}, command.StopCycle{}} {
			if got := targetOf(t, mustPlan(t, env, c)); got != cycleB {
				t.Errorf("%s targets %s, want the running %s", c.Name(), got, cycleB)
			}
		}
		if got := targetOf(t, mustPlan(t, env, command.BoostCycle{Minutes: 5, EffectiveAt: ptr(at(5))})); got != cycleA {
			t.Errorf("a backdated boost targets %s, want %s, the cycle running then", got, cycleA)
		}
	})

	t.Run("a backdated pause picks the cycle running then", func(t *testing.T) {
		b := bootstrapped(t)
		b.add(0, startOf(cycleA, deepWork))
		b.add(20, stopOf(cycleA))
		b.add(30, startOf(cycleB, review))
		if got := targetOf(t, mustPlan(t, envOf(t, b, at(40)), command.PauseCycle{EffectiveAt: ptr(at(10))})); got != cycleA {
			t.Errorf("targets %s, want %s", got, cycleA)
		}
	})

	t.Run("else the only cycle that is not stopped", func(t *testing.T) {
		b := bootstrapped(t)
		b.add(0, startOf(cycleA, deepWork))
		b.add(10, stopOf(cycleA))
		b.add(20, startOf(cycleB, review))
		b.add(25, pauseOf(cycleB))
		env := envOf(t, b, at(30))
		if got := targetOf(t, mustPlan(t, env, command.BoostCycle{Minutes: 5})); got != cycleB {
			t.Errorf("boost targets %s, want %s", got, cycleB)
		}
		if got := targetOf(t, mustPlan(t, env, command.StopCycle{})); got != cycleB {
			t.Errorf("stop targets %s, want %s", got, cycleB)
		}
		mustNoOp(t, env, command.PauseCycle{})
	})

	t.Run("several candidates are ambiguous", func(t *testing.T) {
		b := bootstrapped(t)
		b.add(0, startOf(cycleA, deepWork))
		b.add(10, pauseOf(cycleA))
		b.add(20, startOf(cycleB, review))
		b.add(25, pauseOf(cycleB))
		env := envOf(t, b, at(30))
		for _, c := range []command.Command{command.PauseCycle{}, command.BoostCycle{Minutes: 5}, command.StopCycle{}} {
			r := mustReject(t, env, c, command.ReasonCycleAmbiguous)
			if !sameIDs(cycleIDsOf(r), cycleA, cycleB) {
				t.Errorf("%s: Cycles = %v, want both", c.Name(), cycleIDsOf(r))
			}
		}
	})

	t.Run("none is no_running_cycle", func(t *testing.T) {
		mustReject(t, envOf(t, bootstrapped(t), at(30)), command.PauseCycle{}, command.ReasonNoRunningCycle)
		mustReject(t, envOf(t, bootstrapped(t), at(30)), command.BoostCycle{Minutes: 5}, command.ReasonNoRunningCycle)
	})

	t.Run("a repeated stop after the only cycle stopped", func(t *testing.T) {
		b := bootstrapped(t)
		b.add(0, startOf(cycleA, deepWork))
		b.add(10, stopOf(cycleA))
		env := envOf(t, b, at(30))
		r := mustReject(t, env, command.StopCycle{}, command.ReasonNoRunningCycle)
		if r.Reason.Status() != 409 {
			t.Errorf("status %d, want 409", r.Reason.Status())
		}
		mustNoOp(t, env, command.StopCycle{CycleID: cycleA})
	})
}

func TestAmbiguousCycleMustBeNamed(t *testing.T) {
	one := func(t *testing.T) *logb {
		b := bootstrapped(t)
		b.add(0, startOf(cycleA, deepWork))
		b.add(5, stopOf(cycleA))
		b.add(10, startOf(cycleB, review))
		b.add(20, pauseOf(cycleB))
		return b
	}
	t.Run("exactly one cycle that is not stopped applies", func(t *testing.T) {
		env := envOf(t, one(t), at(30))
		if got := targetOf(t, mustPlan(t, env, command.ResumeCycle{})); got != cycleB {
			t.Errorf("resume targets %s, want %s", got, cycleB)
		}
		if got := targetOf(t, mustPlan(t, env, command.AnnotateCycle{Note: "x"})); got != cycleB {
			t.Errorf("annotate targets %s, want %s", got, cycleB)
		}
	})
	t.Run("two or more are cycle_ambiguous with ids and titles", func(t *testing.T) {
		b := one(t)
		b.add(25, startOf(cycleC, deepWork))
		env := envOf(t, b, at(30))
		for _, c := range []command.Command{command.ResumeCycle{}, command.AnnotateCycle{Note: "x"}} {
			r := mustReject(t, env, c, command.ReasonCycleAmbiguous)
			if r.Reason.Status() != 400 {
				t.Errorf("status %d, want 400", r.Reason.Status())
			}
			want := []command.CycleRef{
				{ID: cycleB, Title: reviewTitle, Status: projection.Paused, StartedAt: at(10)},
				{ID: cycleC, Title: deepWorkTitle, Status: projection.Running, StartedAt: at(25)},
			}
			if !slices.EqualFunc(r.Cycles, want, func(a, b command.CycleRef) bool {
				return a.ID == b.ID && a.Title == b.Title && a.Status == b.Status && a.StartedAt.Equal(b.StartedAt)
			}) {
				t.Errorf("%s: Cycles = %+v, want %+v", c.Name(), r.Cycles, want)
			}
			for _, s := range []string{string(cycleB), reviewTitle, string(cycleC), deepWorkTitle} {
				if !strings.Contains(r.Message, s) {
					t.Errorf("Message %q does not name %q", r.Message, s)
				}
			}
		}
	})
	t.Run("none is no_running_cycle", func(t *testing.T) {
		b := bootstrapped(t)
		b.add(0, startOf(cycleA, deepWork))
		b.add(5, stopOf(cycleA))
		env := envOf(t, b, at(30))
		mustReject(t, env, command.ResumeCycle{}, command.ReasonNoRunningCycle)
		mustReject(t, env, command.AnnotateCycle{Note: "x"}, command.ReasonNoRunningCycle)
	})
	t.Run("a stopped cycle named by id is annotated", func(t *testing.T) {
		env := envOf(t, one(t), at(30))
		if got := targetOf(t, mustPlan(t, env, command.AnnotateCycle{CycleID: cycleA, Note: "x"})); got != cycleA {
			t.Errorf("annotate targets %s, want %s", got, cycleA)
		}
	})
}

func TestBoostMustBePositive(t *testing.T) {
	b := bootstrapped(t)
	b.add(0, startOf(cycleA, deepWork))
	env := envOf(t, b, at(10))
	for _, n := range []int{0, -5} {
		mustReject(t, env, command.BoostCycle{CycleID: cycleA, Minutes: n}, command.ReasonInvalidRequest)
	}
	p := mustPlan(t, env, command.BoostCycle{CycleID: cycleA, Minutes: 1})
	if got := only(t, p).Payload.(event.CycleBoosted); got != (event.CycleBoosted{CycleID: cycleA, Minutes: 1}) {
		t.Errorf("payload = %+v", got)
	}
}

func TestAnnotateAnyStateIncludingStopped(t *testing.T) {
	setups := map[string]func(b *logb){
		"running": func(b *logb) { b.add(0, startOf(cycleA, deepWork)) },
		"paused":  func(b *logb) { b.add(0, startOf(cycleA, deepWork)); b.add(10, pauseOf(cycleA)) },
		"stopped": func(b *logb) { b.add(0, startOf(cycleA, deepWork)); b.add(10, stopOf(cycleA)) },
	}
	for name, setup := range setups {
		t.Run(name, func(t *testing.T) {
			b := bootstrapped(t)
			setup(b)
			kv := []event.KV{{Key: "ticket", Value: "T-1"}, {Key: "pr", Value: "42"}}
			p := mustPlan(t, envOf(t, b, at(30)), command.AnnotateCycle{CycleID: cycleA, Note: "Wrote the draft", KV: kv})
			got := only(t, p).Payload.(event.CycleAnnotated)
			if got.CycleID != cycleA || got.Note != "Wrote the draft" || !slices.Equal(got.KV, kv) {
				t.Errorf("payload = %+v", got)
			}
			c, _ := p.Candidate.Cycle(cycleA)
			if c.Note != "Wrote the draft" || !slices.Equal(c.KV, kv) {
				t.Errorf("candidate note %q, kv %v", c.Note, c.KV)
			}
		})
	}
}

func TestAnnotateKeyRules(t *testing.T) {
	b := bootstrapped(t)
	b.add(0, startOf(cycleA, deepWork))
	env := envOf(t, b, at(10))
	annotate := func(kv ...event.KV) command.AnnotateCycle { return command.AnnotateCycle{CycleID: cycleA, KV: kv} }

	for _, key := range []string{"", "Ticket", "a b", "ticket!", "tïcket", "a.b"} {
		mustReject(t, env, annotate(event.KV{Key: key, Value: "x"}), command.ReasonInvalidRequest)
	}
	r := mustReject(t, env, annotate(event.KV{Key: "ticket", Value: "x"}, event.KV{Key: "cycle_type", Value: "x"}), command.ReasonReservedKey)
	if r.Reason.Status() != 400 {
		t.Errorf("reserved_key status %d, want 400", r.Reason.Status())
	}

	repeated := []event.KV{{Key: "pr", Value: "1"}, {Key: "pr", Value: "2"}, {Key: "a_b-9", Value: ""}}
	if got := only(t, mustPlan(t, env, annotate(repeated...))).Payload.(event.CycleAnnotated).KV; !slices.Equal(got, repeated) {
		t.Errorf("kv = %v, want %v", got, repeated)
	}

	control := "line one\nline\ttwo\x01\x7f"
	got := only(t, mustPlan(t, env, command.AnnotateCycle{CycleID: cycleA, Note: control, KV: []event.KV{{Key: "pr", Value: control}}})).Payload.(event.CycleAnnotated)
	if got.Note != control || got.KV[0].Value != control {
		t.Errorf("control characters were not stored as given: %q, %q", got.Note, got.KV[0].Value)
	}
}

// interrupted is a log in which B starts at 0 and A starts at 10 interrupting
// it, so A is the focus and B is dimmed.
func interrupted(t *testing.T) *logb {
	t.Helper()
	b := bootstrapped(t)
	b.add(0, startOf(cycleB, review))
	b.add(10, interruptOf(cycleA, cycleB, deepWork))
	return b
}

func TestSwitchCycleBatch(t *testing.T) {
	for name, id := range map[string]event.ID{"without an id": "", "with an id": clientID} {
		t.Run(name, func(t *testing.T) {
			env := envOf(t, interrupted(t), at(30))
			c := command.SwitchCycle{ID: id, To: cycleB}
			p := mustPlan(t, env, c)
			if got := typesOf(p); !slices.Equal(got, []event.Type{event.TypeCyclePaused, event.TypeCycleResumed, event.TypeBatchCommitted}) {
				t.Fatalf("events %v", got)
			}
			if p.BatchID == "" || (id != "" && p.BatchID != id) {
				t.Errorf("BatchID = %q, want %q or a fresh one", p.BatchID, id)
			}
			paused, resumed, commit := p.Events[0].Payload.(event.CyclePaused), p.Events[1].Payload.(event.CycleResumed), p.Events[2].Payload.(event.BatchCommitted)
			if paused != (event.CyclePaused{CycleID: cycleA, Batch: p.BatchID}) || resumed != (event.CycleResumed{CycleID: cycleB, Batch: p.BatchID}) || commit.Batch != p.BatchID {
				t.Errorf("payloads %+v %+v %+v", paused, resumed, commit)
			}
			seen := map[event.ID]bool{p.BatchID: true}
			var hash string
			if id != "" {
				hash = hashOf(t, c)
			}
			for _, e := range p.Events {
				if !e.EffectiveAt.Time().Equal(at(30)) || !e.At.Time().Equal(at(30)) {
					t.Errorf("event %s at %v effective %v, want both at 30", e.ID, e.At.Time(), e.EffectiveAt.Time())
				}
				if seen[e.ID] {
					t.Errorf("event id %s repeats the batch id or another event's", e.ID)
				}
				seen[e.ID] = true
				if e.ReqHash != hash {
					t.Errorf("event %s req_hash %q, want %q", e.ID, e.ReqHash, hash)
				}
			}
			if f, ok := p.Candidate.Running(); !ok || f.ID != cycleB {
				t.Errorf("candidate focus = %s, want %s", f.ID, cycleB)
			}
			if d := p.Candidate.Dimmed(); len(d) != 1 || d[0].ID != cycleA {
				t.Errorf("candidate dimmed = %v, want [%s]", d, cycleA)
			}
		})
	}
}

func TestSwitchRejections(t *testing.T) {
	t.Run("nothing running", func(t *testing.T) {
		b := interrupted(t)
		b.add(20, stopOf(cycleA))
		r := mustReject(t, envOf(t, b, at(30)), command.SwitchCycle{To: cycleB}, command.ReasonNoRunningCycle)
		if !strings.Contains(r.Message, "resume") {
			t.Errorf("Message %q does not point at resume", r.Message)
		}
	})
	t.Run("an unknown target", func(t *testing.T) {
		mustReject(t, envOf(t, interrupted(t), at(30)), command.SwitchCycle{To: cycleC}, command.ReasonUnknownCycle)
	})
	t.Run("no target", func(t *testing.T) {
		mustReject(t, envOf(t, interrupted(t), at(30)), command.SwitchCycle{}, command.ReasonInvalidRequest)
	})
	t.Run("a stopped target", func(t *testing.T) {
		b := interrupted(t)
		b.add(20, stopOf(cycleB))
		mustReject(t, envOf(t, b, at(30)), command.SwitchCycle{To: cycleB}, command.ReasonCycleStopped)
	})
	t.Run("an instant not after the focus's last start", func(t *testing.T) {
		mustReject(t, envOf(t, interrupted(t), at(30)), command.SwitchCycle{To: cycleB, EffectiveAt: ptr(at(10))}, command.ReasonEmptyRunningSegment)
	})
	t.Run("an instant inside a pause the target already ends later", func(t *testing.T) {
		b := interrupted(t)
		b.switchTo(20, cycleA, cycleB)
		b.switchTo(25, cycleB, cycleA)
		mustReject(t, envOf(t, b, at(30)), command.SwitchCycle{To: cycleB, EffectiveAt: ptr(at(15))}, command.ReasonCycleSegmentsOverlap)
	})
}

func TestSwitchToTheFocusIsANoOp(t *testing.T) {
	mustNoOp(t, envOf(t, interrupted(t), at(30)), command.SwitchCycle{To: cycleA})
	mustNoOp(t, envOf(t, interrupted(t), at(30)), command.SwitchCycle{ID: clientID, To: cycleA})
}

func TestSwitchBackAndForth(t *testing.T) {
	env := envOf(t, interrupted(t), at(30))
	p := mustPlan(t, env, command.SwitchCycle{To: cycleB})
	env = then(t, env, p, at(40))
	p = mustPlan(t, env, command.SwitchCycle{To: cycleA})
	m := p.Candidate
	if f, ok := m.Running(); !ok || f.ID != cycleA {
		t.Fatalf("focus = %s, want %s", f.ID, cycleA)
	}
	a, _ := m.Cycle(cycleA)
	b, _ := m.Cycle(cycleB)
	if got := a.Elapsed(at(50)); got != 30*time.Minute {
		t.Errorf("A elapsed %v, want 30m (10 to 30 and 40 to 50)", got)
	}
	if got := b.Elapsed(at(50)); got != 20*time.Minute {
		t.Errorf("B elapsed %v, want 20m (0 to 10 and 30 to 40)", got)
	}

	t.Run("back at the same instant is an empty segment", func(t *testing.T) {
		env := envOf(t, interrupted(t), at(30))
		p := mustPlan(t, env, command.SwitchCycle{To: cycleB})
		mustReject(t, then(t, env, p, at(30)), command.SwitchCycle{To: cycleA}, command.ReasonEmptyRunningSegment)
	})
}

func TestSwitchBackdatedAfterTheFocusWasStopped(t *testing.T) {
	// B starts at 09:00 and A interrupts it at 10:00; A is stopped at 10:30.
	b := bootstrapped(t)
	b.add(60, startOf(cycleB, review))
	b.add(120, interruptOf(cycleA, cycleB, deepWork))
	b.add(150, stopOf(cycleA))
	p := mustPlan(t, envOf(t, b, at(160)), command.SwitchCycle{To: cycleB, EffectiveAt: ptr(at(135))})
	if got := typesOf(p); len(got) != 3 {
		t.Fatalf("events %v", got)
	}
	if got := p.Events[0].Payload.(event.CyclePaused).CycleID; got != cycleA {
		t.Errorf("paused %s, want %s, the cycle running at 10:15", got, cycleA)
	}
	if got := status(t, p.Candidate, cycleB, at(160)); got != projection.Running {
		t.Errorf("B = %s, want running", got)
	}
}

func TestSwitchToACycleWithALaterAnnotationIsValid(t *testing.T) {
	b := interrupted(t)
	b.add(20, event.CycleAnnotated{CycleID: cycleB, Note: "later note"})
	p := mustPlan(t, envOf(t, b, at(30)), command.SwitchCycle{To: cycleB, EffectiveAt: ptr(at(15))})
	if got := status(t, p.Candidate, cycleB, at(30)); got != projection.Running {
		t.Errorf("B = %s, want running", got)
	}
}

func TestStopWhileDimmed(t *testing.T) {
	p := mustPlan(t, envOf(t, interrupted(t), at(30)), command.StopCycle{CycleID: cycleB})
	if got := targetOf(t, p); got != cycleB {
		t.Errorf("stopped %s, want %s", got, cycleB)
	}
	if f, ok := p.Candidate.Running(); !ok || f.ID != cycleA {
		t.Errorf("focus = %s, want %s untouched", f.ID, cycleA)
	}
	if d := p.Candidate.Dimmed(); len(d) != 0 {
		t.Errorf("dimmed = %v, want none", d)
	}
}

func TestPauseWithClockBehindLastEventIsClockBehindLog(t *testing.T) {
	// A starts at 08:00 and is boosted at 08:30; the clock then reads 08:20.
	b := bootstrapped(t)
	b.add(0, startOf(cycleA, deepWork))
	boosted := b.add(30, event.CycleBoosted{CycleID: cycleA, Minutes: 5})
	env := envOf(t, b, at(20))

	for _, c := range []command.Command{command.PauseCycle{}, command.PauseCycle{CycleID: cycleA}} {
		r := mustReject(t, env, c, command.ReasonClockBehindLog)
		for _, want := range []string{
			"2026-10-07T12:20:00.000Z (08:20 America/New_York)", "2026-10-07T12:30:00.000Z (08:30 America/New_York)",
			"effective_at", string(boosted),
		} {
			if !strings.Contains(r.Message, want) {
				t.Errorf("Message %q does not mention %q", r.Message, want)
			}
		}
		if r.Entity != string(cycleA) || !sameIDs(r.Events, boosted) || len(r.Instants) != 2 {
			t.Errorf("entity %q, events %v, instants %v", r.Entity, r.Events, r.Instants)
		}
	}

	t.Run("a supplied effective_at is judged by replay alone", func(t *testing.T) {
		mustPlan(t, env, command.PauseCycle{CycleID: cycleA, EffectiveAt: ptr(at(20))})
	})
	t.Run("a no-op is decided before the clock", func(t *testing.T) {
		b := bootstrapped(t)
		b.add(0, startOf(cycleA, deepWork))
		b.add(10, pauseOf(cycleA))
		b.add(30, event.CycleBoosted{CycleID: cycleA, Minutes: 5})
		mustNoOp(t, envOf(t, b, at(20)), command.PauseCycle{CycleID: cycleA})
	})
	t.Run("every cycle verb with no effective_at", func(t *testing.T) {
		for _, c := range []command.Command{
			command.BoostCycle{CycleID: cycleA, Minutes: 5},
			command.StopCycle{CycleID: cycleA},
			command.AnnotateCycle{CycleID: cycleA, Note: "x"},
			command.StartCycle{Type: review},
		} {
			mustReject(t, env, c, command.ReasonClockBehindLog)
		}
	})
	t.Run("a switch behind either cycle", func(t *testing.T) {
		b := interrupted(t)
		b.add(40, event.CycleAnnotated{CycleID: cycleB, Note: "later"})
		mustReject(t, envOf(t, b, at(30)), command.SwitchCycle{To: cycleB}, command.ReasonClockBehindLog)
	})
}
