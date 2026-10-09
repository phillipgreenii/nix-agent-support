package command_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
)

var (
	endOfDay     = event.NewTaskID(due.Daily, day1, "end-of-day-summary")
	weeklyUpdate = event.NewTaskID(due.Weekly, week1, "weekly-update")
	weeklyReview = event.NewTaskID(due.Weekly, week1, "weekly-review")
	capacity     = event.NewTaskID(due.Sprint, sprint1, "capacity-check")
	sprintRetro  = event.NewTaskID(due.Sprint, sprint1, "sprint-retro")
)

// withLight is the example configuration plus the profile "light", which
// lists plan-day, a weekly review and a sprint retrospective, the two new
// definitions.
func withLight(t *testing.T) *config.Config {
	t.Helper()
	return loadConfig(t, func(c map[string]any) {
		c["profiles"].(map[string]any)["light"] = map[string]any{
			"daily": []any{"plan-day"}, "weekly": []any{"weekly-review"}, "sprint": []any{"sprint-retro"}, "cycles": []any{"review"},
		}
		tasks := c["tasks"].(map[string]any)
		tasks["weekly-review"] = map[string]any{
			"title": "Review the week", "cadence": "weekly",
			"due": map[string]any{"weekday": "fri", "at": "15:00", "tz": newYork},
		}
		tasks["sprint-retro"] = map[string]any{
			"title": "Hold the sprint retrospective", "cadence": "sprint",
			"due": map[string]any{"day": 10, "at": "15:00", "tz": newYork},
		}
	})
}

func TestProfileInBatchAppliesToRemainingPeriodsOnly(t *testing.T) {
	env, _ := begun(t, withLight(t))
	env.Now = at(24 * 60)
	p := mustPlan(t, env, command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day2)}, Profile: "light"})
	checkBatch(t, p, at(24*60))

	want := []event.Type{
		event.TypeTaskMissed, event.TypeTaskMissed, event.TypeTaskMissed,
		event.TypeProfileChanged,
		event.TypeTaskWithdrawn, event.TypeTaskMaterialized, // the week
		event.TypeTaskWithdrawn, event.TypeTaskMaterialized, // the sprint
		event.TypePeriodChanged, event.TypeTaskMaterialized, // the new day, from the new profile
		event.TypeBatchCommitted,
	}
	if got := typesOf(p); !slices.Equal(got, want) {
		t.Fatalf("types = %v, want %v", got, want)
	}
	if got := taskIDsOf[event.TaskMissed](p); !sameIDs(got, planDay, postPlan, endOfDay) {
		t.Errorf("missed = %v, want the day's three tasks", got)
	}
	if got := p.Events[3].Payload.(event.ProfileChanged).Profile; got != "light" {
		t.Errorf("profile.changed = %q, want light", got)
	}
	if got := taskIDsOf[event.TaskWithdrawn](p); !sameIDs(got, weeklyUpdate, capacity) {
		t.Errorf("withdrawn = %v, want the week's and the sprint's tasks the new profile drops", got)
	}
	newDay := event.NewTaskID(due.Daily, day2, "plan-day")
	if got := taskIDsOf[event.TaskMaterialized](p); !sameIDs(got, weeklyReview, sprintRetro, newDay) {
		t.Errorf("materialized = %v, want the week's and sprint's additions and the new day's plan-day alone", got)
	}
	if p.Candidate.Profile() != "light" {
		t.Errorf("profile = %q, want light", p.Candidate.Profile())
	}
	for _, id := range []event.TaskID{planDay, postPlan, endOfDay} {
		if got := taskStatus(t, p.Candidate, id); got != projection.Missed {
			t.Errorf("%s = %s: the profile change touched the day being left", id, got)
		}
	}
}

func TestChangeProfileAddsWithdrawsReinstates(t *testing.T) {
	env, _ := begun(t, withLight(t))
	env = extend(t, env, at(10), event.TaskCompleted{TaskID: postPlan})
	env = extend(t, env, at(20), event.TaskSkipped{TaskID: weeklyUpdate, Reason: "nothing to report"})
	marker := idOf('B', 1)
	env = extend(t, env, at(30), event.TaskMissed{TaskID: capacity, Batch: marker}, event.BatchCommitted{Batch: marker})
	env.Now = at(60)

	toLight := mustPlan(t, env, command.ChangeProfile{Profile: "light"})
	checkBatch(t, toLight, at(60))
	if got := typesOf(toLight); got[0] != event.TypeProfileChanged {
		t.Errorf("the batch begins with %s, want profile.changed", got[0])
	}
	if got := taskIDsOf[event.TaskWithdrawn](toLight); !sameIDs(got, endOfDay) {
		t.Errorf("withdrawn = %v, want the open end-of-day task alone", got)
	}
	if got := taskIDsOf[event.TaskMaterialized](toLight); !sameIDs(got, weeklyReview, sprintRetro) {
		t.Errorf("materialized = %v, want the two additions", got)
	}
	if got := taskIDsOf[event.TaskReinstated](toLight); len(got) != 0 {
		t.Errorf("reinstated = %v, want none", got)
	}
	for id, want := range map[event.TaskID]projection.TaskStatus{
		postPlan: projection.Completed, weeklyUpdate: projection.Skipped, capacity: projection.Missed,
		planDay: projection.Open, endOfDay: projection.Withdrawn,
	} {
		if got := taskStatus(t, toLight.Candidate, id); got != want {
			t.Errorf("%s = %s, want %s", id, got, want)
		}
	}

	env = then(t, env, toLight, at(90))
	back := mustPlan(t, env, command.ChangeProfile{Profile: "normal"})
	if got := taskIDsOf[event.TaskReinstated](back); !sameIDs(got, endOfDay) {
		t.Errorf("reinstated = %v, want the withdrawn end-of-day task", got)
	}
	if got := taskIDsOf[event.TaskMaterialized](back); len(got) != 0 {
		t.Errorf("materialized = %v: a withdrawn task is reinstated, never materialized again", got)
	}
	if got := taskIDsOf[event.TaskWithdrawn](back); !sameIDs(got, weeklyReview, sprintRetro) {
		t.Errorf("withdrawn = %v, want the light profile's additions", got)
	}
	if got := taskStatus(t, back.Candidate, endOfDay); got != projection.Open {
		t.Errorf("the reinstated task is %s, want open", got)
	}

	t.Run("an unknown profile", func(t *testing.T) {
		r := mustReject(t, env, command.ChangeProfile{Profile: "nothing"}, command.ReasonUnknownProfile)
		if r.Reason.Status() != 400 || !strings.Contains(r.Message, "nothing") {
			t.Errorf("status %d, message %q", r.Reason.Status(), r.Message)
		}
		mustReject(t, env, command.ChangePeriods{Changes: []command.PeriodChange{dayTo(day2)}, Profile: "nothing"}, command.ReasonUnknownProfile)
	})
	t.Run("no profile", func(t *testing.T) {
		mustReject(t, env, command.ChangeProfile{}, command.ReasonInvalidRequest)
	})
}

func TestProfileChangeWithdrawsTasksWhoseDefinitionVanished(t *testing.T) {
	env, _ := begun(t, loadConfig(t, nil))
	env = extend(t, env, at(10), event.TaskCompleted{TaskID: planDay})
	// A reload drops post-plan and plan-day from the configuration.
	env.Config = loadConfig(t, func(c map[string]any) {
		for _, name := range []string{"normal", "on-call"} {
			c["profiles"].(map[string]any)[name].(map[string]any)["daily"] = []any{"end-of-day-summary"}
		}
		delete(c["tasks"].(map[string]any), "post-plan")
		delete(c["tasks"].(map[string]any), "plan-day")
	})
	env.Now = at(20)
	p := mustPlan(t, env, command.ChangeProfile{Profile: "normal"})
	if got := taskIDsOf[event.TaskWithdrawn](p); !sameIDs(got, postPlan) {
		t.Errorf("withdrawn = %v, want the open task whose definition vanished", got)
	}
	if got := taskIDsOf[event.TaskMaterialized](p); len(got) != 0 {
		t.Errorf("materialized = %v, want none", got)
	}
	if got := taskStatus(t, p.Candidate, planDay); got != projection.Completed {
		t.Errorf("the completed task is %s, want it untouched", got)
	}
}
