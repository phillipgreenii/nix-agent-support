package command_test

import (
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
)

func TestReqHashExcludesIDDryRunAndExpectedVersionAndDefaultedFields(t *testing.T) {
	eff := at(5)
	version := &command.Version{LogLines: 4, ConfigGeneration: 9}
	periods := func(id event.ID, dry bool, v *command.Version, e *time.Time) command.Command {
		return command.ChangePeriods{
			ID: id, DryRun: dry, ExpectedVersion: v, EffectiveAt: e,
			Changes:   []command.PeriodChange{dayTo(day2), weekFrom(week1.AddDays(7))},
			Profile:   "on-call",
			Overrides: []command.Override{{TaskID: postPlan, Reason: "moved"}},
		}
	}
	profile := func(id event.ID, dry bool, v *command.Version, _ *time.Time) command.Command {
		return command.ChangeProfile{ID: id, DryRun: dry, ExpectedVersion: v, Profile: "on-call"}
	}
	for name, build := range map[string]func(event.ID, bool, *command.Version, *time.Time) command.Command{
		"periods": periods, "profile": profile,
	} {
		t.Run(name, func(t *testing.T) {
			bare := hashOf(t, build("", false, nil, nil))
			for _, c := range []command.Command{build(clientID, false, nil, nil), build("", true, nil, nil), build("", false, version, nil), build(clientID, true, version, nil)} {
				if got := hashOf(t, c); got != bare {
					t.Errorf("%+v hashes differently from the bare request", c)
				}
			}
			if c := build(clientID, false, nil, nil); c.ClientID() != clientID {
				t.Errorf("ClientID() = %q, want %q", c.ClientID(), clientID)
			}
			if name == "periods" {
				if hashOf(t, build("", false, nil, &eff)) == bare {
					t.Error("a supplied effective_at does not enter the hash")
				}
				if hashOf(t, build("", false, nil, &eff)) == hashOf(t, build("", false, nil, ptr(at(6)))) {
					t.Error("two effective_at values hash the same")
				}
			}
		})
	}
	if hashOf(t, command.ChangeProfile{Profile: "normal"}) == hashOf(t, command.ChangePeriods{Profile: "normal"}) {
		t.Error("the two commands hash the same: the command name does not enter the hash")
	}

	t.Run("each client field enters the hash", func(t *testing.T) {
		base := command.ChangePeriods{Changes: []command.PeriodChange{weekFrom(week1.AddDays(7))}}
		vary := []func(c *command.ChangePeriods){
			func(c *command.ChangePeriods) { c.Changes[0].Start = week1.AddDays(8) },
			func(c *command.ChangePeriods) { c.Changes[0].End = datePtr(week1.AddDays(20)) },
			func(c *command.ChangePeriods) { c.Changes[0].TZ = "Europe/Paris" },
			func(c *command.ChangePeriods) { c.Changes[0].Label = "another" },
			func(c *command.ChangePeriods) { c.Changes[0].Kind = projection.Sprint },
			func(c *command.ChangePeriods) { c.Profile = "on-call" },
			func(c *command.ChangePeriods) { c.Overrides = []command.Override{{TaskID: postPlan, Reason: "a"}} },
			func(c *command.ChangePeriods) { c.SkipAllReason = strPtr("away") },
		}
		h := hashOf(t, base)
		for i, f := range vary {
			c := base
			c.Changes = slices.Clone(base.Changes)
			f(&c)
			if hashOf(t, c) == h {
				t.Errorf("variation %d does not change the hash", i)
			}
		}
		a := command.ChangePeriods{Changes: base.Changes, Overrides: []command.Override{{TaskID: postPlan, Reason: "a"}}}
		b := command.ChangePeriods{Changes: base.Changes, Overrides: []command.Override{{TaskID: postPlan, Reason: "b"}}}
		if hashOf(t, a) == hashOf(t, b) {
			t.Error("an override's reason does not enter the hash")
		}
		if hashOf(t, command.ChangeProfile{Profile: "normal"}) == hashOf(t, command.ChangeProfile{Profile: "on-call"}) {
			t.Error("the profile does not enter the hash")
		}
	})
}

func TestIsDryRunReflectsTheFlag(t *testing.T) {
	for _, dry := range []bool{false, true} {
		if got := (command.ChangePeriods{DryRun: dry}).IsDryRun(); got != dry {
			t.Errorf("ChangePeriods{DryRun: %v}.IsDryRun() = %v", dry, got)
		}
		if got := (command.ChangeProfile{DryRun: dry}).IsDryRun(); got != dry {
			t.Errorf("ChangeProfile{DryRun: %v}.IsDryRun() = %v", dry, got)
		}
	}
	if (command.ChangePeriods{}).Name() == (command.ChangeProfile{}).Name() {
		t.Error("the two commands share a name")
	}
}

// ulid matches an event id inside an encoded line.
var ulid = regexp.MustCompile(`[0-9A-HJKMNP-TV-Z]{26}`)

// normalized encodes a plan's events with every id replaced by its order of
// first appearance, so two plans compare whatever ids they drew.
func normalized(t *testing.T, p command.Plan) []string {
	t.Helper()
	seen := map[string]string{}
	var out []string
	for _, e := range p.Events {
		line, err := event.Encode(e)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		out = append(out, ulid.ReplaceAllStringFunc(string(line), func(id string) string {
			if _, ok := seen[id]; !ok {
				seen[id] = "#" + string(rune('A'+len(seen)))
			}
			return seen[id]
		}))
	}
	return out
}

func TestDryRunEqualsRealPlan(t *testing.T) {
	base, _ := begun(t, withLight(t))
	base.Now = at(24 * 60)
	empty := emptyEnv(t, withLight(t), at(-60))
	all := []command.PeriodChange{dayTo(day1), weekFrom(week1), sprintFrom(sprint1)}
	for name, tc := range map[string]struct {
		env       command.Env
		dry, real command.Command
	}{
		"a period change with a profile and an override": {
			base,
			command.ChangePeriods{DryRun: true, Changes: []command.PeriodChange{dayTo(day2)}, Profile: "light", Overrides: []command.Override{{TaskID: postPlan, Reason: "moved"}}},
			command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day2)}, Profile: "light", Overrides: []command.Override{{TaskID: postPlan, Reason: "moved"}}},
		},
		"a period change with a request id": {
			base,
			command.ChangePeriods{ID: clientID, DryRun: true, Changes: []command.PeriodChange{weekFrom(week1.AddDays(7))}},
			command.ChangePeriods{ID: clientID, Changes: []command.PeriodChange{weekFrom(week1.AddDays(7))}},
		},
		"a period change with skip_all_reason": {
			base,
			command.ChangePeriods{DryRun: true, Changes: []command.PeriodChange{dayTo(day2)}, SkipAllReason: strPtr("out sick")},
			command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day2)}, SkipAllReason: strPtr("out sick")},
		},
		"a bootstrap": {
			empty,
			command.ChangePeriods{DryRun: true, Changes: all},
			command.ChangePeriods{Changes: all},
		},
		"a bootstrap naming another profile": {
			empty,
			command.ChangePeriods{DryRun: true, Changes: all, Profile: "light"},
			command.ChangePeriods{Changes: all, Profile: "light"},
		},
		"a profile change": {
			base,
			command.ChangeProfile{DryRun: true, Profile: "light"},
			command.ChangeProfile{Profile: "light"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			lines := tc.env.Model.Lines()
			// The real request draws its ids from another source, so the
			// comparison is of the events with their ids normalized.
			var n uint32
			dryEnv, realEnv := tc.env, tc.env
			dryEnv.NewID = newIDs()
			realEnv.NewID = func() event.ID { n++; return idOf('M', n) }
			dry := mustPlan(t, dryEnv, tc.dry)
			actual := mustPlan(t, realEnv, tc.real)
			if got, want := normalized(t, dry), normalized(t, actual); !slices.Equal(got, want) {
				t.Errorf("dry run events\n%v\nwant the real request's\n%v", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			if dry.Preview == nil || actual.Preview != nil {
				t.Errorf("Preview: dry %v, real %v; want one on the dry run alone", dry.Preview, actual.Preview)
			}
			if dry.Candidate == nil || dry.NoOp {
				t.Error("the dry run has no candidate model")
			}
			if tc.env.Model.Lines() != lines {
				t.Errorf("the model has %d lines, want %d: Build appended", tc.env.Model.Lines(), lines)
			}
		})
	}
}

func TestExpectedVersionStale(t *testing.T) {
	b := bootstrapped(t)
	current := command.Version{LogLines: len(b.events), ConfigGeneration: 1760000000000}
	env := envOf(t, b, at(24*60))
	env.Version = current
	for name, v := range map[string]command.Version{
		"log_lines":         {LogLines: current.LogLines + 1, ConfigGeneration: current.ConfigGeneration},
		"config_generation": {LogLines: current.LogLines, ConfigGeneration: current.ConfigGeneration + 1},
		"both":              {LogLines: current.LogLines - 1, ConfigGeneration: current.ConfigGeneration - 1},
	} {
		t.Run(name, func(t *testing.T) {
			r := mustReject(t, env, command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day2)}, ExpectedVersion: &v}, command.ReasonStalePreview)
			if r.Reason.Status() != 409 {
				t.Errorf("status %d, want 409", r.Reason.Status())
			}
			mustReject(t, env, command.ChangeProfile{Profile: "on-call", ExpectedVersion: &v}, command.ReasonStalePreview)
		})
	}
	t.Run("the current version is accepted", func(t *testing.T) {
		mustPlan(t, env, command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day2)}, ExpectedVersion: &current})
		mustPlan(t, env, command.ChangeProfile{Profile: "on-call", ExpectedVersion: &current})
	})
}

func TestMatchesNoCivilDateReported(t *testing.T) {
	cfg := loadConfig(t, func(c map[string]any) {
		c["tasks"].(map[string]any)["capacity-check"].(map[string]any)["due"].(map[string]any)["day"] = 15
	})
	env := emptyEnv(t, cfg, at(-60))
	c := command.ChangePeriods{DryRun: true, Changes: []command.PeriodChange{dayTo(day1), weekFrom(week1), sprintFrom(sprint1)}}
	p := mustPlan(t, env, c)
	if p.Preview == nil {
		t.Fatal("the dry run has no preview")
	}
	if len(p.Preview.NotMaterialized) != 1 || p.Preview.NotMaterialized[0].Definition != "capacity-check" {
		t.Fatalf("NotMaterialized = %+v, want capacity-check", p.Preview.NotMaterialized)
	}
	if reason := p.Preview.NotMaterialized[0].Reason; !strings.Contains(reason, "day 15") {
		t.Errorf("reason %q does not say why", reason)
	}
	for _, id := range taskIDsOf[event.TaskMaterialized](p) {
		if strings.HasSuffix(string(id), ":capacity-check") {
			t.Errorf("%s is materialized", id)
		}
	}
	for _, ref := range p.Preview.Materialize {
		if strings.HasSuffix(string(ref.ID), ":capacity-check") {
			t.Errorf("%s is listed to materialize", ref.ID)
		}
	}
	if got := payloadsOf[event.PeriodChanged](p); len(got) != 3 {
		t.Errorf("period changes = %d, want the sprint's too", len(got))
	}
}

func TestPeriodChangePreviewListsEveryPart(t *testing.T) {
	env, _ := begun(t, withLight(t))
	env.Now = at(24 * 60)
	env.Version = command.Version{LogLines: env.Model.Lines(), ConfigGeneration: 5}
	p := mustPlan(t, env, command.ChangePeriods{DryRun: true, Changes: []command.PeriodChange{dayTo(day2)}, Profile: "light"})
	pv := p.Preview
	if pv == nil {
		t.Fatal("no preview")
	}
	refs := func(rs []command.TaskRef) []event.TaskID {
		var out []event.TaskID
		for _, r := range rs {
			out = append(out, r.ID)
		}
		return out
	}
	if got := refs(pv.Leaving); !sameIDs(got, planDay, postPlan, endOfDay) {
		t.Errorf("Leaving = %v", got)
	}
	for _, r := range pv.Leaving {
		if r.Title == "" || !r.Overdue {
			t.Errorf("leaving %+v: want its title and overdue, the day being over", r)
		}
	}
	if got := refs(pv.Materialize); !sameIDs(got, event.NewTaskID(due.Daily, day2, "plan-day")) {
		t.Errorf("Materialize = %v", got)
	}
	if got := refs(pv.ProfileAdd); !sameIDs(got, weeklyReview, sprintRetro) {
		t.Errorf("ProfileAdd = %v", got)
	}
	if got := refs(pv.ProfileWithdraw); !sameIDs(got, weeklyUpdate, capacity) {
		t.Errorf("ProfileWithdraw = %v", got)
	}
	if len(pv.ProfileReinstate) != 0 || len(pv.NotMaterialized) != 0 || len(pv.BlockingCycles) != 0 {
		t.Errorf("preview %+v lists more than it should", pv)
	}
	if pv.Version != env.Version {
		t.Errorf("Version = %+v, want %+v", pv.Version, env.Version)
	}
}

func TestProfileChangePreviewFlagsOverdue(t *testing.T) {
	env, _ := begun(t, withLight(t))
	env.Now = at(60)
	env = then(t, env, mustPlan(t, env, command.ChangeProfile{Profile: "light"}), at(600)) // 18:00, after the 17:30 due
	p := mustPlan(t, env, command.ChangeProfile{DryRun: true, Profile: "normal"})
	if p.Preview == nil {
		t.Fatal("no preview")
	}
	flags := map[event.TaskID]bool{}
	for _, r := range p.Preview.ProfileReinstate {
		flags[r.ID] = r.Overdue
	}
	// At 18:00 on day1, post-plan (due 09:30), end-of-day-summary (17:30) and
	// the capacity check (09:00 on the sprint's first day) are past due; the
	// weekly update is due the next morning.
	want := map[event.TaskID]bool{postPlan: true, endOfDay: true, capacity: true, weeklyUpdate: false}
	if !reflect.DeepEqual(flags, want) {
		t.Errorf("reinstated with overdue flags %v, want %v", flags, want)
	}
	for _, r := range p.Preview.ProfileWithdraw {
		if r.Overdue {
			t.Errorf("%s is flagged overdue before it is due", r.ID)
		}
	}
}

func TestRolloverRejectedWhileACycleIsActive(t *testing.T) {
	now := at(24 * 60)
	backdated := at(12 * 60) // 20:00 on day1
	tests := []struct {
		name    string
		log     func(b *logb)
		eff     *time.Time
		blocked []event.CycleID
	}{
		{"a running cycle blocks", func(b *logb) { b.add(0, startOf(cycleA, deepWork)) }, nil, []event.CycleID{cycleA}},
		{"a paused cycle blocks", func(b *logb) {
			b.add(0, startOf(cycleA, deepWork))
			b.add(10, pauseOf(cycleA))
		}, nil, []event.CycleID{cycleA}},
		{"a stopped cycle does not", func(b *logb) {
			b.add(0, startOf(cycleA, deepWork))
			b.add(10, stopOf(cycleA))
		}, nil, nil},
		{"two active cycles are both named", func(b *logb) {
			b.add(0, startOf(cycleA, deepWork))
			b.add(10, interruptOf(cycleB, cycleA, review))
		}, nil, []event.CycleID{cycleA, cycleB}},
		{"a backdated rollover is blocked by a cycle active now", func(b *logb) {
			b.add(23*60, startOf(cycleA, deepWork))
		}, &backdated, []event.CycleID{cycleA}},
		{"a cycle that ran across the backdated instant and is stopped now does not block", func(b *logb) {
			b.add(11*60, startOf(cycleA, deepWork))
			b.add(17*60, stopOf(cycleA))
		}, &backdated, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := bootstrapped(t)
			tc.log(b)
			env := envOf(t, b, now)
			c := command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day2)}, EffectiveAt: tc.eff}
			if tc.blocked == nil {
				mustPlan(t, env, c)
				return
			}
			r := mustReject(t, env, c, command.ReasonCycleActive)
			if r.Reason.Status() != 409 || !sameIDs(cycleIDsOf(r), tc.blocked...) {
				t.Errorf("status %d, Cycles %v, want 409 and %v", r.Reason.Status(), cycleIDsOf(r), tc.blocked)
			}
			for _, ref := range r.Cycles {
				c, _ := env.Model.Cycle(ref.ID)
				if ref.Title == "" || ref.Status != c.Status || !ref.StartedAt.Equal(c.Segments[0].Start) {
					t.Errorf("cycle ref %+v, want the title, status %s and start %v", ref, c.Status, c.Segments[0].Start)
				}
				for _, want := range []string{string(ref.ID), ref.Title, string(ref.Status)} {
					if !strings.Contains(r.Message, want) {
						t.Errorf("Message %q does not mention %q", r.Message, want)
					}
				}
			}
		})
	}

	t.Run("a cycle forgotten overnight is stopped at an earlier time, then the rollover is accepted", func(t *testing.T) {
		b := bootstrapped(t)
		b.add(8*60, startOf(cycleA, deepWork)) // 16:00 on day1, still running the next morning
		env := envOf(t, b, now)
		roll := command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day2)}, EffectiveAt: &backdated}
		mustReject(t, env, roll, command.ReasonCycleActive)
		stop := mustPlan(t, env, command.StopCycle{CycleID: cycleA, EffectiveAt: ptr(at(9 * 60))})
		env = then(t, env, stop, now)
		p := mustPlan(t, env, roll)
		for _, e := range p.Events {
			if strings.HasPrefix(string(e.Type), "cycle.") {
				t.Errorf("the rollover adds %s", e.Type)
			}
		}
	})
	t.Run("bootstrap is blocked the same way", func(t *testing.T) {
		env := emptyEnv(t, loadConfig(t, nil), at(10))
		env = extend(t, env, at(0), startOf(cycleA, deepWork))
		r := mustReject(t, env, command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day1)}}, command.ReasonCycleActive)
		if !sameIDs(cycleIDsOf(r), cycleA) {
			t.Errorf("Cycles = %v, want %s", cycleIDsOf(r), cycleA)
		}
	})
}

func TestDryRunListsBlockingCyclesAndDoesNotFail(t *testing.T) {
	b := bootstrapped(t)
	b.add(0, startOf(cycleA, deepWork))
	b.add(10, interruptOf(cycleB, cycleA, review))
	env := envOf(t, b, at(24*60))
	c := command.ChangePeriods{DryRun: true, Changes: []command.PeriodChange{dayTo(day2)}}
	p := mustPlan(t, env, c)
	if p.Preview == nil {
		t.Fatal("no preview")
	}
	got := p.Preview.BlockingCycles
	if len(got) != 2 || got[0].ID != cycleA || got[0].Status != projection.Paused || got[1].ID != cycleB || got[1].Status != projection.Running {
		t.Errorf("BlockingCycles = %+v, want A paused and B running", got)
	}
	if got[0].Title != deepWorkTitle || got[1].Title != reviewTitle || !got[0].StartedAt.Equal(at(0)) {
		t.Errorf("BlockingCycles = %+v, want each with its title and start", got)
	}
	if len(p.Events) == 0 || len(p.Preview.Leaving) != 2 || len(p.Preview.Materialize) != 3 {
		t.Errorf("the dry run is not the full preview: %d events, %+v", len(p.Events), p.Preview)
	}
	c.DryRun = false
	mustReject(t, env, c, command.ReasonCycleActive)
}

func TestStoppedCycleIsUnaffectedByPeriodChange(t *testing.T) {
	b := bootstrapped(t)
	b.add(0, startOf(cycleA, deepWork))
	b.add(30, stopOf(cycleA))
	env := envOf(t, b, at(24*60))
	p := mustPlan(t, env, command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day2)}})
	for _, e := range p.Events {
		if strings.HasPrefix(string(e.Type), "cycle.") {
			t.Errorf("the plan has %s", e.Type)
		}
	}
	before, _ := env.Model.Cycle(cycleA)
	after, _ := p.Candidate.Cycle(cycleA)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("cycle %+v became %+v", before, after)
	}
	for _, v := range []any{command.ChangePeriods{}, command.PeriodChange{}, command.Preview{}, command.CycleRef{}} {
		typ := reflect.TypeOf(v)
		for i := range typ.NumField() {
			name := strings.ToLower(typ.Field(i).Name)
			for _, banned := range []string{"prefill", "endat", "stopat", "stop"} {
				if strings.Contains(name, banned) {
					t.Errorf("%s has the field %s", typ.Name(), typ.Field(i).Name)
				}
			}
		}
	}
}
