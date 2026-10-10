package command_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// commandCase builds one command of a kind with the given client id and
// effective_at (ignored by the commands that take none).
type commandCase struct {
	name     string
	build    func(id event.ID, eff *time.Time) command.Command
	timeless bool // takes no effective_at
}

// commandsWithoutDryRun is one command of each kind that has no dry run.
func commandsWithoutDryRun() []commandCase {
	return []commandCase{
		{name: "complete", build: func(id event.ID, eff *time.Time) command.Command {
			return command.CompleteTask{ID: id, TaskID: postPlan, EffectiveAt: eff}
		}},
		{name: "skip", build: func(id event.ID, eff *time.Time) command.Command {
			return command.SkipTask{ID: id, TaskID: postPlan, Reason: "late", EffectiveAt: eff}
		}},
		{name: "start", build: func(id event.ID, eff *time.Time) command.Command {
			return command.StartCycle{ID: id, Type: deepWork, Minutes: intPtr(30), EffectiveAt: eff}
		}},
		{name: "pause", build: func(id event.ID, eff *time.Time) command.Command {
			return command.PauseCycle{ID: id, CycleID: cycleA, EffectiveAt: eff}
		}},
		{name: "resume", build: func(id event.ID, eff *time.Time) command.Command {
			return command.ResumeCycle{ID: id, CycleID: cycleA, EffectiveAt: eff}
		}},
		{name: "boost", build: func(id event.ID, eff *time.Time) command.Command {
			return command.BoostCycle{ID: id, CycleID: cycleA, Minutes: 10, EffectiveAt: eff}
		}},
		{name: "stop", build: func(id event.ID, eff *time.Time) command.Command {
			return command.StopCycle{ID: id, CycleID: cycleA, EffectiveAt: eff}
		}},
		{name: "switch", build: func(id event.ID, eff *time.Time) command.Command {
			return command.SwitchCycle{ID: id, To: cycleA, EffectiveAt: eff}
		}},
		{name: "annotate", timeless: true, build: func(id event.ID, _ *time.Time) command.Command {
			return command.AnnotateCycle{ID: id, CycleID: cycleA, Note: "notes", KV: []event.KV{{Key: "ticket", Value: "T-1"}}}
		}},
		{name: "break", timeless: true, build: func(id event.ID, _ *time.Time) command.Command {
			return command.BackfillBreak{ID: id, CycleID: cycleA, From: at(10), To: at(20)}
		}},
		{name: "correct", timeless: true, build: func(id event.ID, _ *time.Time) command.Command {
			return command.Correct{ID: id, Target: idOf('L', 1), Fields: map[string]json.RawMessage{"note": json.RawMessage(`"fixed"`)}, Reason: "typo"}
		}},
		{name: "retract", timeless: true, build: func(id event.ID, _ *time.Time) command.Command {
			return command.Retract{ID: id, Target: idOf('L', 1), Reason: "mistake"}
		}},
	}
}

func hashOf(t *testing.T, c command.Command) string {
	t.Helper()
	h, err := c.ReqHash()
	if err != nil {
		t.Fatalf("%T.ReqHash: %v", c, err)
	}
	if len(h) != 64 {
		t.Fatalf("%T.ReqHash = %q, want 64 hex digits", c, h)
	}
	return h
}

func TestReqHashExcludesIDAndDefaultedFieldsForEveryCommand(t *testing.T) {
	eff := at(5)
	other := at(6)
	seen := map[string]string{}
	for _, cc := range commandsWithoutDryRun() {
		t.Run(cc.name, func(t *testing.T) {
			bare := hashOf(t, cc.build("", nil))
			if got := hashOf(t, cc.build(clientID, nil)); got != bare {
				t.Errorf("the id changes the hash")
			}
			if c := cc.build(clientID, nil); c.ClientID() != clientID {
				t.Errorf("ClientID() = %q, want %q", c.ClientID(), clientID)
			}
			if prev, dup := seen[bare]; dup {
				t.Errorf("hash equals that of %s: the command name does not enter it", prev)
			}
			seen[bare] = cc.name
			if cc.timeless {
				return
			}
			supplied := hashOf(t, cc.build("", &eff))
			if supplied == bare {
				t.Errorf("a supplied effective_at does not enter the hash")
			}
			if got := hashOf(t, cc.build(clientID, &eff)); got != supplied {
				t.Errorf("the id changes the hash of a request with effective_at")
			}
			if got := hashOf(t, cc.build("", &other)); got == supplied {
				t.Errorf("two effective_at values hash the same")
			}
		})
	}

	t.Run("a client field enters the hash", func(t *testing.T) {
		pairs := [][2]command.Command{
			{command.CompleteTask{TaskID: postPlan}, command.CompleteTask{TaskID: planDay}},
			{command.SkipTask{TaskID: postPlan, Reason: "late"}, command.SkipTask{TaskID: postPlan, Reason: "away"}},
			{command.StartCycle{Type: deepWork}, command.StartCycle{Type: deepWork, Minutes: intPtr(30)}},
			{command.PauseCycle{CycleID: cycleA}, command.PauseCycle{}},
			{command.BoostCycle{CycleID: cycleA, Minutes: 5}, command.BoostCycle{CycleID: cycleA, Minutes: 10}},
			{command.SwitchCycle{To: cycleA}, command.SwitchCycle{To: cycleB}},
			{command.AnnotateCycle{CycleID: cycleA, Note: "a"}, command.AnnotateCycle{CycleID: cycleA, Note: "b"}},
			{command.AnnotateCycle{CycleID: cycleA, KV: []event.KV{{Key: "pr", Value: "1"}}}, command.AnnotateCycle{CycleID: cycleA, KV: []event.KV{{Key: "pr", Value: "2"}}}},
			{command.BackfillBreak{CycleID: cycleA, From: at(10), To: at(20)}, command.BackfillBreak{CycleID: cycleA, From: at(11), To: at(20)}},
			{command.BackfillBreak{CycleID: cycleA, From: at(10), To: at(20)}, command.BackfillBreak{CycleID: cycleA, From: at(10), To: at(21)}},
			{command.BackfillBreak{CycleID: cycleA, From: at(10), To: at(20)}, command.BackfillBreak{CycleID: cycleB, From: at(10), To: at(20)}},
			{command.Correct{Target: idOf('L', 1), Fields: map[string]json.RawMessage{"note": json.RawMessage(`"a"`)}}, command.Correct{Target: idOf('L', 1), Fields: map[string]json.RawMessage{"note": json.RawMessage(`"b"`)}}},
			{command.Correct{Target: idOf('L', 1), Fields: map[string]json.RawMessage{"note": json.RawMessage(`"a"`)}}, command.Correct{Target: idOf('L', 2), Fields: map[string]json.RawMessage{"note": json.RawMessage(`"a"`)}}},
			{command.Correct{Target: idOf('L', 1), Fields: map[string]json.RawMessage{"note": json.RawMessage(`"a"`)}}, command.Correct{Target: idOf('L', 1), Fields: map[string]json.RawMessage{"note": json.RawMessage(`"a"`)}, Reason: "typo"}},
			{command.Retract{Target: idOf('L', 1)}, command.Retract{TargetBatch: idOf('L', 1)}},
			{command.Retract{Target: idOf('L', 1)}, command.Retract{Target: idOf('L', 1), Reason: "mistake"}},
		}
		for _, p := range pairs {
			if hashOf(t, p[0]) == hashOf(t, p[1]) {
				t.Errorf("%+v and %+v hash the same", p[0], p[1])
			}
		}
	})

	t.Run("a break's instants enter the hash to the millisecond, as stored", func(t *testing.T) {
		c := command.BackfillBreak{CycleID: cycleA, From: at(10), To: at(20)}
		finer := command.BackfillBreak{CycleID: cycleA, From: at(10).Add(time.Microsecond), To: at(20)}
		if hashOf(t, c) != hashOf(t, finer) {
			t.Error("a sub-millisecond difference, which the stored instant drops, changes the hash")
		}
	})

	t.Run("equivalent JSON in a correction's fields hashes the same", func(t *testing.T) {
		a := command.Correct{Target: idOf('L', 1), Fields: map[string]json.RawMessage{"kv": json.RawMessage(`[{"key":"pr","value":"1"}]`)}}
		b := command.Correct{Target: idOf('L', 1), Fields: map[string]json.RawMessage{"kv": json.RawMessage(`[ {"value": "1", "key": "pr"} ]`)}}
		if hashOf(t, a) != hashOf(t, b) {
			t.Error("whitespace or key order in a replacement value changes the hash")
		}
	})
}

func TestIsDryRunIsFalseForTheseCommands(t *testing.T) {
	for _, cc := range commandsWithoutDryRun() {
		if c := cc.build(clientID, nil); c.IsDryRun() {
			t.Errorf("%s: IsDryRun() = true", cc.name)
		}
	}
}

func TestCommandNamesAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, cc := range commandsWithoutDryRun() {
		name := cc.build("", nil).Name()
		if name == "" || seen[name] {
			t.Errorf("%s: Name() = %q, empty or repeated", cc.name, name)
		}
		seen[name] = true
	}
}

func TestNoCommandHasCarryOverField(t *testing.T) {
	// No carry-over: a task still open at rollover is missed or skipped,
	// never carried into the next period, so no command can ask for it.
	for _, cc := range commandsWithoutDryRun() {
		typ := reflect.TypeOf(cc.build("", nil))
		for i := range typ.NumField() {
			name := strings.ToLower(typ.Field(i).Name)
			if strings.Contains(name, "carry") {
				t.Errorf("%s has the field %s", typ.Name(), typ.Field(i).Name)
			}
		}
	}
}

func TestUnknownTargetsAreCheckedBeforeStateCodes(t *testing.T) {
	const ghost = event.CycleID("01J9ZZZZZZZZZZZZZZZZZZZZZG")
	ghostEvent := idOf('G', 1)
	idle := func(t *testing.T) *logb {
		b := bootstrapped(t)
		b.add(0, startOf(cycleA, deepWork))
		b.add(10, stopOf(cycleA))
		return b
	}
	paused := func(t *testing.T) *logb {
		b := bootstrapped(t)
		b.add(0, startOf(cycleA, deepWork))
		b.add(10, pauseOf(cycleA))
		b.add(15, startOf(cycleB, review))
		b.add(20, pauseOf(cycleB))
		return b
	}
	tests := []struct {
		name  string
		build func(t *testing.T) *logb
		now   time.Time
		cmd   command.Command
		want  command.Reason
	}{
		{"a switch to an unknown cycle while nothing runs", idle, at(30), command.SwitchCycle{To: ghost}, command.ReasonUnknownCycle},
		{"a pause of an unknown cycle while nothing runs", idle, at(30), command.PauseCycle{CycleID: ghost}, command.ReasonUnknownCycle},
		{"a resume of an unknown cycle while two are paused", paused, at(30), command.ResumeCycle{CycleID: ghost}, command.ReasonUnknownCycle},
		{"a stop of an unknown cycle with the clock behind the log", paused, at(5), command.StopCycle{CycleID: ghost}, command.ReasonUnknownCycle},
		{"an annotation of an unknown cycle", idle, at(30), command.AnnotateCycle{CycleID: ghost, Note: "x"}, command.ReasonUnknownCycle},
		{"a completion of an unknown task with the clock behind the log", idle, at(-90), command.CompleteTask{TaskID: "day:2026-10-07:nothing"}, command.ReasonUnknownTask},
		{"a start of an unknown type", paused, at(30), command.StartCycle{Type: "nothing"}, command.ReasonUnknownCycleType},
		{"a break of an unknown cycle that would end after a stop", idle, at(30), command.BackfillBreak{CycleID: ghost, From: at(5), To: at(20)}, command.ReasonUnknownCycle},
		{"a correction of an unknown event with an identity field", idle, at(30), command.Correct{Target: ghostEvent, Fields: map[string]json.RawMessage{"cycle_id": json.RawMessage(`"x"`)}}, command.ReasonUnknownEvent},
		{"a correction of an unknown event with a client title", idle, at(30), command.Correct{Target: ghostEvent, Fields: map[string]json.RawMessage{"title": json.RawMessage(`"x"`)}}, command.ReasonUnknownEvent},
		{"a correction of an unknown event with an unknown cycle type", idle, at(30), command.Correct{Target: ghostEvent, Fields: map[string]json.RawMessage{"type": json.RawMessage(`"nothing"`)}}, command.ReasonUnknownEvent},
		{"a retraction of an unknown event", idle, at(30), command.Retract{Target: ghostEvent}, command.ReasonUnknownEvent},
		{"a retraction of an unknown batch", paused, at(30), command.Retract{TargetBatch: ghostEvent}, command.ReasonUnknownEvent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := mustReject(t, envOf(t, tc.build(t), tc.now), tc.cmd, tc.want)
			if r.Entity != "" {
				t.Errorf("Entity = %q: an unknown id is no stored entity", r.Entity)
			}
		})
	}
}

func TestFutureEffectiveAtRejected(t *testing.T) {
	running := func(t *testing.T) *logb {
		b := bootstrapped(t)
		b.add(0, startOf(cycleA, deepWork))
		return b
	}
	now := at(10)
	tests := []struct {
		name    string
		skew    int // 0 keeps the configured default
		ahead   time.Duration
		reject  bool
		command func(eff *time.Time) command.Command
	}{
		{"a completion a second past the default skew", 0, 61 * time.Second, true, completeAt},
		{"a completion exactly at the default skew", 0, 60 * time.Second, false, completeAt},
		{"a completion past a larger configured skew", 120, 121 * time.Second, true, completeAt},
		{"a completion within a larger configured skew", 120, 90 * time.Second, false, completeAt},
		{"a boost a millisecond past the skew", 0, 60*time.Second + time.Millisecond, true, func(eff *time.Time) command.Command {
			return command.BoostCycle{CycleID: cycleA, Minutes: 5, EffectiveAt: eff}
		}},
		{"a boost at the skew", 0, 60 * time.Second, false, func(eff *time.Time) command.Command {
			return command.BoostCycle{CycleID: cycleA, Minutes: 5, EffectiveAt: eff}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := envOf(t, running(t), now)
			if tc.skew != 0 {
				env.Config = loadConfig(t, func(c map[string]any) {
					c["defaults"].(map[string]any)["max_future_skew_seconds"] = tc.skew
				})
			}
			eff := now.Add(tc.ahead)
			c := tc.command(&eff)
			if !tc.reject {
				if got := only(t, mustPlan(t, env, c)).EffectiveAt.Time(); !got.Equal(eff) {
					t.Errorf("effective_at = %v, want %v", got, eff)
				}
				return
			}
			r := mustReject(t, env, c, command.ReasonFutureEffectiveAt)
			if r.Reason.Status() != 422 {
				t.Errorf("status %d, want 422", r.Reason.Status())
			}
			if len(r.Instants) == 0 || !r.Instants[0].Equal(eff) {
				t.Errorf("Instants = %v, want the new event's effective_at %v first", r.Instants, eff)
			}
		})
	}
}

func completeAt(eff *time.Time) command.Command {
	return command.CompleteTask{TaskID: postPlan, EffectiveAt: eff}
}

// TestEveryRefusalBeforeTheTargetCarriesTheEffectiveInstant checks that an
// invalid_request, an invalid text and a reserved key name the new event's
// effective_at (the supplied one, else the clock) though the request is
// refused before its target is looked up.
func TestEveryRefusalBeforeTheTargetCarriesTheEffectiveInstant(t *testing.T) {
	now, given := at(10), at(5)
	bad := "bad\xffutf8"
	tooLong := strings.Repeat("x", event.MaxEventBytes)
	tests := []struct {
		name   string
		cmd    command.Command
		reason command.Reason
		want   time.Time
	}{
		{"a completion with no task_id", command.CompleteTask{EffectiveAt: &given}, command.ReasonInvalidRequest, given},
		{"a completion with a bad task_id", command.CompleteTask{TaskID: event.TaskID(bad)}, command.ReasonInvalidRequest, now},
		{"a skip with a blank reason", command.SkipTask{TaskID: postPlan, Reason: " ", EffectiveAt: &given}, command.ReasonInvalidRequest, given},
		{"a skip with a reason too long", command.SkipTask{TaskID: postPlan, Reason: tooLong}, command.ReasonInvalidRequest, now},
		{"a start with no type", command.StartCycle{EffectiveAt: &given}, command.ReasonInvalidRequest, given},
		{"a start with minutes out of range", command.StartCycle{Type: review, Minutes: intPtr(0)}, command.ReasonInvalidRequest, now},
		{"a boost with minutes out of range", command.BoostCycle{CycleID: cycleA, Minutes: 0, EffectiveAt: &given}, command.ReasonInvalidRequest, given},
		{"a pause with a bad cycle_id", command.PauseCycle{CycleID: event.CycleID(bad), EffectiveAt: &given}, command.ReasonInvalidRequest, given},
		{"a switch with no target", command.SwitchCycle{EffectiveAt: &given}, command.ReasonInvalidRequest, given},
		{"an annotation with a bad key", command.AnnotateCycle{CycleID: cycleA, KV: []event.KV{{Key: "Bad Key", Value: "v"}}}, command.ReasonInvalidRequest, now},
		{"an annotation with the reserved key", command.AnnotateCycle{CycleID: cycleA, KV: []event.KV{{Key: "cycle_type", Value: "v"}}}, command.ReasonReservedKey, now},
		{"a profile change with no profile", command.ChangeProfile{}, command.ReasonInvalidRequest, now},
		{"a period change with no change", command.ChangePeriods{EffectiveAt: &given}, command.ReasonInvalidRequest, given},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := bootstrapped(t)
			b.add(0, startOf(cycleA, deepWork))
			r := mustReject(t, envOf(t, b, now), tc.cmd, tc.reason)
			if !slices.EqualFunc(r.Instants, []time.Time{tc.want}, time.Time.Equal) {
				t.Errorf("Instants = %v, want [%v]", r.Instants, tc.want)
			}
		})
	}
}

func TestMinutesBounds(t *testing.T) {
	running := func(t *testing.T) *logb {
		b := bootstrapped(t)
		b.add(0, startOf(cycleA, deepWork))
		return b
	}
	for _, n := range []int{0, -1, 525601} {
		env := envOf(t, running(t), at(10))
		mustReject(t, env, command.StartCycle{Type: review, Minutes: intPtr(n)}, command.ReasonInvalidRequest)
		mustReject(t, env, command.BoostCycle{CycleID: cycleA, Minutes: n}, command.ReasonInvalidRequest)
	}
	env := envOf(t, running(t), at(10))
	start := only(t, mustPlan(t, env, command.StartCycle{Type: review, Minutes: intPtr(525600)}))
	if p := start.Payload.(event.CycleStarted); p.PlannedMinutes != 525600 {
		t.Errorf("planned_minutes = %d, want 525600", p.PlannedMinutes)
	}
	boost := only(t, mustPlan(t, env, command.BoostCycle{CycleID: cycleA, Minutes: 525600}))
	if p := boost.Payload.(event.CycleBoosted); p.Minutes != 525600 {
		t.Errorf("minutes = %d, want 525600", p.Minutes)
	}
}

func TestOversizeAndInvalidUTF8TextRejected(t *testing.T) {
	huge := strings.Repeat("x", 300*1024)
	invalid := "late \xff"
	twoPaused := func(t *testing.T) *logb {
		b := bootstrapped(t)
		b.add(0, startOf(cycleA, deepWork))
		b.add(10, pauseOf(cycleA))
		b.add(15, startOf(cycleB, review))
		b.add(20, pauseOf(cycleB))
		return b
	}
	tests := []struct {
		name string
		cmd  command.Command
	}{
		{"a 300 KiB skip reason", command.SkipTask{TaskID: postPlan, Reason: huge}},
		{"a skip reason holding the byte 0xff", command.SkipTask{TaskID: postPlan, Reason: invalid}},
		{"a 300 KiB note", command.AnnotateCycle{CycleID: cycleA, Note: huge}},
		{"a note holding the byte 0xff", command.AnnotateCycle{CycleID: cycleA, Note: invalid}},
		{"a value holding the byte 0xff", command.AnnotateCycle{CycleID: cycleA, KV: []event.KV{{Key: "ticket", Value: invalid}}}},
		{"a cycle type holding the byte 0xff", command.StartCycle{Type: invalid}},
		{"a task id holding the byte 0xff", command.CompleteTask{TaskID: event.TaskID(invalid)}},
		{"an oversize note is malformed before the cycle is identified", command.AnnotateCycle{Note: huge}},
		{"an invalid client id", command.PauseCycle{ID: "not-an-id", CycleID: cycleA}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := mustReject(t, envOf(t, twoPaused(t), at(30)), tc.cmd, command.ReasonInvalidRequest)
			if r.Reason.Status() != 400 {
				t.Errorf("status %d, want 400", r.Reason.Status())
			}
		})
	}
}

func TestNoteAndKeyValuePairsWhoseSumExceedsTheLimitAreInvalidRequest(t *testing.T) {
	b := bootstrapped(t)
	b.add(0, startOf(cycleA, deepWork))
	env := envOf(t, b, at(10))
	half := strings.Repeat("n", 150*1024)
	value := strings.Repeat("v", 150*1024)
	if len(half) >= event.MaxEventBytes || len(value) >= event.MaxEventBytes {
		t.Fatal("each text alone must be under the limit")
	}
	r := mustReject(t, env, command.AnnotateCycle{CycleID: cycleA, Note: half, KV: []event.KV{{Key: "ticket", Value: value}}}, command.ReasonInvalidRequest)
	if r.Reason.Status() != 400 {
		t.Errorf("status %d, want 400", r.Reason.Status())
	}

	t.Run("together just under the limit they are stored", func(t *testing.T) {
		under := strings.Repeat("n", 100*1024)
		e := only(t, mustPlan(t, env, command.AnnotateCycle{CycleID: cycleA, Note: under, KV: []event.KV{{Key: "ticket", Value: under}}}))
		line, err := event.Encode(e)
		if err != nil || len(line) > event.MaxEventBytes {
			t.Errorf("Encode = %d bytes, %v", len(line), err)
		}
	})

	t.Run("a note that fits alone but not with the envelope", func(t *testing.T) {
		near := strings.Repeat("n", event.MaxEventBytes-100)
		mustReject(t, env, command.AnnotateCycle{ID: clientID, CycleID: cycleA, Note: near}, command.ReasonInvalidRequest)
	})
}

func TestBuildNeedsAModelAConfigAndIDs(t *testing.T) {
	b := bootstrapped(t)
	full := envOf(t, b, at(10))
	for name, env := range map[string]command.Env{
		"no model":  {Config: full.Config, Now: full.Now, NewID: full.NewID},
		"no config": {Model: full.Model, Now: full.Now, NewID: full.NewID},
		"no ids":    {Model: full.Model, Config: full.Config, Now: full.Now},
	} {
		if _, err := command.Build(env, command.CompleteTask{TaskID: postPlan}); err == nil {
			t.Errorf("%s: Build succeeded", name)
		}
	}
	if _, err := command.Build(full, nil); err == nil {
		t.Error("a nil command was built")
	}
}
