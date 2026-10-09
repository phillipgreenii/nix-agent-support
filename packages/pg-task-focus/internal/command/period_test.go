package command_test

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden logs under testdata/logs/rollover")

const goldenDir = "../../testdata/logs/rollover"

var (
	// week1 is the Monday of day1's week; the example's sprint starts then too
	// and lasts fourteen days.
	week1   = civil.Date{Year: 2026, Month: time.October, Day: 5}
	sprint1 = week1
)

func datePtr(d civil.Date) *civil.Date { return &d }

func strPtr(s string) *string { return &s }

// dayTo is a change of the day to d in New York.
func dayTo(d civil.Date) command.PeriodChange {
	return command.PeriodChange{Kind: projection.Day, Start: d, TZ: newYork}
}

// weekFrom is a change of the week to the seven days from d.
func weekFrom(d civil.Date) command.PeriodChange {
	return command.PeriodChange{Kind: projection.Week, Start: d, End: datePtr(d.AddDays(6)), TZ: newYork, Label: "Week of " + d.String()}
}

// sprintFrom is a change of the sprint to the fourteen days from d.
func sprintFrom(d civil.Date) command.PeriodChange {
	return command.PeriodChange{Kind: projection.Sprint, Start: d, End: datePtr(d.AddDays(13)), TZ: newYork, Label: "Sprint 1"}
}

// emptyEnv is the environment of a request against an empty log.
func emptyEnv(t *testing.T, cfg *config.Config, now time.Time) command.Env {
	t.Helper()
	m, err := projection.Replay(nil)
	if err != nil {
		t.Fatalf("Replay(nil): %v", err)
	}
	return command.Env{Model: m, Config: cfg, Now: now, NewID: newIDs()}
}

// begun is the environment, read at t0, after Build has set the day, the week
// and the sprint on an empty log an hour before t0, with the plan that did it.
func begun(t *testing.T, cfg *config.Config) (command.Env, command.Plan) {
	t.Helper()
	env := emptyEnv(t, cfg, at(-60))
	p := mustPlan(t, env, command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day1), weekFrom(week1), sprintFrom(sprint1)}})
	return then(t, env, p, t0), p
}

// extend appends stored events, recorded and effective at when, to env's
// model through the real codec and candidate replay.
func extend(t *testing.T, env command.Env, when time.Time, payloads ...event.Payload) command.Env {
	t.Helper()
	base := env.Model.Log()
	var add []event.Event
	for i, p := range payloads {
		line, err := event.Encode(event.Event{
			Envelope: event.Envelope{V: event.SchemaVersion, ID: idOf('X', uint32(len(base)+i+1)), At: event.At(when), EffectiveAt: event.At(when), Type: p.EventType()},
			Payload:  p,
		})
		if err != nil {
			t.Fatalf("Encode %T: %v", p, err)
		}
		e, err := event.Decode(line)
		if err != nil {
			t.Fatalf("Decode %T: %v", p, err)
		}
		add = append(add, e)
	}
	m, err := projection.Candidate(base, add)
	if err != nil {
		t.Fatalf("Candidate: %v", err)
	}
	env.Model = m
	return env
}

// payloadsOf lists the payloads of type T in a plan, in order.
func payloadsOf[T event.Payload](p command.Plan) []T {
	var out []T
	for _, e := range p.Events {
		if v, ok := e.Payload.(T); ok {
			out = append(out, v)
		}
	}
	return out
}

// taskIDsOf lists the task ids of the payloads of type T in a plan.
func taskIDsOf[T event.Payload](p command.Plan) []event.TaskID {
	var out []event.TaskID
	for _, v := range payloadsOf[T](p) {
		out = append(out, reflect.ValueOf(v).FieldByName("TaskID").Interface().(event.TaskID))
	}
	return out
}

// checkBatch checks that every event of a plan shares effective_at eff and
// the batch, and that the plan ends with its batch.committed.
func checkBatch(t *testing.T, p command.Plan, eff time.Time) {
	t.Helper()
	if len(p.Events) == 0 || p.BatchID == "" {
		t.Fatalf("plan has %d events and batch %q", len(p.Events), p.BatchID)
	}
	last := p.Events[len(p.Events)-1]
	if c, ok := last.Payload.(event.BatchCommitted); !ok || c.Batch != p.BatchID {
		t.Errorf("the last event is %s, want the batch.committed of %s", last.Type, p.BatchID)
	}
	for _, e := range p.Events {
		if e.Payload.BatchID() != p.BatchID {
			t.Errorf("%s %s has batch %q, want %q", e.Type, e.ID, e.Payload.BatchID(), p.BatchID)
		}
		if !e.EffectiveAt.Time().Equal(eff) {
			t.Errorf("%s effective at %v, want %v", e.Type, e.EffectiveAt.Time(), eff)
		}
	}
}

// writeGolden compares the log with the golden file of that name, rewriting
// it under -update, then decodes the file and replays it.
func writeGolden(t *testing.T, name string, log []event.Event) *projection.Model {
	t.Helper()
	var want []byte
	for _, e := range log {
		line, err := event.Encode(e)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		want = append(want, append(line, '\n')...)
	}
	path := filepath.Join(goldenDir, name)
	if *updateGolden {
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden log: %v (run the test with -update to write it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from the log the test builds; run the test with -update", path)
	}
	var events []event.Event
	for i, line := range bytes.Split(bytes.TrimSuffix(got, []byte("\n")), []byte("\n")) {
		e, err := event.Decode(line)
		if err != nil {
			t.Fatalf("line %d does not decode: %v", i+1, err)
		}
		e.Line = i + 1
		events = append(events, e)
	}
	m, err := projection.Replay(events)
	if err != nil {
		t.Fatalf("Replay %s: %v", name, err)
	}
	return m
}

func TestBootstrapBatchOrder(t *testing.T) {
	cfg := loadConfig(t, func(c map[string]any) {
		c["tasks"].(map[string]any)["plan-day"].(map[string]any)["link"] = "https://example.test/plan"
	})
	env := emptyEnv(t, cfg, at(-60))
	c := command.ChangePeriods{ID: clientID, Changes: []command.PeriodChange{dayTo(day1), weekFrom(week1), sprintFrom(sprint1)}}
	p := mustPlan(t, env, c)

	want := []event.Type{
		event.TypeProfileChanged,
		event.TypePeriodChanged, event.TypeTaskMaterialized, event.TypeTaskMaterialized, event.TypeTaskMaterialized,
		event.TypePeriodChanged, event.TypeTaskMaterialized,
		event.TypePeriodChanged, event.TypeTaskMaterialized,
		event.TypeBatchCommitted,
	}
	if got := typesOf(p); !slices.Equal(got, want) {
		t.Fatalf("types = %v, want %v", got, want)
	}
	if p.BatchID != clientID {
		t.Errorf("batch = %s, want the request id %s", p.BatchID, clientID)
	}
	checkBatch(t, p, at(-60))
	hash := hashOf(t, c)
	for i, e := range p.Events {
		if e.ReqHash != hash {
			t.Errorf("event %d req_hash = %q, want %q", i, e.ReqHash, hash)
		}
		if e.ID != idOf('N', uint32(i+1)) {
			t.Errorf("event %d id = %s, want the fresh id %s", i, e.ID, idOf('N', uint32(i+1)))
		}
	}
	if got := p.Events[0].Payload.(event.ProfileChanged).Profile; got != cfg.Defaults().Profile {
		t.Errorf("bootstrap profile = %q, want defaults.profile %q", got, cfg.Defaults().Profile)
	}
	kinds := payloadsOf[event.PeriodChanged](p)
	if len(kinds) != 3 || kinds[0].Kind != "day" || kinds[1].Kind != "week" || kinds[2].Kind != "sprint" {
		t.Fatalf("period changes = %+v", kinds)
	}
	if kinds[0].End != nil || *kinds[1].End != week1.AddDays(6) || kinds[2].Label != "Sprint 1" || kinds[1].TZ != newYork {
		t.Errorf("period changes = %+v", kinds)
	}

	periods := map[due.Cadence][2]civil.Date{
		due.Daily: {day1, day1}, due.Weekly: {week1, week1.AddDays(6)}, due.Sprint: {sprint1, sprint1.AddDays(13)},
	}
	wantIDs := []event.TaskID{
		event.NewTaskID(due.Daily, day1, "plan-day"), event.NewTaskID(due.Daily, day1, "post-plan"),
		event.NewTaskID(due.Daily, day1, "end-of-day-summary"),
		event.NewTaskID(due.Weekly, week1, "weekly-update"), event.NewTaskID(due.Sprint, sprint1, "capacity-check"),
	}
	mats := payloadsOf[event.TaskMaterialized](p)
	if got := taskIDsOf[event.TaskMaterialized](p); !slices.Equal(got, wantIDs) {
		t.Fatalf("materialized = %v, want %v", got, wantIDs)
	}
	for _, m := range mats {
		def, ok := cfg.Task(m.Definition)
		if !ok {
			t.Fatalf("definition %q is not in the config", m.Definition)
		}
		span := periods[def.Cadence]
		res, err := due.Resolve(def.Cadence, def.Due, span[0], span[1])
		if err != nil {
			t.Fatalf("Resolve %s: %v", m.Definition, err)
		}
		if m.Title != def.Title || m.Group != def.Group || m.Link != def.Link || m.Cadence != def.Cadence || m.Period != span[0] {
			t.Errorf("%s snapshot = %+v, want the definition %+v", m.TaskID, m, def)
		}
		if !m.Due.Time().Equal(res.Instant) {
			t.Errorf("%s due = %v, want %v", m.TaskID, m.Due.Time(), res.Instant)
		}
		if m.DueRule.TZ.Name() != newYork || m.DueRule.At != def.Due.At || !reflect.DeepEqual(m.DueRule.Weekday, def.Due.Weekday) || !reflect.DeepEqual(m.DueRule.Day, def.Due.Day) {
			t.Errorf("%s due_rule = %+v, want %+v", m.TaskID, m.DueRule, def.Due)
		}
	}
	if mats[0].Link != "https://example.test/plan" {
		t.Errorf("link = %q, want the definition's", mats[0].Link)
	}

	m := writeGolden(t, "bootstrap.jsonl", p.Candidate.Log())
	if m.Profile() != "normal" || len(m.Tasks()) != 5 {
		t.Errorf("replayed golden: profile %q, %d tasks", m.Profile(), len(m.Tasks()))
	}
	if d, _ := m.Period(projection.Day); d.Start != day1 {
		t.Errorf("replayed golden: day %s, want %s", d.Start, day1)
	}
}

func TestRolloverGoldenLog(t *testing.T) {
	env, _ := begun(t, loadConfig(t, nil))
	env = extend(t, env, at(30), event.TaskCompleted{TaskID: planDay})
	env.Now = at(24 * 60)
	p := mustPlan(t, env, command.ChangePeriods{
		Changes:   []command.PeriodChange{dayTo(day2)},
		Overrides: []command.Override{{TaskID: postPlan, Reason: "the meeting moved"}},
	})
	m := writeGolden(t, "rollover.jsonl", p.Candidate.Log())
	for id, want := range map[event.TaskID]projection.TaskStatus{
		planDay: projection.Completed, postPlan: projection.Skipped,
		event.NewTaskID(due.Daily, day1, "end-of-day-summary"): projection.Missed,
		event.NewTaskID(due.Daily, day2, "post-plan"):          projection.Open,
		event.NewTaskID(due.Weekly, week1, "weekly-update"):    projection.Open,
	} {
		if got := taskStatus(t, m, id); got != want {
			t.Errorf("%s = %s, want %s", id, got, want)
		}
	}
}

// fiveTasks is a stored log whose day holds a task in each status: open,
// completed, skipped, missed and withdrawn.
func fiveTasks(t *testing.T) (*logb, map[string]event.TaskID) {
	t.Helper()
	ids := map[string]event.TaskID{}
	for _, def := range []string{"a-open", "b-completed", "c-skipped", "d-missed", "e-withdrawn"} {
		ids[def] = event.NewTaskID(due.Daily, day1, def)
	}
	b := newLog(t)
	b.batch(
		-60,
		func(bt event.ID) event.Payload { return event.ProfileChanged{Profile: "normal", Batch: bt} },
		func(bt event.ID) event.Payload {
			return event.PeriodChanged{Kind: "day", Start: day1, TZ: newYork, Batch: bt}
		},
		taskOf("a-open"), taskOf("b-completed"), taskOf("c-skipped"), taskOf("d-missed"), taskOf("e-withdrawn"),
	)
	b.add(10, event.TaskCompleted{TaskID: ids["b-completed"]})
	b.add(20, event.TaskSkipped{TaskID: ids["c-skipped"], Reason: "away"})
	b.batch(30, func(bt event.ID) event.Payload { return event.TaskMissed{TaskID: ids["d-missed"], Batch: bt} })
	b.batch(40, func(bt event.ID) event.Payload { return event.TaskWithdrawn{TaskID: ids["e-withdrawn"], Batch: bt} })
	return b, ids
}

func TestRolloverMissesOpenTasksOnly(t *testing.T) {
	b, ids := fiveTasks(t)
	env := envOf(t, b, at(24*60))
	eff := at(23 * 60)
	p := mustPlan(t, env, command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day2)}, EffectiveAt: &eff})
	checkBatch(t, p, eff)
	if got := taskIDsOf[event.TaskMissed](p); !sameIDs(got, ids["a-open"]) {
		t.Errorf("missed = %v, want only the open task %s", got, ids["a-open"])
	}
	if got := typesOf(p)[0]; got != event.TypeTaskMissed {
		t.Errorf("the batch begins with %s, want the rollover's task.missed", got)
	}
	for _, e := range p.Events {
		switch pl := e.Payload.(type) {
		case event.TaskSkipped, event.TaskWithdrawn, event.TaskReinstated, event.TaskCompleted:
			t.Errorf("the rollover adds %s %+v", e.Type, pl)
		}
	}
	for def, want := range map[string]projection.TaskStatus{
		"a-open": projection.Missed, "b-completed": projection.Completed, "c-skipped": projection.Skipped,
		"d-missed": projection.Missed, "e-withdrawn": projection.Withdrawn,
	} {
		if got := taskStatus(t, p.Candidate, ids[def]); got != want {
			t.Errorf("%s after the rollover = %s, want %s", def, got, want)
		}
	}
	task, _ := p.Candidate.Task(ids["c-skipped"])
	if task.Reason != "away" {
		t.Errorf("the skipped task's reason = %q, want it untouched", task.Reason)
	}
}

func TestOverridesAndSkipAllPrecedence(t *testing.T) {
	now := at(24 * 60)
	t.Run("an override wins over skip_all_reason", func(t *testing.T) {
		p := mustPlan(t, envOf(t, bootstrapped(t), now), command.ChangePeriods{
			Changes:       []command.PeriodChange{dayTo(day2)},
			Overrides:     []command.Override{{TaskID: postPlan, Reason: "  the meeting moved "}},
			SkipAllReason: strPtr(" out sick "),
		})
		if got := taskIDsOf[event.TaskMissed](p); len(got) != 0 {
			t.Errorf("missed = %v, want none under skip_all_reason", got)
		}
		reasons := map[event.TaskID]string{}
		for _, s := range payloadsOf[event.TaskSkipped](p) {
			reasons[s.TaskID] = s.Reason
		}
		if want := map[event.TaskID]string{postPlan: "the meeting moved", planDay: "out sick"}; !reflect.DeepEqual(reasons, want) {
			t.Errorf("skips = %v, want %v", reasons, want)
		}
	})
	t.Run("an override alone skips its task and misses the rest", func(t *testing.T) {
		p := mustPlan(t, envOf(t, bootstrapped(t), now), command.ChangePeriods{
			Changes:   []command.PeriodChange{dayTo(day2)},
			Overrides: []command.Override{{TaskID: postPlan, Reason: "the meeting moved"}},
		})
		if got := taskIDsOf[event.TaskSkipped](p); !sameIDs(got, postPlan) {
			t.Errorf("skipped = %v, want %s", got, postPlan)
		}
		if got := taskIDsOf[event.TaskMissed](p); !sameIDs(got, planDay) {
			t.Errorf("missed = %v, want %s", got, planDay)
		}
	})
	t.Run("an override naming an unknown task rejects the request", func(t *testing.T) {
		r := mustReject(t, envOf(t, bootstrapped(t), now), command.ChangePeriods{
			Changes:   []command.PeriodChange{dayTo(day2)},
			Overrides: []command.Override{{TaskID: postPlan, Reason: "x"}, {TaskID: "day:2026-10-07:nothing", Reason: "x"}},
		}, command.ReasonUnknownTask)
		if r.Reason.Status() != 404 || !strings.Contains(r.Message, "day:2026-10-07:nothing") {
			t.Errorf("status %d, message %q", r.Reason.Status(), r.Message)
		}
	})
	t.Run("an override naming a resolved task rejects the request", func(t *testing.T) {
		b := bootstrapped(t)
		stored := b.add(10, event.TaskCompleted{TaskID: postPlan})
		r := mustReject(t, envOf(t, b, now), command.ChangePeriods{
			Changes:   []command.PeriodChange{dayTo(day2)},
			Overrides: []command.Override{{TaskID: postPlan, Reason: "x"}},
		}, command.ReasonTaskAlreadyResolved)
		if !sameIDs(r.Events, stored) || r.Entity != string(postPlan) {
			t.Errorf("Events %v, Entity %q, want the stored completion %s of %s", r.Events, r.Entity, stored, postPlan)
		}
	})
}

func TestOverrideAndSkipAllReasonsMustBeNonBlank(t *testing.T) {
	for _, blank := range []string{"", "  ", "\t\n", "　"} {
		env := envOf(t, bootstrapped(t), at(24*60))
		mustReject(t, env, command.ChangePeriods{
			Changes:   []command.PeriodChange{dayTo(day2)},
			Overrides: []command.Override{{TaskID: postPlan, Reason: blank}},
		}, command.ReasonInvalidRequest)
		mustReject(t, env, command.ChangePeriods{
			Changes:       []command.PeriodChange{dayTo(day2)},
			SkipAllReason: strPtr(blank),
		}, command.ReasonInvalidRequest)
	}
}

func TestPeriodUnchangedRejected(t *testing.T) {
	now := at(24 * 60)
	b := bootstrapped(t)
	opened := b.events[1].ID // the stored day change of day1
	for _, tc := range []struct {
		name  string
		start civil.Date
		eff   *time.Time
	}{
		{"the same start", day1, nil},
		{"an earlier start", day1.AddDays(-1), nil},
		{"an earlier start backdated before the stored change", day1.AddDays(-1), ptr(at(-120))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := mustReject(t, envOf(t, b, now), command.ChangePeriods{Changes: []command.PeriodChange{dayTo(tc.start)}, EffectiveAt: tc.eff}, command.ReasonPeriodUnchanged)
			if r.Reason.Status() != 409 || r.Entity != "day" || !sameIDs(r.Events, opened) {
				t.Errorf("status %d, Entity %q, Events %v, want 409, day and %s", r.Reason.Status(), r.Entity, r.Events, opened)
			}
			for _, want := range []string{"the new event", string(opened), tc.start.String(), day1.String()} {
				if !strings.Contains(r.Message, want) {
					t.Errorf("Message %q does not mention %q", r.Message, want)
				}
			}
		})
	}

	t.Run("replay gives the same code from the events alone", func(t *testing.T) {
		env := envOf(t, b, now)
		bt := idOf('X', 9)
		var add []event.Event
		for i, pl := range []event.Payload{event.PeriodChanged{Kind: "day", Start: day1, TZ: newYork, Batch: bt}, event.BatchCommitted{Batch: bt}} {
			line, err := event.Encode(event.Event{
				Envelope: event.Envelope{V: event.SchemaVersion, ID: idOf('X', uint32(i+1)), At: event.At(now), EffectiveAt: event.At(now), Type: pl.EventType()},
				Payload:  pl,
			})
			if err != nil {
				t.Fatal(err)
			}
			e, err := event.Decode(line)
			if err != nil {
				t.Fatal(err)
			}
			add = append(add, e)
		}
		_, err := projection.Candidate(env.Model.Log(), add)
		var inv *projection.Invalid
		if !errors.As(err, &inv) || string(inv.Code) != string(command.ReasonPeriodUnchanged) {
			t.Errorf("Candidate = %v, want period_unchanged", err)
		}
	})
}

// threeDays is a stored log whose profile is set on 2026-10-01 and whose day
// is changed to the 1st, effective then, and to the 8th, effective then; each
// change is recorded at noon UTC.
func threeDays(t *testing.T) (*logb, event.ID) {
	t.Helper()
	noon := func(day int) time.Time { return time.Date(2026, time.October, day, 12, 0, 0, 0, time.UTC) }
	b := newLog(t)
	bt := b.id()
	b.addAt(noon(1), noon(1), event.ProfileChanged{Profile: "normal", Batch: bt})
	b.addAt(noon(1), noon(1), event.BatchCommitted{Batch: bt})
	var eighth event.ID
	for _, d := range []int{1, 8} {
		bt := b.id()
		id := b.addAt(noon(8), noon(d), event.PeriodChanged{Kind: "day", Start: civil.Date{Year: 2026, Month: time.October, Day: d}, TZ: newYork, Batch: bt})
		b.addAt(noon(8), noon(8), event.BatchCommitted{Batch: bt})
		eighth = id
	}
	return b, eighth
}

func TestBackdatedChangeBeforeAnEarlierOneIsPeriodOutOfOrder(t *testing.T) {
	b, eighth := threeDays(t)
	eff := time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)
	env := envOf(t, b, time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC))
	r := mustReject(t, env, command.ChangePeriods{
		Changes:     []command.PeriodChange{dayTo(civil.Date{Year: 2026, Month: time.October, Day: 9})},
		EffectiveAt: &eff,
	}, command.ReasonPeriodOutOfOrder)
	if r.Reason.Status() != 422 || r.Entity != "day" || !slices.Contains(r.Events, eighth) {
		t.Errorf("status %d, Entity %q, Events %v, want 422, day and the change of the 8th %s", r.Reason.Status(), r.Entity, r.Events, eighth)
	}
	if !slices.ContainsFunc(r.Instants, eff.Equal) {
		t.Errorf("Instants %v do not include the new event's effective_at %v", r.Instants, eff)
	}
}

func TestEachKindChangesIndependently(t *testing.T) {
	weekly := event.NewTaskID(due.Weekly, week1, "weekly-update")
	sprint := event.NewTaskID(due.Sprint, sprint1, "capacity-check")

	t.Run("changing the day leaves the week and the sprint", func(t *testing.T) {
		env, _ := begun(t, loadConfig(t, nil))
		env.Now = at(24 * 60)
		p := mustPlan(t, env, command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day2)}})
		for _, e := range p.Events {
			if strings.Contains(string(e.Data), "week:") || strings.Contains(string(e.Data), "sprint:") {
				t.Errorf("%s touches a weekly or sprint task: %s", e.Type, e.Data)
			}
		}
		if got := payloadsOf[event.PeriodChanged](p); len(got) != 1 || got[0].Kind != "day" {
			t.Errorf("period changes = %+v, want the day's alone", got)
		}
		if taskStatus(t, p.Candidate, weekly) != projection.Open || taskStatus(t, p.Candidate, sprint) != projection.Open {
			t.Error("a weekly or sprint task is no longer open")
		}
		before, _ := env.Model.Period(projection.Week)
		after, _ := p.Candidate.Period(projection.Week)
		if before != after {
			t.Errorf("week %+v became %+v", before, after)
		}
	})
	t.Run("changing the week leaves the day", func(t *testing.T) {
		env, _ := begun(t, loadConfig(t, nil))
		env.Now = at(5 * 24 * 60)
		p := mustPlan(t, env, command.ChangePeriods{Changes: []command.PeriodChange{weekFrom(week1.AddDays(7))}})
		if got := taskIDsOf[event.TaskMissed](p); !sameIDs(got, weekly) {
			t.Errorf("missed = %v, want the weekly task alone", got)
		}
		if got := taskIDsOf[event.TaskMaterialized](p); !sameIDs(got, event.NewTaskID(due.Weekly, week1.AddDays(7), "weekly-update")) {
			t.Errorf("materialized = %v, want the new week's task alone", got)
		}
		if taskStatus(t, p.Candidate, postPlan) != projection.Open {
			t.Error("the day's task is no longer open")
		}
	})
}

func TestEndRequiredForWeekAndSprintAbsentForDay(t *testing.T) {
	week := weekFrom(week1.AddDays(7))
	week.End = nil
	sprint := sprintFrom(sprint1.AddDays(14))
	sprint.End = nil
	day := dayTo(day2)
	day.End = datePtr(day2)
	backwards := weekFrom(week1.AddDays(7))
	backwards.End = datePtr(week1)
	noStart := dayTo(day2)
	noStart.Start = civil.Date{}
	impossible := dayTo(civil.Date{Year: 2026, Month: time.November, Day: 31})
	unknownKind := dayTo(day2)
	unknownKind.Kind = "month"
	badLabel := weekFrom(week1.AddDays(7))
	badLabel.Label = "w \xff"
	hugeLabel := weekFrom(week1.AddDays(7))
	hugeLabel.Label = strings.Repeat("l", 300*1024)
	for name, changes := range map[string][]command.PeriodChange{
		"a week without end":                 {week},
		"a sprint without end":               {sprint},
		"a day with an end":                  {day},
		"an end before the start":            {backwards},
		"no start":                           {noStart},
		"a date that does not exist":         {impossible},
		"an unknown kind":                    {unknownKind},
		"two changes of one kind":            {dayTo(day2), dayTo(day2.AddDays(1))},
		"no change at all":                   nil,
		"a label holding the byte 0xff":      {badLabel},
		"a label longer than an event holds": {hugeLabel},
	} {
		t.Run(name, func(t *testing.T) {
			r := mustReject(t, envOf(t, bootstrapped(t), at(10*24*60)), command.ChangePeriods{Changes: changes}, command.ReasonInvalidRequest)
			if r.Reason.Status() != 400 {
				t.Errorf("status %d, want 400", r.Reason.Status())
			}
		})
	}
}

func TestBackdatingIsUnrestricted(t *testing.T) {
	// The day was set ten days before t0, so the log allows a rollover
	// backdated by up to ten days.
	cfg := loadConfig(t, nil)
	env := emptyEnv(t, cfg, at(-10*24*60))
	boot := mustPlan(t, env, command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day1)}})
	now := at(9 * 24 * 60)
	env = then(t, env, boot, now)

	for _, tc := range []struct {
		name string
		eff  time.Time
	}{
		{"by a day", now.Add(-24 * time.Hour)},
		{"by a week", now.Add(-7 * 24 * time.Hour)},
		{"to before the start of the day it leaves", at(-12 * 60)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := mustPlan(t, env, command.ChangePeriods{
				Changes:     []command.PeriodChange{dayTo(day2)},
				EffectiveAt: ptr(tc.eff),
				Overrides:   []command.Override{{TaskID: postPlan, Reason: "forgot to roll"}},
			})
			checkBatch(t, p, tc.eff)
			if got := taskIDsOf[event.TaskSkipped](p); !sameIDs(got, postPlan) {
				t.Errorf("skipped = %v, want %s", got, postPlan)
			}
			if got := taskIDsOf[event.TaskMissed](p); len(got) != 2 {
				t.Errorf("missed = %v, want the two other daily tasks", got)
			}
			task, _ := p.Candidate.Task(postPlan)
			if task.ResolvedAt == nil || !task.ResolvedAt.Equal(tc.eff) {
				t.Errorf("the skip is effective at %v, want %v", task.ResolvedAt, tc.eff)
			}
		})
	}
	t.Run("future_effective_at still applies", func(t *testing.T) {
		mustReject(t, env, command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day2)}, EffectiveAt: ptr(now.Add(61 * time.Second))}, command.ReasonFutureEffectiveAt)
	})
	t.Run("a time the timeline cannot hold gets the code of its condition", func(t *testing.T) {
		mustReject(t, env, command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day2)}, EffectiveAt: ptr(at(-11 * 24 * 60))}, command.ReasonPeriodOutOfOrder)
	})
	t.Run("no setting bounds it", func(t *testing.T) {
		typ := reflect.TypeOf(config.Defaults{})
		for i := range typ.NumField() {
			name := strings.ToLower(typ.Field(i).Name)
			for _, banned := range []string{"backdat", "daystart", "bound", "maxpast"} {
				if strings.Contains(name, banned) {
					t.Errorf("config.Defaults has the field %s", typ.Field(i).Name)
				}
			}
		}
	})
}

func TestChangePeriodsMapsZoneErrorToInvalidZone(t *testing.T) {
	for _, tz := range []string{"ET", "Local", "", "america/new_york", "-05:00"} {
		t.Run(tz, func(t *testing.T) {
			c := dayTo(day2)
			c.TZ = tz
			r := mustReject(t, envOf(t, bootstrapped(t), at(24*60)), command.ChangePeriods{Changes: []command.PeriodChange{c}}, command.ReasonInvalidZone)
			if r.Reason.Status() != 400 || !strings.Contains(r.Message, `"`+tz+`"`) {
				t.Errorf("status %d, message %q does not name the zone", r.Reason.Status(), r.Message)
			}
		})
	}
}

func TestChangePeriodsCheckOrder(t *testing.T) {
	now := at(24 * 60)
	future := now.Add(2 * time.Minute)
	env := func(t *testing.T, cycle bool) command.Env {
		b := bootstrapped(t)
		if cycle {
			b.add(0, startOf(cycleA, deepWork))
		}
		e := envOf(t, b, now)
		e.Version = command.Version{LogLines: e.Model.Lines(), ConfigGeneration: 3}
		return e
	}
	badZone := dayTo(day2)
	badZone.TZ = "ET"
	stale := &command.Version{LogLines: 1, ConfigGeneration: 3}
	ghost := []command.Override{{TaskID: "day:2026-10-07:nothing", Reason: "x"}}
	blank := []command.Override{{TaskID: postPlan, Reason: "  "}}
	day2Only := []command.PeriodChange{dayTo(day2)}
	tests := []struct {
		name          string
		first, second command.Reason
		both, only2nd command.ChangePeriods
		cycle         bool
	}{
		{
			"invalid_request before future_effective_at", command.ReasonInvalidRequest, command.ReasonFutureEffectiveAt,
			command.ChangePeriods{Changes: day2Only, Overrides: blank, EffectiveAt: &future},
			command.ChangePeriods{Changes: day2Only, EffectiveAt: &future},
			false,
		},
		{
			"future_effective_at before invalid_zone", command.ReasonFutureEffectiveAt, command.ReasonInvalidZone,
			command.ChangePeriods{Changes: []command.PeriodChange{badZone}, EffectiveAt: &future},
			command.ChangePeriods{Changes: []command.PeriodChange{badZone}},
			false,
		},
		{
			"invalid_zone before unknown_profile", command.ReasonInvalidZone, command.ReasonUnknownProfile,
			command.ChangePeriods{Changes: []command.PeriodChange{badZone}, Profile: "nothing"},
			command.ChangePeriods{Changes: day2Only, Profile: "nothing"},
			false,
		},
		{
			"unknown_profile before unknown_task", command.ReasonUnknownProfile, command.ReasonUnknownTask,
			command.ChangePeriods{Changes: day2Only, Profile: "nothing", Overrides: ghost},
			command.ChangePeriods{Changes: day2Only, Overrides: ghost},
			false,
		},
		{
			"unknown_task before stale_preview", command.ReasonUnknownTask, command.ReasonStalePreview,
			command.ChangePeriods{Changes: day2Only, Overrides: ghost, ExpectedVersion: stale},
			command.ChangePeriods{Changes: day2Only, ExpectedVersion: stale},
			false,
		},
		{
			"stale_preview before period_unchanged", command.ReasonStalePreview, command.ReasonPeriodUnchanged,
			command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day1)}, ExpectedVersion: stale},
			command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day1)}},
			false,
		},
		{
			"period_unchanged before cycle_active", command.ReasonPeriodUnchanged, command.ReasonCycleActive,
			command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day1)}},
			command.ChangePeriods{Changes: day2Only},
			true,
		},
		{
			"cycle_active before the candidate replay", command.ReasonCycleActive, command.ReasonPeriodOutOfOrder,
			command.ChangePeriods{Changes: day2Only, EffectiveAt: ptr(at(-120))},
			command.ChangePeriods{Changes: day2Only, EffectiveAt: ptr(at(-120))},
			true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mustReject(t, env(t, tc.cycle), tc.both, tc.first)
			// The second rule alone gives its own code. A cycle_active pair
			// keeps the cycle for the first request only.
			second := env(t, tc.cycle && tc.second == command.ReasonCycleActive)
			mustReject(t, second, tc.only2nd, tc.second)
		})
	}
}

func TestNoCarryOver(t *testing.T) {
	env, _ := begun(t, loadConfig(t, nil))
	env.Now = at(24 * 60)
	p := mustPlan(t, env, command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day2)}})
	for _, m := range payloadsOf[event.TaskMaterialized](p) {
		if _, left := env.Model.Task(m.TaskID); left || m.Period != day2 {
			t.Errorf("%s (period %s) belongs to the day being left", m.TaskID, m.Period)
		}
	}
	for _, v := range []any{command.ChangePeriods{}, command.PeriodChange{}, command.Override{}, command.ChangeProfile{}, command.Preview{}, command.TaskRef{}} {
		typ := reflect.TypeOf(v)
		for i := range typ.NumField() {
			if strings.Contains(strings.ToLower(typ.Field(i).Name), "carry") {
				t.Errorf("%s has the field %s", typ.Name(), typ.Field(i).Name)
			}
		}
	}
}
