package command_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// commandsOf builds one command of each kind of this layer with the given
// client id and effective_at (ignored by annotate, which takes none).
type commandCase struct {
	name     string
	build    func(id event.ID, eff *time.Time) command.Command
	timeless bool // takes no effective_at
}

func taskAndCycleCommands() []commandCase {
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
	for _, cc := range taskAndCycleCommands() {
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
		}
		for _, p := range pairs {
			if hashOf(t, p[0]) == hashOf(t, p[1]) {
				t.Errorf("%+v and %+v hash the same", p[0], p[1])
			}
		}
	})
}

func TestIsDryRunIsFalseForTheseCommands(t *testing.T) {
	for _, cc := range taskAndCycleCommands() {
		if c := cc.build(clientID, nil); c.IsDryRun() {
			t.Errorf("%s: IsDryRun() = true", cc.name)
		}
	}
}

func TestCommandNamesAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, cc := range taskAndCycleCommands() {
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
	for _, cc := range taskAndCycleCommands() {
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
