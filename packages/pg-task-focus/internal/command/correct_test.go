package command_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/testgen"
)

// fataler is what the JSON helpers need of a test, a property's included.
type fataler interface {
	Helper()
	Fatalf(format string, args ...any)
}

// rawOf is v as a JSON value.
func rawOf(t fataler, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal %v: %v", v, err)
	}
	return raw
}

// fieldsOf is a correction's fields from alternating keys and values.
func fieldsOf(t fataler, kv ...any) map[string]json.RawMessage {
	t.Helper()
	out := map[string]json.RawMessage{}
	for i := 0; i < len(kv); i += 2 {
		out[kv[i].(string)] = rawOf(t, kv[i+1])
	}
	return out
}

// editable is a log with something of each kind to correct: cycle A, a
// review, started at t0, boosted at 5, annotated at 6 and paused at 20, and
// post-plan skipped at 10.
type editable struct {
	b                               *logb
	start, boost, note, skip, pause event.ID
}

func editableLog(t *testing.T) editable {
	t.Helper()
	b := bootstrapped(t)
	e := editable{b: b}
	e.start = b.add(0, startOf(cycleA, review))
	e.boost = b.add(5, event.CycleBoosted{CycleID: cycleA, Minutes: 5})
	e.note = b.add(6, event.CycleAnnotated{CycleID: cycleA, Note: "first"})
	e.skip = b.add(10, event.TaskSkipped{TaskID: postPlan, Reason: "late"})
	e.pause = b.add(20, pauseOf(cycleA))
	return e
}

// bootstrapBatch is the id of the first batch of a log built by bootstrapped.
func bootstrapBatch(b *logb) event.ID { return b.events[0].Payload.BatchID() }

// committedOf is the id of the batch.committed of batch in b.
func committedOf(t *testing.T, b *logb, batch event.ID) event.ID {
	t.Helper()
	for _, e := range b.events {
		if c, ok := e.Payload.(event.BatchCommitted); ok && c.Batch == batch {
			return e.ID
		}
	}
	t.Fatalf("batch %s has no batch.committed", batch)
	return ""
}

// memberOf is the id of the first event of type typ in batch.
func memberOf(t *testing.T, m *projection.Model, batch event.ID, typ event.Type) event.ID {
	t.Helper()
	for _, id := range m.BatchEvents(batch) {
		if v, ok := m.Event(id); ok && v.Original.Type == typ {
			return id
		}
	}
	t.Fatalf("batch %s has no %s", batch, typ)
	return ""
}

// eventOfType is the id of the first event of type typ a plan appends.
func eventOfType(t *testing.T, p command.Plan, typ event.Type) event.ID {
	t.Helper()
	for _, e := range p.Events {
		if e.Type == typ {
			return e.ID
		}
	}
	t.Fatalf("the plan appends no %s", typ)
	return ""
}

func TestCorrectionRejectsIdentityFields(t *testing.T) {
	e := editableLog(t)
	keys := []string{
		"cycle_id", "task_id", "target", "target_batch", "batch", "interrupts",
		"definition", "kind", "start", "cadence", "period", "profile",
	}
	check := func(t *testing.T, target event.ID, entity string, fields map[string]json.RawMessage, key string) {
		t.Helper()
		r := mustReject(t, envOf(t, e.b, at(30)), command.Correct{Target: target, Fields: fields}, command.ReasonInvalidCorrection)
		if r.Reason.Status() != 422 {
			t.Errorf("status %d, want 422", r.Reason.Status())
		}
		if !strings.Contains(r.Message, key) || !strings.Contains(r.Message, string(target)) {
			t.Errorf("message %q does not name the field %s and the stored target %s", r.Message, key, target)
		}
		if r.Entity != entity || !sameIDs(r.Events, target) {
			t.Errorf("Entity %q, Events %v; want %q and the target %s", r.Entity, r.Events, entity, target)
		}
		if !slices.ContainsFunc(r.Instants, at(30).Equal) {
			t.Errorf("Instants %v lack the new event's effective_at", r.Instants)
		}
	}
	for _, k := range keys {
		t.Run(k, func(t *testing.T) {
			check(t, e.skip, string(postPlan), fieldsOf(t, k, "01J9ZZZZZZZZZZZZZZZZZZZZZX"), k)
		})
	}
	t.Run("the envelope type", func(t *testing.T) {
		check(t, e.skip, string(postPlan), fieldsOf(t, "type", "task.completed"), "type")
	})
	for _, k := range []string{"id", "at", "req_hash", "v"} {
		t.Run("the envelope "+k, func(t *testing.T) {
			check(t, e.skip, string(postPlan), fieldsOf(t, k, "x"), k)
		})
	}
	t.Run("a cycle's identity beside the type it may change", func(t *testing.T) {
		check(t, e.start, string(cycleA), fieldsOf(t, "type", deepWork, "cycle_id", "x"), "cycle_id")
	})
}

func TestCorrectionRejectsForbiddenTargets(t *testing.T) {
	e := editableLog(t)
	b := e.b
	corrected := b.add(21, event.EventCorrected{Target: e.note, Fields: fieldsOf(t, "note", "fixed")})
	retracted := b.add(22, event.EventRetracted{Target: e.boost})
	committed := committedOf(t, b, bootstrapBatch(b))
	for name, target := range map[string]event.ID{
		"an event.corrected": corrected, "an event.retracted": retracted, "a batch.committed": committed,
	} {
		t.Run(name, func(t *testing.T) {
			r := mustReject(t, envOf(t, b, at(30)), command.Correct{Target: target, Fields: fieldsOf(t, "reason", "x")}, command.ReasonInvalidCorrection)
			if !strings.Contains(r.Message, string(target)) || !sameIDs(r.Events, target) {
				t.Errorf("message %q, Events %v: want the stored target %s", r.Message, r.Events, target)
			}
		})
	}
}

func TestCorrectionValidatedAgainstSchema(t *testing.T) {
	e := editableLog(t)
	huge := strings.Repeat("n", 300*1024)
	tests := []struct {
		name   string
		target event.ID
		fields map[string]json.RawMessage
		path   string
	}{
		{"a negative minutes", e.boost, fieldsOf(t, "minutes", -5), "data.minutes"},
		{"a minutes of zero", e.boost, fieldsOf(t, "minutes", 0), "data.minutes"},
		{"a minutes over a year", e.boost, fieldsOf(t, "minutes", 525601), "data.minutes"},
		{"minutes that are not a whole number", e.boost, fieldsOf(t, "minutes", "ten"), "data.minutes"},
		{"a negative planned_minutes", e.start, fieldsOf(t, "planned_minutes", -1), "data.planned_minutes"},
		{"a blank skip reason", e.skip, fieldsOf(t, "reason", "  "), "data.reason"},
		{"an empty skip reason", e.skip, fieldsOf(t, "reason", ""), "data.reason"},
		{"a note over the size limit", e.note, fieldsOf(t, "note", huge), "data.note"},
		{"a note that is not valid UTF-8", e.note, map[string]json.RawMessage{"note": json.RawMessage("\"a\xff\"")}, "data.note"},
		{"a value that is not JSON", e.note, map[string]json.RawMessage{"note": json.RawMessage("nope")}, "data.note"},
		{"a key the event does not have", e.boost, fieldsOf(t, "note", "x"), "data.note"},
		{"an effective_at that is not an instant", e.skip, fieldsOf(t, "effective_at", "yesterday"), "effective_at"},
		{"no fields at all", e.skip, nil, "fields"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := mustReject(t, envOf(t, e.b, at(30)), command.Correct{Target: tc.target, Fields: tc.fields}, command.ReasonInvalidRequest)
			if r.Reason.Status() != 400 {
				t.Errorf("status %d, want 400", r.Reason.Status())
			}
			if !strings.Contains(r.Message, tc.path) {
				t.Errorf("message %q does not name the field path %s", r.Message, tc.path)
			}
		})
	}
	t.Run("a schema finding names the stored target in the details", func(t *testing.T) {
		r := mustReject(t, envOf(t, e.b, at(30)), command.Correct{Target: e.boost, Fields: fieldsOf(t, "note", "x")}, command.ReasonInvalidRequest)
		if r.Entity != string(cycleA) || !sameIDs(r.Events, e.boost) {
			t.Errorf("Entity %q, Events %v; want %s and %s", r.Entity, r.Events, cycleA, e.boost)
		}
	})
	t.Run("a correction with no target", func(t *testing.T) {
		mustReject(t, envOf(t, e.b, at(30)), command.Correct{Fields: fieldsOf(t, "note", "x")}, command.ReasonInvalidRequest)
	})
	t.Run("a correction reason that is not valid UTF-8", func(t *testing.T) {
		mustReject(t, envOf(t, e.b, at(30)), command.Correct{Target: e.note, Fields: fieldsOf(t, "note", "x"), Reason: "a\xff"}, command.ReasonInvalidRequest)
	})
}

func TestCorrectionCanChangeEffectiveAtAndReason(t *testing.T) {
	e := editableLog(t)
	env := envOf(t, e.b, at(30))
	c := command.Correct{ID: clientID, Target: e.skip, Fields: fieldsOf(t, "effective_at", event.At(at(8)), "reason", "  away "), Reason: "typo"}
	p := mustPlan(t, env, c)
	ev := only(t, p)
	got, ok := ev.Payload.(event.EventCorrected)
	if !ok || ev.ID != clientID || ev.ReqHash != hashOf(t, c) || !ev.EffectiveAt.Time().Equal(at(30)) {
		t.Fatalf("event %+v, want an event.corrected with the client id and hash, effective now", ev)
	}
	if got.Target != e.skip || got.Reason != "typo" || string(got.Fields["reason"]) != `"away"` || string(got.Fields["effective_at"]) != `"2026-10-07T12:08:00.000Z"` {
		t.Errorf("payload = %+v (fields %s, %s), want the target, the trimmed reason and the instant", got, got.Fields["reason"], got.Fields["effective_at"])
	}
	task, _ := p.Candidate.Task(postPlan)
	if task.Status != projection.Skipped || task.Reason != "away" || task.ResolvedAt == nil || !task.ResolvedAt.Equal(at(8)) {
		t.Errorf("task = %+v, want skipped at %v for away", task, at(8))
	}
	v, _ := p.Candidate.Event(e.skip)
	if !sameIDs(v.CorrectedBy, clientID) || v.Original.Payload.(event.TaskSkipped).Reason != "late" {
		t.Errorf("view = %+v: the original is kept and the correction listed", v)
	}
}

func TestCorrectionOfCycleTypeFillsTitleFromConfig(t *testing.T) {
	e := editableLog(t)
	env := envOf(t, e.b, at(30))
	p := mustPlan(t, env, command.Correct{Target: e.start, Fields: fieldsOf(t, "type", deepWork)})
	fields := only(t, p).Payload.(event.EventCorrected).Fields
	if len(fields) != 2 || string(fields["type"]) != `"deep-work"` || string(fields["title"]) != `"`+deepWorkTitle+`"` {
		t.Errorf("fields = %v, want the type and the configured title only", fields)
	}
	c, _ := p.Candidate.Cycle(cycleA)
	if c.Type != deepWork || c.Title != deepWorkTitle || c.PlannedMinutes != 25 {
		t.Errorf("cycle = %+v, want deep-work with the configured title and the planned minutes untouched", c)
	}

	t.Run("an unknown type is unknown_cycle_type", func(t *testing.T) {
		r := mustReject(t, env, command.Correct{Target: e.start, Fields: fieldsOf(t, "type", "nothing")}, command.ReasonUnknownCycleType)
		if r.Reason.Status() != 400 {
			t.Errorf("status %d, want 400", r.Reason.Status())
		}
	})
	t.Run("a client title is invalid_request", func(t *testing.T) {
		mustReject(t, env, command.Correct{Target: e.start, Fields: fieldsOf(t, "title", "Mine")}, command.ReasonInvalidRequest)
		mustReject(t, env, command.Correct{Target: e.start, Fields: fieldsOf(t, "type", deepWork, "title", "Mine")}, command.ReasonInvalidRequest)
	})
	t.Run("a type that is not a string is invalid_request", func(t *testing.T) {
		mustReject(t, env, command.Correct{Target: e.start, Fields: fieldsOf(t, "type", 5)}, command.ReasonInvalidRequest)
	})
	t.Run("the golden log built by Build replays without the configuration", func(t *testing.T) {
		env, _ := begun(t, loadConfig(t, nil))
		env, sp := apply(t, env, command.StartCycle{Type: review}, at(0))
		start := only(t, sp)
		_, p := apply(t, env, command.Correct{ID: clientID, Target: start.ID, Fields: fieldsOf(t, "type", deepWork), Reason: "started the wrong type"}, at(10))
		m := writeGoldenIn(t, correctionsDir, "cycle-type-corrected.jsonl", p.Candidate.Log())
		c, ok := m.Cycle(start.Payload.(event.CycleStarted).CycleID)
		if !ok || c.Type != deepWork || c.Title != deepWorkTitle || c.PlannedMinutes != 25 {
			t.Errorf("replayed golden: cycle %+v", c)
		}
	})
}

func TestCorrectionMovingAPauseBeforeItsStartIsCycleEventBeforeStart(t *testing.T) {
	e := editableLog(t)
	r := mustReject(t, envOf(t, e.b, at(30)), command.Correct{Target: e.pause, Fields: fieldsOf(t, "effective_at", event.At(at(-5)))}, command.ReasonCycleEventBeforeStart)
	if r.Entity != string(cycleA) || !slices.Contains(r.Events, e.pause) || !slices.Contains(r.Events, e.start) {
		t.Errorf("Entity %q, Events %v; want cycle %s, the pause and the start", r.Entity, r.Events, cycleA)
	}
	if !strings.Contains(r.Message, "the new event") {
		t.Errorf("message %q does not name the correction as the new event", r.Message)
	}
}

func TestCorrectionWithAFutureEffectiveAtIsFutureEffectiveAt(t *testing.T) {
	e := editableLog(t)
	now := at(30)
	r := mustReject(t, envOf(t, e.b, now), command.Correct{Target: e.skip, Fields: fieldsOf(t, "effective_at", event.At(now.Add(2*time.Minute)))}, command.ReasonFutureEffectiveAt)
	if r.Reason.Status() != 422 || !slices.ContainsFunc(r.Instants, now.Add(2*time.Minute).Equal) {
		t.Errorf("status %d, Instants %v", r.Reason.Status(), r.Instants)
	}
	mustPlan(t, envOf(t, e.b, now), command.Correct{Target: e.skip, Fields: fieldsOf(t, "effective_at", event.At(now.Add(time.Minute)))})
}

func TestCorrectionMovingARolloverAnywhereIsJudgedByTimelineRulesOnly(t *testing.T) {
	env, _ := begun(t, loadConfig(t, nil))
	env, roll := apply(t, env, command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day2)}}, at(24*60))
	changed := eventOfType(t, roll, event.TypePeriodChanged)
	env.Now = at(24*60 + 10)

	// A minute after the first day change, almost a day back: no bound.
	p := mustPlan(t, env, command.Correct{Target: changed, Fields: fieldsOf(t, "effective_at", event.At(at(-59)))})
	if d, _ := p.Candidate.Period(projection.Day); d.Start != day2 {
		t.Errorf("day = %s, want %s", d.Start, day2)
	}
	if v, _ := p.Candidate.Event(changed); !v.Corrected.EffectiveAt.Time().Equal(at(-59)) {
		t.Errorf("corrected effective_at = %v", v.Corrected.EffectiveAt.Time())
	}
	// Four hundred days back sorts before the change it follows: the timeline
	// refuses it, not a bound.
	mustReject(t, env, command.Correct{Target: changed, Fields: fieldsOf(t, "effective_at", event.At(at(-400*24*60)))}, command.ReasonPeriodOutOfOrder)
	// The start is an identity field.
	mustReject(t, env, command.Correct{Target: changed, Fields: fieldsOf(t, "start", day1.AddDays(2))}, command.ReasonInvalidCorrection)
}

func TestUndoIsAlwaysAvailableForACorrection(t *testing.T) {
	e := editableLog(t)
	tests := []struct {
		name   string
		target event.ID
		fields map[string]json.RawMessage
	}{
		{"a reason and an effective_at", e.skip, fieldsOf(t, "reason", "away", "effective_at", event.At(at(8)))},
		{"a cycle type", e.start, fieldsOf(t, "type", deepWork)},
		{"a boost's minutes", e.boost, fieldsOf(t, "minutes", 15)},
		{"a note", e.note, fieldsOf(t, "note", "second")},
		{"a pause's time", e.pause, fieldsOf(t, "effective_at", event.At(at(15)))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := envOf(t, e.b, at(30))
			before := env.Model.Domain()
			env, p := apply(t, env, command.Correct{Target: tc.target, Fields: tc.fields}, at(30))
			if reflect.DeepEqual(p.Candidate.Domain(), before) {
				t.Fatal("the correction changed nothing")
			}
			_, undo := apply(t, env, command.Retract{Target: only(t, p).ID}, at(31))
			if got := undo.Candidate.Domain(); !reflect.DeepEqual(got, before) {
				t.Errorf("after the undo the domain is %+v, want %+v", got, before)
			}
		})
	}
}

func TestCorrectionAppliedTwiceChangesNothing(t *testing.T) {
	cfg := loadConfig(t, nil)
	rapid.Check(t, func(rt *rapid.T) {
		log := testgen.Log(12).Draw(rt, "log")
		m, err := projection.Replay(log)
		if err != nil {
			rt.Fatalf("Replay of a generated log: %v", err)
		}
		now := log[len(log)-1].At.Time().Add(time.Hour)
		views := m.Events(projection.EventQuery{})
		v := views[rapid.IntRange(0, len(views)-1).Draw(rt, "target")]
		target := v.Original
		fields := map[string]json.RawMessage{}
		switch p := target.Payload.(type) {
		case event.CycleAnnotated:
			fields["note"] = rawOf(rt, rapid.SampledFrom([]string{"", "Fixed the note", "Paired on it"}).Draw(rt, "note"))
		case event.TaskSkipped:
			fields["reason"] = rawOf(rt, rapid.SampledFrom([]string{"Not needed", "Done elsewhere"}).Draw(rt, "reason"))
		case event.CycleBoosted:
			fields["minutes"] = rawOf(rt, rapid.IntRange(1, 90).Draw(rt, "minutes"))
		case event.CycleStarted:
			if rapid.Bool().Draw(rt, "type") {
				fields["type"] = rawOf(rt, rapid.SampledFrom([]string{deepWork, review, "notifications"}).Draw(rt, "cycle_type"))
			} else {
				fields["planned_minutes"] = rawOf(rt, rapid.IntRange(1, 90).Draw(rt, "planned_minutes"))
			}
		case event.TaskMaterialized:
			fields["title"] = rawOf(rt, "Renamed "+p.Definition)
		}
		if len(fields) == 0 || rapid.Bool().Draw(rt, "move") {
			shift := time.Duration(rapid.IntRange(-3_600_000, 3_600_000).Draw(rt, "shift_ms")) * time.Millisecond
			fields["effective_at"] = rawOf(rt, event.At(target.EffectiveAt.Time().Add(shift)))
		}
		c := command.Correct{Target: target.ID, Fields: fields}
		env := command.Env{Model: m, Config: cfg, Now: now, NewID: newIDs()}
		first, err := command.Build(env, c)
		if err != nil {
			return // a correction the timeline refuses has nothing to repeat
		}
		env.Model, env.Now = first.Candidate, now.Add(time.Minute)
		second, err := command.Build(env, c)
		if err != nil {
			rt.Fatalf("the same correction again was refused: %v", err)
		}
		if got, want := second.Candidate.Domain(), first.Candidate.Domain(); !reflect.DeepEqual(got, want) {
			rt.Fatalf("the correction applied twice changed the state:\n%+v\nwant\n%+v", got, want)
		}
	})
}
