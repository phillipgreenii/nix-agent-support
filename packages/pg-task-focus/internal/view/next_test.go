package view_test

import (
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/testutil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/view"
)

var (
	weekStart = civil.Date{Year: 2026, Month: time.October, Day: 5}
	weekEnd   = civil.Date{Year: 2026, Month: time.October, Day: 11}
)

// withWeek is a log whose first batch sets the profile, the day and the week
// (both in New York) and then materializes the tasks the callers list.
func withWeek(t *testing.T, tasks ...func(event.ID) event.Payload) *logb {
	t.Helper()
	b := newLog(t)
	b.batch(t0, append([]func(event.ID) event.Payload{
		profileOf("normal"), dayIn(day1, newYork), periodOf("week", weekStart, &weekEnd, newYork),
	}, tasks...)...)
	return b
}

func taskTitles(st view.State) []string {
	var out []string
	for _, tv := range st.Tasks {
		out = append(out, tv.Task.Definition)
	}
	return out
}

func TestTaskOrdering(t *testing.T) {
	// group_order in the example configuration: Start of day, During the day,
	// End of day. "Other" is listed nowhere and a task may have no group.
	build := func(t *testing.T) *logb {
		return withWeek(
			t,
			taskOf("closing", due.Daily, day1, "End of day", at(530)),
			taskOf("stray", due.Daily, day1, "Other", at(10)),
			taskOf("later-start", due.Daily, day1, "Start of day", at(90)),
			taskOf("midday", due.Daily, day1, "During the day", at(240)),
			taskOf("early-start", due.Daily, day1, "Start of day", at(60)),
			taskOf("no-group", due.Daily, day1, "", at(5)),
			taskOf("weekly-late", due.Weekly, weekStart, "", at(3000)),
			taskOf("weekly-early", due.Weekly, weekStart, "Start of day", at(2000)),
		)
	}

	t.Run("by kind, then group rank, then due time, unlisted groups last", func(t *testing.T) {
		st := view.Build(build(t).model(), testutil.LoadConfig(t, nil), at(20))
		want := []string{
			"early-start", "later-start", "midday", "closing", "no-group", "stray", // day: listed groups by rank, then the unlisted and the empty group by due time
			"weekly-early", "weekly-late", // week
		}
		if got := taskTitles(st); !slices.Equal(got, want) {
			t.Errorf("task order = %v, want %v", got, want)
		}
		for _, tv := range st.Tasks {
			wantKind := projection.Day
			if tv.Task.Cadence == due.Weekly {
				wantKind = projection.Week
			}
			if tv.Kind != wantKind {
				t.Errorf("task %s Kind = %s, want %s", tv.Task.Definition, tv.Kind, wantKind)
			}
		}
		rank := map[string]int{}
		for _, tv := range st.Tasks {
			rank[tv.Task.Definition] = tv.GroupRank
		}
		if rank["stray"] != 3 || rank["no-group"] != 3 || rank["early-start"] != 0 || rank["closing"] != 2 {
			t.Errorf("GroupRank = %v, want 0 Start of day, 2 End of day and 3 for the unlisted and the empty group", rank)
		}
	})

	t.Run("the order follows the group_order of the configuration now", func(t *testing.T) {
		cfg := testutil.LoadConfig(t, func(c map[string]any) {
			c["group_order"] = []any{"End of day", "Start of day"}
		})
		st := view.Build(build(t).model(), cfg, at(20))
		want := []string{"closing", "early-start", "later-start", "no-group", "stray", "midday", "weekly-early", "weekly-late"}
		if got := taskTitles(st); !slices.Equal(got, want) {
			t.Errorf("task order = %v, want %v", got, want)
		}
	})

	t.Run("tasks that tie keep the order they were materialized in", func(t *testing.T) {
		b := withWeek(
			t,
			taskOf("second-listed-first", due.Daily, day1, "Start of day", at(60)),
			taskOf("first-listed-second", due.Daily, day1, "Start of day", at(60)),
		)
		st := view.Build(b.model(), testutil.LoadConfig(t, nil), at(20))
		if got, want := taskTitles(st), []string{"second-listed-first", "first-listed-second"}; !slices.Equal(got, want) {
			t.Errorf("task order = %v, want %v", got, want)
		}
	})
}

func TestTasksListOnlyTheCurrentPeriodOfEachKind(t *testing.T) {
	day2 := day1.AddDays(1)
	b := withWeek(t, taskOf("plan", due.Daily, day1, "Start of day", at(60)))
	b.batch(
		at(1440),
		func(bt event.ID) event.Payload { return event.TaskMissed{TaskID: dailyID("plan"), Batch: bt} },
		dayIn(day2, newYork),
		taskOf("plan", due.Daily, day2, "Start of day", at(1500)),
	)
	st := view.Build(b.model(), testutil.LoadConfig(t, nil), at(1450))
	if len(st.Tasks) != 1 || st.Tasks[0].Task.PeriodStart != day2 {
		t.Errorf("Tasks = %v, want only the task of the current day: nothing is carried over", taskTitles(st))
	}
}

func TestNextTiesBrokenByGroup(t *testing.T) {
	cfg := testutil.LoadConfig(t, nil)

	t.Run("the lower group rank wins a tie on the due time", func(t *testing.T) {
		b := withWeek(
			t,
			taskOf("closing", due.Daily, day1, "End of day", at(60)),
			taskOf("opening", due.Daily, day1, "Start of day", at(60)),
			taskOf("stray", due.Daily, day1, "Other", at(60)),
		)
		st := view.Build(b.model(), cfg, at(20))
		if st.Next == nil || st.Next.Task.Definition != "opening" {
			t.Fatalf("Next = %+v, want opening", st.Next)
		}
	})

	t.Run("an unlisted group loses the tie", func(t *testing.T) {
		b := withWeek(
			t,
			taskOf("stray", due.Daily, day1, "Other", at(60)),
			taskOf("closing", due.Daily, day1, "End of day", at(60)),
		)
		if st := view.Build(b.model(), cfg, at(20)); nextOf(st).Task.Definition != "closing" {
			t.Errorf("Next = %s, want closing", nextOf(st).Task.Definition)
		}
	})

	t.Run("the earliest due time wins across every kind, whatever the group", func(t *testing.T) {
		b := withWeek(
			t,
			taskOf("daily", due.Daily, day1, "Start of day", at(300)),
			taskOf("weekly", due.Weekly, weekStart, "", at(120)),
		)
		st := view.Build(b.model(), cfg, at(20))
		if st.Next == nil || st.Next.Task.Definition != "weekly" || st.Next.Kind != projection.Week {
			t.Errorf("Next = %+v, want the weekly task due first", st.Next)
		}
	})

	t.Run("a tie on due time and group goes to the task listed first", func(t *testing.T) {
		b := withWeek(
			t,
			taskOf("one", due.Daily, day1, "Start of day", at(60)),
			taskOf("two", due.Weekly, weekStart, "Start of day", at(60)),
		)
		if st := view.Build(b.model(), cfg, at(20)); nextOf(st).Task.Definition != "one" {
			t.Errorf("Next = %s, want one: the day is listed before the week", nextOf(st).Task.Definition)
		}
	})
}

func TestNextIgnoresResolvedAndWithdrawn(t *testing.T) {
	b := withWeek(
		t,
		taskOf("done", due.Daily, day1, "Start of day", at(30)),
		taskOf("skipped", due.Daily, day1, "Start of day", at(40)),
		taskOf("missed", due.Daily, day1, "Start of day", at(50)),
		taskOf("withdrawn", due.Daily, day1, "Start of day", at(55)),
		taskOf("open", due.Daily, day1, "End of day", at(600)),
	)
	b.add(at(5), event.TaskCompleted{TaskID: dailyID("done")})
	b.add(at(6), event.TaskSkipped{TaskID: dailyID("skipped"), Reason: "Not needed today"})
	b.batch(
		at(7),
		func(bt event.ID) event.Payload { return event.TaskMissed{TaskID: dailyID("missed"), Batch: bt} },
		func(bt event.ID) event.Payload { return event.TaskWithdrawn{TaskID: dailyID("withdrawn"), Batch: bt} },
	)
	m, cfg := b.model(), testutil.LoadConfig(t, nil)

	st := view.Build(m, cfg, at(20))
	if st.Next == nil || st.Next.Task.Definition != "open" {
		t.Fatalf("Next = %+v, want the only open task", st.Next)
	}
	wantStatus := map[string]projection.TaskStatus{
		"done": projection.Completed, "skipped": projection.Skipped, "missed": projection.Missed,
		"withdrawn": projection.Withdrawn, "open": projection.Open,
	}
	for _, tv := range st.Tasks {
		if tv.Task.Status != wantStatus[tv.Task.Definition] {
			t.Errorf("task %s status = %s, want %s", tv.Task.Definition, tv.Task.Status, wantStatus[tv.Task.Definition])
		}
	}

	b.add(at(21), event.TaskCompleted{TaskID: dailyID("open")})
	if st := view.Build(b.model(), cfg, at(22)); st.Next != nil {
		t.Errorf("Next = %v, want none once no task is open", st.Next.Task.Definition)
	}
}

func TestOverdueFlag(t *testing.T) {
	// Due at 09:30 New York, which is at(90).
	b := withWeek(
		t,
		taskOf("pending", due.Daily, day1, "Start of day", at(90)),
		taskOf("finished", due.Daily, day1, "Start of day", at(90)),
	)
	b.add(at(10), event.TaskCompleted{TaskID: dailyID("finished")})
	m, cfg := b.model(), testutil.LoadConfig(t, nil)

	tests := []struct {
		name        string
		now         time.Time
		wantPending bool
	}{
		{"well before the due time", at(30), false},
		{"a millisecond before the due time", at(90).Add(-time.Millisecond), false},
		{"at the due time", at(90), false},
		{"a millisecond after the due time", at(90).Add(time.Millisecond), true},
		{"twelve minutes after", at(102), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := view.Build(m, cfg, tt.now)
			for _, tv := range st.Tasks {
				want := tt.wantPending && tv.Task.Definition == "pending"
				if tv.Overdue != want {
					t.Errorf("task %s Overdue = %v, want %v", tv.Task.Definition, tv.Overdue, want)
				}
			}
			if st.Next == nil || st.Next.Overdue != tt.wantPending {
				t.Errorf("Next = %+v, want it flagged overdue = %v", st.Next, tt.wantPending)
			}
		})
	}
}
