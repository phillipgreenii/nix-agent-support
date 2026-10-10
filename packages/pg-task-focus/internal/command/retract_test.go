package command_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/testutil"
)

// rolledOver is the environment, read a day after t0 plus ten minutes, after
// Build has begun the periods an hour before t0 and rolled the day over to
// day2 a day after t0, with the rollover's plan.
func rolledOver(t *testing.T, cfg *config.Config) (command.Env, command.Plan) {
	t.Helper()
	env, _ := begun(t, cfg)
	env, roll := apply(t, env, command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day2)}}, at(24*60))
	env.Now = at(24*60 + 10)
	return env, roll
}

var (
	newPostPlan = event.NewTaskID(due.Daily, day2, "post-plan")
	newPlanDay  = event.NewTaskID(due.Daily, day2, "plan-day")
)

func TestRetractAnyEventExceptCommitted(t *testing.T) {
	tests := []struct {
		name  string
		build func(t *testing.T) (*logb, event.ID)
	}{
		{"a task.completed", func(t *testing.T) (*logb, event.ID) {
			b := bootstrapped(t)
			return b, b.add(10, event.TaskCompleted{TaskID: postPlan})
		}},
		{"a task.skipped", func(t *testing.T) (*logb, event.ID) {
			b := bootstrapped(t)
			return b, b.add(10, event.TaskSkipped{TaskID: postPlan, Reason: "late"})
		}},
		{"a cycle.started with no later event", func(t *testing.T) (*logb, event.ID) {
			b := bootstrapped(t)
			return b, b.add(0, startOf(cycleA, deepWork))
		}},
		{"a cycle.boosted", func(t *testing.T) (*logb, event.ID) {
			b := runningFrom0(t)
			return b, b.add(5, event.CycleBoosted{CycleID: cycleA, Minutes: 5})
		}},
		{"a cycle.annotated", func(t *testing.T) (*logb, event.ID) {
			b := runningFrom0(t)
			return b, b.add(5, event.CycleAnnotated{CycleID: cycleA, Note: "notes"})
		}},
		{"a cycle.paused", func(t *testing.T) (*logb, event.ID) {
			b := runningFrom0(t)
			return b, b.add(5, pauseOf(cycleA))
		}},
		{"a cycle.resumed", func(t *testing.T) (*logb, event.ID) {
			b := runningFrom0(t)
			b.add(5, pauseOf(cycleA))
			id := b.add(10, resumeOf(cycleA))
			b.add(20, stopOf(cycleA))
			return b, id
		}},
		{"a cycle.stopped", func(t *testing.T) (*logb, event.ID) {
			b, stop := stoppedAt90(t)
			return b, stop
		}},
		{"an event.corrected", func(t *testing.T) (*logb, event.ID) {
			b := runningFrom0(t)
			note := b.add(5, event.CycleAnnotated{CycleID: cycleA, Note: "notes"})
			return b, b.add(6, event.EventCorrected{Target: note, Fields: fieldsOf(t, "note", "fixed")})
		}},
		{"an event.retracted", func(t *testing.T) (*logb, event.ID) {
			b := bootstrapped(t)
			done := b.add(10, event.TaskCompleted{TaskID: postPlan})
			return b, b.add(11, event.EventRetracted{Target: done})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, target := tc.build(t)
			p := mustPlan(t, envOf(t, b, at(120)), command.Retract{Target: target, Reason: "a mistake"})
			r, ok := only(t, p).Payload.(event.EventRetracted)
			if !ok || r.Target != target || r.TargetBatch != "" || r.Reason != "a mistake" {
				t.Errorf("payload %+v, want a retraction of %s", only(t, p).Payload, target)
			}
			if v, _ := p.Candidate.Event(target); !v.Retracted {
				t.Errorf("event %s is not retracted in the candidate", target)
			}
		})
	}
	t.Run("a batch.committed", func(t *testing.T) {
		b := bootstrapped(t)
		commit := committedOf(t, b, bootstrapBatch(b))
		r := mustReject(t, envOf(t, b, at(120)), command.Retract{Target: commit}, command.ReasonInvalidCorrection)
		if r.Reason.Status() != 422 || !sameIDs(r.Events, commit) || !strings.Contains(r.Message, string(bootstrapBatch(b))) {
			t.Errorf("status %d, Events %v, message %q: want the stored commit and its batch", r.Reason.Status(), r.Events, r.Message)
		}
	})
}

func TestRetractCorrectionRestoresOriginal(t *testing.T) {
	b := bootstrapped(t)
	skip := b.add(10, event.TaskSkipped{TaskID: postPlan, Reason: "late"})
	fix := b.add(11, event.EventCorrected{Target: skip, Fields: fieldsOf(t, "reason", "away")})
	env := envOf(t, b, at(20))
	if task, _ := env.Model.Task(postPlan); task.Reason != "away" {
		t.Fatalf("the correction does not apply: %+v", task)
	}
	p := mustPlan(t, env, command.Retract{Target: fix})
	if task, _ := p.Candidate.Task(postPlan); task.Reason != "late" || task.Status != projection.Skipped {
		t.Errorf("task = %+v, want the original reason late", task)
	}
}

func TestRetractRetractionRestoresRetractedEvent(t *testing.T) {
	b := bootstrapped(t)
	done := b.add(10, event.TaskCompleted{TaskID: postPlan})
	undo := b.add(11, event.EventRetracted{Target: done})
	env := envOf(t, b, at(20))
	if task, _ := env.Model.Task(postPlan); task.Status != projection.Open {
		t.Fatalf("task = %+v, want open after the retraction", task)
	}
	p := mustPlan(t, env, command.Retract{Target: undo})
	if task, _ := p.Candidate.Task(postPlan); task.Status != projection.Completed {
		t.Errorf("task = %+v, want completed again", task)
	}
}

func TestRetractLoneMaterializedRejected(t *testing.T) {
	b := bootstrapped(t)
	env := envOf(t, b, at(20))
	target := memberOf(t, env.Model, bootstrapBatch(b), event.TypeTaskMaterialized)
	r := mustReject(t, env, command.Retract{Target: target}, command.ReasonInvalidCorrection)
	if !strings.Contains(r.Message, "task.materialized") || !strings.Contains(r.Message, "batch") || !sameIDs(r.Events, target) {
		t.Errorf("message %q, Events %v", r.Message, r.Events)
	}
}

func TestRetractLonePeriodChangedRejected(t *testing.T) {
	b := bootstrapped(t)
	env := envOf(t, b, at(20))
	target := memberOf(t, env.Model, bootstrapBatch(b), event.TypePeriodChanged)
	r := mustReject(t, env, command.Retract{Target: target}, command.ReasonInvalidCorrection)
	if !strings.Contains(r.Message, "period.changed") || r.Entity != string(projection.Day) {
		t.Errorf("message %q, Entity %q", r.Message, r.Entity)
	}
}

// switchedBack is a log in which A runs from t0, B interrupts it at 10 and a
// switch at 20 makes A the focus again, with the switch's batch id.
func switchedBack(t *testing.T) (*logb, event.ID) {
	t.Helper()
	b := runningFrom0(t)
	b.add(10, interruptOf(cycleB, cycleA, review))
	return b, b.switchTo(20, cycleB, cycleA)
}

func TestRetractBatchMemberAloneRejected(t *testing.T) {
	b, batch := switchedBack(t)
	env := envOf(t, b, at(30))
	member := memberOf(t, env.Model, batch, event.TypeCyclePaused)
	r := mustReject(t, env, command.Retract{Target: member}, command.ReasonInvalidCorrection)
	if !strings.Contains(r.Message, string(batch)) || r.Entity != string(cycleB) {
		t.Errorf("message %q, Entity %q: want the batch %s and cycle %s", r.Message, r.Entity, batch, cycleB)
	}
}

func TestRetractBatchMemberAloneIsInvalidCorrectionEvenWhenItsBatchIsAlreadyRetracted(t *testing.T) {
	b, batch := switchedBack(t)
	b.add(25, event.EventRetracted{TargetBatch: batch})
	env := envOf(t, b, at(30))
	member := memberOf(t, env.Model, batch, event.TypeCyclePaused)
	if v, _ := env.Model.Event(member); !v.Retracted {
		t.Fatal("the member is not retracted with its batch")
	}
	mustReject(t, env, command.Retract{Target: member}, command.ReasonInvalidCorrection)
}

func TestRetractTaskWhosePeriodWouldLoseItsChangeRejected(t *testing.T) {
	// The on-call profile lists one more daily task; changing to it on day2
	// materializes that task in its own batch, so retracting the rollover
	// would leave it with no live period.changed of day2.
	cfg := testutil.LoadConfig(t, func(c map[string]any) {
		c["tasks"].(map[string]any)["page-review"] = map[string]any{
			"title": "Review the pages", "cadence": "daily", "due": map[string]any{"at": "10:00", "tz": newYork},
		}
		oncall := c["profiles"].(map[string]any)["on-call"].(map[string]any)
		oncall["daily"] = append(oncall["daily"].([]any), "page-review")
	})
	env, roll := rolledOver(t, cfg)
	env, _ = apply(t, env, command.ChangeProfile{Profile: "on-call"}, at(24*60+20))
	extra := event.NewTaskID(due.Daily, day2, "page-review")
	if _, ok := env.Model.Task(extra); !ok {
		t.Fatalf("the profile change did not materialize %s", extra)
	}
	env.Now = at(24*60 + 30)
	if deps := env.Model.Dependents(roll.BatchID); len(deps) != 0 {
		t.Fatalf("Dependents = %v: the task of the profile batch is not the rollover's", deps)
	}
	r := mustReject(t, env, command.Retract{TargetBatch: roll.BatchID}, command.ReasonTaskWithoutPeriod)
	if r.Entity != string(extra) {
		t.Errorf("Entity %q, want %s", r.Entity, extra)
	}
}

func TestRetractRequiresExactlyOneOfTargetAndTargetBatch(t *testing.T) {
	b, batch := switchedBack(t)
	env := envOf(t, b, at(30))
	start := b.events[0].ID // any stored event: the request is judged first
	for name, c := range map[string]command.Retract{
		"neither":            {},
		"both":               {Target: start, TargetBatch: batch},
		"a reason not UTF-8": {Target: start, Reason: "a\xff"},
	} {
		t.Run(name, func(t *testing.T) {
			r := mustReject(t, env, c, command.ReasonInvalidRequest)
			if r.Reason.Status() != 400 {
				t.Errorf("status %d", r.Reason.Status())
			}
		})
	}
}

func TestRetractUnknownBatchIs404(t *testing.T) {
	b, _ := switchedBack(t)
	env := envOf(t, b, at(30))
	ghost := idOf('G', 1)
	r := mustReject(t, env, command.Retract{TargetBatch: ghost}, command.ReasonUnknownEvent)
	if r.Reason.Status() != 404 || r.Entity != "" || len(r.Events) != 0 {
		t.Errorf("status %d, Entity %q, Events %v", r.Reason.Status(), r.Entity, r.Events)
	}
	// An event id is not a batch, and a batch id is not an event.
	mustReject(t, env, command.Retract{TargetBatch: b.events[len(b.events)-1].ID}, command.ReasonUnknownEvent)
	r = mustReject(t, env, command.Retract{Target: bootstrapBatch(b)}, command.ReasonUnknownEvent)
	if !strings.Contains(r.Message, "target_batch") {
		t.Errorf("message %q does not point at target_batch for a batch id", r.Message)
	}
}

func TestBatchRetractionRemovesRolloverAndNewPeriods(t *testing.T) {
	env, roll := rolledOver(t, testutil.LoadConfig(t, nil))
	before, _ := begun(t, testutil.LoadConfig(t, nil))
	prior, _ := before.Model.Period(projection.Day)
	_, p := apply(t, env, command.Retract{ID: clientID, TargetBatch: roll.BatchID, Reason: "rolled the day by mistake"}, env.Now)
	m := writeGoldenIn(t, correctionsDir, "rollover-retracted.jsonl", p.Candidate.Log())
	for name, model := range map[string]*projection.Model{"candidate": p.Candidate, "replayed golden": m} {
		d, _ := model.Period(projection.Day)
		if !reflect.DeepEqual(d, prior) {
			t.Errorf("%s: day %+v, want the prior day %+v", name, d, prior)
		}
		for _, id := range []event.TaskID{postPlan, planDay} {
			if task, _ := model.Task(id); task.Status != projection.Open {
				t.Errorf("%s: task %s is %s, want open again", name, id, task.Status)
			}
		}
		if _, ok := model.Task(newPostPlan); ok {
			t.Errorf("%s: the new period's task %s remains", name, newPostPlan)
		}
		if w, _ := model.Period(projection.Week); w.Start != week1 {
			t.Errorf("%s: week %s, want %s untouched", name, w.Start, week1)
		}
	}
}

func TestBatchHasDependents(t *testing.T) {
	env, roll := rolledOver(t, testutil.LoadConfig(t, nil))
	env, done := apply(t, env, command.CompleteTask{TaskID: newPostPlan}, at(24*60+20))
	env, skip := apply(t, env, command.SkipTask{TaskID: newPlanDay, Reason: "not today"}, at(24*60+25))
	env.Now = at(24*60 + 30)
	r := mustReject(t, env, command.Retract{TargetBatch: roll.BatchID}, command.ReasonBatchHasDependents)
	want := []event.ID{only(t, done).ID, only(t, skip).ID}
	if !slices.Equal(r.Events, want) {
		t.Errorf("Events = %v, want exactly the dependents %v", r.Events, want)
	}
	for _, id := range want {
		if !strings.Contains(r.Message, string(id)) {
			t.Errorf("message %q does not name dependent %s", r.Message, id)
		}
	}
	if r.Reason.Status() != 409 || !slices.Equal(env.Model.Dependents(roll.BatchID), want) {
		t.Errorf("status %d, Model.Dependents %v", r.Reason.Status(), env.Model.Dependents(roll.BatchID))
	}
}

func TestLateCompletionOfMissedTaskIsNotADependent(t *testing.T) {
	env, roll := rolledOver(t, testutil.LoadConfig(t, nil))
	if task, _ := env.Model.Task(postPlan); task.Status != projection.Missed {
		t.Fatalf("task %s is %s, want missed", postPlan, task.Status)
	}
	env, late := apply(t, env, command.CompleteTask{TaskID: postPlan, EffectiveAt: testutil.Ptr(at(5 * 60))}, at(24*60+20))
	_, p := apply(t, env, command.Retract{TargetBatch: roll.BatchID}, at(24*60+30))
	task, _ := p.Candidate.Task(postPlan)
	if task.Status != projection.Completed || !task.ResolvedAt.Equal(at(5*60)) {
		t.Errorf("task = %+v, want the late completion kept", task)
	}
	if v, _ := p.Candidate.Event(only(t, late).ID); v.Retracted {
		t.Error("the late completion was retracted with the batch")
	}
	missed := memberOf(t, p.Candidate, roll.BatchID, event.TypeTaskMissed)
	if v, _ := p.Candidate.Event(missed); !v.Retracted {
		t.Errorf("the missed marker %s is still live", missed)
	}
}

func TestRetractCycleStartWithLaterEventsIsCycleHasDependents(t *testing.T) {
	b := bootstrapped(t)
	start := b.add(0, startOf(cycleA, deepWork))
	boost := b.add(5, event.CycleBoosted{CycleID: cycleA, Minutes: 5})
	interrupt := b.add(10, interruptOf(cycleB, cycleA, review))
	b.add(20, stopOf(cycleB))
	resume := b.add(21, resumeOf(cycleA))
	env := envOf(t, b, at(30))
	r := mustReject(t, env, command.Retract{Target: start}, command.ReasonCycleHasDependents)
	want := []event.ID{boost, interrupt, resume}
	if !slices.Equal(r.Events, want) || r.Entity != string(cycleA) || r.Reason.Status() != 409 {
		t.Errorf("Events %v, Entity %q, status %d; want %v and cycle %s", r.Events, r.Entity, r.Reason.Status(), want, cycleA)
	}
	for _, id := range want {
		if !strings.Contains(r.Message, string(id)) {
			t.Errorf("message %q does not name dependent %s", r.Message, id)
		}
	}
}

func TestRetractionOfAnAlreadyRetractedTargetIsANoOp(t *testing.T) {
	t.Run("an event", func(t *testing.T) {
		b := bootstrapped(t)
		done := b.add(10, event.TaskCompleted{TaskID: postPlan})
		env, first := apply(t, envOf(t, b, at(20)), command.Retract{Target: done}, at(20))
		undo := only(t, first).ID
		note := mustNoOp(t, env, command.Retract{Target: done})
		if !strings.Contains(note, string(undo)) {
			t.Errorf("note %q does not name the retraction %s", note, undo)
		}
		env, _ = apply(t, env, command.Retract{Target: undo}, at(21))
		_, again := apply(t, env, command.Retract{Target: done}, at(22))
		if v, _ := again.Candidate.Event(done); !v.Retracted || v.RetractedBy != only(t, again).ID {
			t.Errorf("view %+v: a new retraction applies once the first is undone", v)
		}
	})
	t.Run("a batch", func(t *testing.T) {
		env, roll := rolledOver(t, testutil.LoadConfig(t, nil))
		env, first := apply(t, env, command.Retract{TargetBatch: roll.BatchID}, env.Now)
		undo := only(t, first).ID
		note := mustNoOp(t, env, command.Retract{TargetBatch: roll.BatchID})
		if !strings.Contains(note, string(undo)) || !strings.Contains(note, string(roll.BatchID)) {
			t.Errorf("note %q does not name the batch %s and the retraction %s", note, roll.BatchID, undo)
		}
		env, _ = apply(t, env, command.Retract{Target: undo}, env.Now)
		_, again := apply(t, env, command.Retract{TargetBatch: roll.BatchID}, env.Now)
		if d, _ := again.Candidate.Period(projection.Day); d.Start != day1 {
			t.Errorf("day %s, want %s once the batch is retracted again", d.Start, day1)
		}
	})
}

func TestBatchRetractionIsOneEventWithTargetBatchAndNoBatch(t *testing.T) {
	env, roll := rolledOver(t, testutil.LoadConfig(t, nil))
	c := command.Retract{ID: clientID, TargetBatch: roll.BatchID}
	p := mustPlan(t, env, c)
	e := only(t, p)
	r, ok := e.Payload.(event.EventRetracted)
	if !ok || r.TargetBatch != roll.BatchID || r.Target != "" || e.Payload.BatchID() != "" || p.BatchID != "" {
		t.Errorf("event %+v, plan batch %q: want one retraction naming target_batch and no batch", e, p.BatchID)
	}
	if e.ID != clientID || e.ReqHash != hashOf(t, c) || !e.EffectiveAt.Time().Equal(env.Now) {
		t.Errorf("event %+v: want the client id, its hash, effective now", e.Envelope)
	}
	line, err := event.Encode(e)
	if err != nil || strings.Contains(string(line), `"batch"`) {
		t.Errorf("encoded %s (%v): the retraction carries a batch", line, err)
	}
}

func TestRetractionOfABatchRetractionRestoresTheBatch(t *testing.T) {
	env, roll := rolledOver(t, testutil.LoadConfig(t, nil))
	env, first := apply(t, env, command.Retract{TargetBatch: roll.BatchID}, env.Now)
	rolled := roll.Candidate.Domain()
	_, p := apply(t, env, command.Retract{Target: only(t, first).ID, Reason: "the rollover was right"}, at(24*60+11))
	m := writeGoldenIn(t, correctionsDir, "rollover-restored.jsonl", p.Candidate.Log())
	for name, model := range map[string]*projection.Model{"candidate": p.Candidate, "replayed golden": m} {
		if got := model.Domain(); !reflect.DeepEqual(got, rolled) {
			t.Errorf("%s: domain %+v, want the rolled-over state %+v", name, got, rolled)
		}
	}
}

func TestRetractSwitchBatchRestoresFocus(t *testing.T) {
	b, batch := switchedBack(t)
	env := envOf(t, b, at(30))
	if focus, _ := env.Model.Running(); focus.ID != cycleA {
		t.Fatalf("focus %s, want %s after the switch", focus.ID, cycleA)
	}
	p := mustPlan(t, env, command.Retract{TargetBatch: batch})
	if focus, ok := p.Candidate.Running(); !ok || focus.ID != cycleB {
		t.Errorf("focus %s, want %s again once the switch is undone", focus.ID, cycleB)
	}
	if got := status(t, p.Candidate, cycleA, at(30)); got != projection.Paused {
		t.Errorf("cycle A is %s, want paused by the interrupt", got)
	}

	t.Run("a later event that needs the switch makes the undo impossible", func(t *testing.T) {
		b, batch := switchedBack(t)
		b.add(25, interruptOf(cycleC, cycleA, review)) // interrupts A, which only the switch set running
		mustReject(t, envOf(t, b, at(30)), command.Retract{TargetBatch: batch}, command.ReasonInterruptedCycleNotRunning)
	})
}

// TestTaskMaterializedAndPeriodChangedAreAlwaysInABatch pins why a
// retraction of either names its batch: the codec refuses one with no batch,
// so no stored task.materialized or period.changed is outside a batch, and
// the refusal of retracting one alone always points at target_batch.
func TestTaskMaterializedAndPeriodChangedAreAlwaysInABatch(t *testing.T) {
	for name, p := range map[string]event.Payload{
		"a task.materialized": taskOf("lonely")(""),
		"a period.changed":    event.PeriodChanged{Kind: "week", Start: week1, End: datePtr(week1.AddDays(6)), TZ: newYork},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := event.Encode(event.Event{
				Envelope: event.Envelope{V: event.SchemaVersion, ID: idOf('L', 99), At: event.At(t0), EffectiveAt: event.At(t0), Type: p.EventType()},
				Payload:  p,
			})
			if err == nil {
				t.Fatalf("a %s with no batch encodes; the retraction rule needs a message for it", p.EventType())
			}
		})
	}
	b := bootstrapped(t)
	env := envOf(t, b, at(20))
	batch := bootstrapBatch(b)
	for _, typ := range []event.Type{event.TypeTaskMaterialized, event.TypePeriodChanged} {
		target := memberOf(t, env.Model, batch, typ)
		r := mustReject(t, env, command.Retract{Target: target}, command.ReasonInvalidCorrection)
		if !strings.Contains(r.Message, "target_batch") || !strings.Contains(r.Message, string(batch)) {
			t.Errorf("%s: message %q does not point at target_batch %s", typ, r.Message, batch)
		}
	}
}

// TestCycleHasDependentsNamesTheStartAsCorrected retracts a cycle start whose
// effective_at a correction moved: the refusal gives the live instant.
func TestCycleHasDependentsNamesTheStartAsCorrected(t *testing.T) {
	b := bootstrapped(t)
	start := b.add(0, startOf(cycleA, deepWork))
	b.add(1, event.EventCorrected{Target: start, Fields: map[string]json.RawMessage{"effective_at": rawOf(t, event.At(at(-5)))}})
	b.add(5, event.CycleBoosted{CycleID: cycleA, Minutes: 5})
	env := envOf(t, b, at(30))
	r := mustReject(t, env, command.Retract{Target: start}, command.ReasonCycleHasDependents)
	if !slices.ContainsFunc(r.Instants, at(-5).Equal) || slices.ContainsFunc(r.Instants, at(0).Equal) {
		t.Errorf("Instants %v, want the corrected start %v and not the appended %v", r.Instants, at(-5), at(0))
	}
	if !slices.ContainsFunc(r.Instants, at(30).Equal) {
		t.Errorf("Instants %v lack the new event's effective_at", r.Instants)
	}
}

// TestRetractStepOneRefusalsCarryTheNewEventInstant checks that a malformed
// retraction gives the new event's effective_at, the recording instant.
func TestRetractStepOneRefusalsCarryTheNewEventInstant(t *testing.T) {
	b, batch := switchedBack(t)
	env := envOf(t, b, at(30))
	for name, c := range map[string]command.Retract{
		"neither":            {},
		"both":               {Target: b.events[0].ID, TargetBatch: batch},
		"a reason not UTF-8": {Target: b.events[0].ID, Reason: "a\xff"},
	} {
		t.Run(name, func(t *testing.T) {
			r := mustReject(t, env, c, command.ReasonInvalidRequest)
			if !slices.Equal(r.Instants, []time.Time{at(30)}) {
				t.Errorf("Instants %v, want the new event's effective_at %v", r.Instants, at(30))
			}
		})
	}
}
