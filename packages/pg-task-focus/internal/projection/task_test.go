package projection

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden logs under testdata/logs")

var (
	day1 = civil.Date{Year: 2026, Month: time.October, Day: 7}
	day2 = day1.AddDays(1)
)

// taskIn is the materialization of definition def for the period starting on
// period.
func (b *logb) taskIn(def string, c due.Cadence, period civil.Date) func(event.ID) event.Payload {
	z := newZone(b.t, "America/New_York")
	return func(bt event.ID) event.Payload {
		return event.TaskMaterialized{
			TaskID: event.NewTaskID(c, period, def), Definition: def, Cadence: c, Period: period,
			Title: "Task " + def, Link: exampleLink, Due: event.At(at(60)),
			DueRule: due.Rule{At: civil.TimeOfDay{Hour: 9, Minute: 30}, TZ: z}, Batch: bt,
		}
	}
}

func (b *logb) daily(def string, period civil.Date) func(event.ID) event.Payload {
	return b.taskIn(def, due.Daily, period)
}

func missedTask(id event.TaskID) func(event.ID) event.Payload {
	return func(bt event.ID) event.Payload { return event.TaskMissed{TaskID: id, Batch: bt} }
}

func withdrawnTask(id event.TaskID) func(event.ID) event.Payload {
	return func(bt event.ID) event.Payload { return event.TaskWithdrawn{TaskID: id, Batch: bt} }
}

func reinstatedTask(id event.TaskID) func(event.ID) event.Payload {
	return func(bt event.ID) event.Payload { return event.TaskReinstated{TaskID: id, Batch: bt} }
}

func skippedTask(id event.TaskID, batch bool) func(event.ID) event.Payload {
	return func(bt event.ID) event.Payload {
		if !batch {
			bt = ""
		}
		return event.TaskSkipped{TaskID: id, Reason: "not needed", Batch: bt}
	}
}

// bootstrapped is a log whose first batch sets the profile and the day, and
// whose second materializes the daily task post-plan at 10 minutes.
func bootstrapped(t *testing.T) (*logb, event.TaskID) {
	t.Helper()
	b := newLog(t)
	b.batch(0, profileOf("work"), dayOf(day1))
	b.batch(10, b.daily("post-plan", day1))
	return b, taskA
}

func mustReplay(t *testing.T, events []event.Event) *Model {
	t.Helper()
	m, err := Replay(events)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	return m
}

func mustTask(t *testing.T, m *Model, id event.TaskID) Task {
	t.Helper()
	task, ok := m.Task(id)
	if !ok {
		t.Fatalf("Task(%s) not found", id)
	}
	return task
}

func TestTaskStatusMatrix(t *testing.T) {
	// The task is materialized at 10 minutes. A missed or withdrawn marker is
	// at 100, a reinstatement at 200; a resolution is at 50 (before the
	// marker), 150 (between the withdrawal and the reinstatement) or 250.
	const (
		before  = 50
		between = 150
		after   = 250
	)
	type markers struct{ missed, withdrawn, reinstated bool }
	none, missedOnly := markers{}, markers{missed: true}
	withdrawn := markers{withdrawn: true}
	reinstated := markers{withdrawn: true, reinstated: true}
	missedWithdrawn := markers{missed: true, withdrawn: true}
	missedReinstated := markers{missed: true, withdrawn: true, reinstated: true}

	tests := []struct {
		name    string
		markers markers
		res     string // "", "completed" or "skipped"
		resAt   int
		want    TaskStatus
		code    Code // when the log is impossible
	}{
		{"nothing", none, "", 0, Open, ""},
		{"completed", none, "completed", after, Completed, ""},
		{"skipped", none, "skipped", after, Skipped, ""},

		{"missed", missedOnly, "", 0, Missed, ""},
		{"missed then completed late", missedOnly, "completed", after, Completed, ""},
		{"completed before it was missed", missedOnly, "completed", before, Completed, ""},
		{"missed then skipped late", missedOnly, "skipped", after, Skipped, ""},
		{"skipped before it was missed", missedOnly, "skipped", before, Skipped, ""},

		{"withdrawn", withdrawn, "", 0, Withdrawn, ""},
		{"completed before the withdrawal", withdrawn, "completed", before, Completed, ""},
		{"completed after the withdrawal", withdrawn, "completed", after, "", codeTaskWithdrawn},
		{"skipped before the withdrawal", withdrawn, "skipped", before, Skipped, ""},
		{"skipped after the withdrawal", withdrawn, "skipped", after, "", codeTaskWithdrawn},

		{"withdrawn then reinstated", reinstated, "", 0, Open, ""},
		{"completed before the withdrawal, reinstated", reinstated, "completed", before, Completed, ""},
		{"completed while withdrawn", reinstated, "completed", between, "", codeTaskWithdrawn},
		{"completed after the reinstatement", reinstated, "completed", after, Completed, ""},
		{"skipped before the withdrawal, reinstated", reinstated, "skipped", before, Skipped, ""},
		{"skipped while withdrawn", reinstated, "skipped", between, "", codeTaskWithdrawn},
		{"skipped after the reinstatement", reinstated, "skipped", after, Skipped, ""},

		{"missed and withdrawn: the withdrawal wins", missedWithdrawn, "", 0, Withdrawn, ""},
		{"missed, withdrawn, completed late", missedWithdrawn, "completed", after, "", codeTaskWithdrawn},
		{"missed, withdrawn, reinstated: missed again", missedReinstated, "", 0, Missed, ""},
		{"missed, withdrawn, reinstated, completed", missedReinstated, "completed", after, Completed, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, id := bootstrapped(t)
			if tt.markers.missed {
				b.batch(100, missedTask(id))
			}
			if tt.markers.withdrawn {
				b.batch(100, withdrawnTask(id))
			}
			if tt.markers.reinstated {
				b.batch(200, reinstatedTask(id))
			}
			switch tt.res {
			case "completed":
				b.add(tt.resAt, event.TaskCompleted{TaskID: id})
			case "skipped":
				b.add(tt.resAt, skippedTask(id, false)(""))
			}

			m, err := Replay(b.events)
			if tt.code != "" {
				inv := asInvalid(t, err)
				if inv.Code != tt.code || inv.Entity != string(id) {
					t.Errorf("Replay: code %q entity %q (%s), want %q for %s", inv.Code, inv.Entity, inv.Message, tt.code, id)
				}
				return
			}
			if err != nil {
				t.Fatalf("Replay: %v", err)
			}
			task := mustTask(t, m, id)
			if task.Status != tt.want {
				t.Errorf("Status = %q, want %q", task.Status, tt.want)
			}
			switch tt.want {
			case Completed, Skipped:
				if task.ResolvedAt == nil || !task.ResolvedAt.Equal(at(tt.resAt)) {
					t.Errorf("ResolvedAt = %v, want %s", task.ResolvedAt, at(tt.resAt))
				}
				if (tt.want == Skipped) != (task.Reason == "not needed") {
					t.Errorf("Reason = %q for status %q", task.Reason, task.Status)
				}
			default:
				if task.ResolvedAt != nil || task.Reason != "" {
					t.Errorf("ResolvedAt = %v, Reason = %q, want none for status %q: missed and withdrawn are not resolutions",
						task.ResolvedAt, task.Reason, task.Status)
				}
			}
		})
	}
}

func TestTaskCarriesTheMaterializationSnapshot(t *testing.T) {
	b, id := bootstrapped(t)
	m := mustReplay(t, b.events)
	task := mustTask(t, m, id)
	if task.ID != id || task.Definition != "post-plan" || task.Cadence != due.Daily || task.PeriodStart != day1 ||
		task.Title != "Task post-plan" || task.Link != exampleLink || task.Group != "" ||
		!task.Due.Equal(at(60)) || task.DueRule.At != (civil.TimeOfDay{Hour: 9, Minute: 30}) ||
		task.DueRule.TZ.Name() != "America/New_York" || task.Status != Open {
		t.Errorf("Task = %+v", task)
	}
	if _, ok := m.Task("day:2026-10-07:unknown"); ok {
		t.Error("Task(unknown) found a task")
	}

	b.batch(20, b.daily("wrap-up", day1))
	tasks := mustReplay(t, b.events).Tasks()
	got := make([]event.TaskID, len(tasks))
	for i, task := range tasks {
		got[i] = task.ID
	}
	if want := []event.TaskID{id, "day:2026-10-07:wrap-up"}; !slices.Equal(got, want) {
		t.Errorf("Tasks() = %v, want %v in materialization order", got, want)
	}
	tasks[0].Title = "changed"
	if mustTask(t, mustReplay(t, b.events), id).Title != "Task post-plan" {
		t.Error("Tasks() hands out the model's own storage")
	}
}

// rollover appends the rollover batch that leaves day1 for day2 at 875
// minutes (00:05 in New York on the 8th), marking the task missed, and
// materializes the day2 task.
// rollover rolls the day over to day2 at 875: id is missed and day2's
// post-plan, due a day after day1's, is materialized.
func rollover(b *logb, id event.TaskID) {
	next := func(bt event.ID) event.Payload {
		p := b.daily("post-plan", day2)(bt).(event.TaskMaterialized)
		p.Due = event.At(p.Due.Time().Add(24 * time.Hour))
		return p
	}
	b.batch(875, missedTask(id), dayOf(day2), next)
}

func TestLateCompletionOfMissedTask(t *testing.T) {
	b, id := bootstrapped(t)
	rollover(b, id)
	newID := event.NewTaskID(due.Daily, day2, "post-plan")
	const sixteenHundredYesterday = 390 // 20:00 UTC on the 7th is 16:00 in New York

	t.Run("the old task is completed and its missed marker is superseded", func(t *testing.T) {
		late := slices.Clone(b.events)
		lateB := &logb{t: t, events: late, batches: b.batches}
		lateB.add(sixteenHundredYesterday, event.TaskCompleted{TaskID: id})
		m := mustReplay(t, lateB.events)
		task := mustTask(t, m, id)
		if task.Status != Completed || task.ResolvedAt == nil || !task.ResolvedAt.Equal(at(sixteenHundredYesterday)) {
			t.Errorf("Task = %+v, want completed at %s", task, at(sixteenHundredYesterday))
		}
		if got := mustTask(t, m, newID).Status; got != Open {
			t.Errorf("the new period's task is %q, want open", got)
		}
	})

	t.Run("the same completion of a task of the new period is before its materialization", func(t *testing.T) {
		base := b.split()
		b.add(sixteenHundredYesterday, event.TaskCompleted{TaskID: newID})
		added := b.added(base)
		materializedAt := b.members(bid(3))[2]

		_, err := Candidate(base, added)
		inv := asInvalid(t, err)
		assertFinding(t, inv, codeResolutionBeforeMaterialization, string(newID),
			[]event.ID{materializedAt}, at(sixteenHundredYesterday), at(875))
		if !strings.Contains(inv.Message, "the new event") {
			t.Errorf("message %q does not call the completion the new event", inv.Message)
		}
	})
}

func TestMissedAndWithdrawnExemptFromOrderingChecks(t *testing.T) {
	// Both markers sort before the materialization at 10 minutes and are
	// accepted; neither is a resolution.
	t.Run("missed before the materialization", func(t *testing.T) {
		b := newLog(t)
		b.batch(0, profileOf("work"), dayOf(day1))
		b.batch(5, missedTask(taskA))
		b.batch(10, b.daily("post-plan", day1))
		if got := mustTask(t, mustReplay(t, b.events), taskA).Status; got != Missed {
			t.Errorf("Status = %q, want missed", got)
		}
	})
	t.Run("withdrawn before the materialization", func(t *testing.T) {
		b := newLog(t)
		b.batch(0, profileOf("work"), dayOf(day1))
		b.batch(5, withdrawnTask(taskA))
		b.batch(10, b.daily("post-plan", day1))
		if got := mustTask(t, mustReplay(t, b.events), taskA).Status; got != Withdrawn {
			t.Errorf("Status = %q, want withdrawn", got)
		}
	})
	t.Run("a completion supersedes a missed marker whichever comes first", func(t *testing.T) {
		for _, resAt := range []int{20, 300} {
			b, id := bootstrapped(t)
			b.batch(100, missedTask(id))
			b.add(resAt, event.TaskCompleted{TaskID: id})
			if got := mustTask(t, mustReplay(t, b.events), id).Status; got != Completed {
				t.Errorf("completed at %d: Status = %q, want completed", resAt, got)
			}
		}
	})
}

func TestTwoResolutionsAreTaskAlreadyResolved(t *testing.T) {
	t.Run("a stored log with two resolutions names both", func(t *testing.T) {
		b, id := bootstrapped(t)
		first := b.add(100, event.TaskCompleted{TaskID: id})
		second := b.add(110, skippedTask(id, false)(""))
		_, err := Replay(b.events)
		inv := asInvalid(t, err)
		assertFinding(t, inv, codeTaskAlreadyResolved, string(id), []event.ID{first, second}, at(100), at(110))
		for _, want := range []event.ID{first, second} {
			if !strings.Contains(inv.Message, string(want)) {
				t.Errorf("message %q does not name the resolving event %s", inv.Message, want)
			}
		}
	})

	t.Run("a retraction of a retraction brings the second resolution back", func(t *testing.T) {
		b, id := bootstrapped(t)
		first := b.add(100, event.TaskCompleted{TaskID: id})
		second := b.add(110, event.TaskCompleted{TaskID: id})
		retraction := b.add(120, event.EventRetracted{Target: second})
		if got := mustTask(t, mustReplay(t, b.events), id).Status; got != Completed {
			t.Fatalf("with the second completion retracted the status is %q, want completed", got)
		}
		b.add(130, event.EventRetracted{Target: retraction})
		_, err := Replay(b.events)
		inv := asInvalid(t, err)
		assertFinding(t, inv, codeTaskAlreadyResolved, string(id), []event.ID{first, second}, at(100), at(110))
	})

	t.Run("a candidate lists the stored resolution and calls the other the new event", func(t *testing.T) {
		b, id := bootstrapped(t)
		first := b.add(100, event.TaskCompleted{TaskID: id})
		base := b.split()
		second := b.add(110, skippedTask(id, false)(""))
		_, err := Candidate(base, b.added(base))
		inv := asInvalid(t, err)
		assertFinding(t, inv, codeTaskAlreadyResolved, string(id), []event.ID{first}, at(100), at(110))
		if !strings.Contains(inv.Message, "the new event") || strings.Contains(inv.Message, string(second)) {
			t.Errorf("message %q must call the added skip the new event and never cite %s", inv.Message, second)
		}
	})
}

func TestCompletionBeforeMaterialization(t *testing.T) {
	b, id := bootstrapped(t)
	completion := b.add(5, event.TaskCompleted{TaskID: id}) // the task is materialized at 10
	materialization := b.members(bid(2))[0]
	_, err := Replay(b.events)
	inv := asInvalid(t, err)
	assertFinding(t, inv, codeResolutionBeforeMaterialization, string(id),
		[]event.ID{completion, materialization}, at(5), at(10))

	t.Run("a resolution of a task that was never materialized", func(t *testing.T) {
		b := newLog(t)
		b.batch(0, profileOf("work"), dayOf(day1))
		orphan := b.add(5, event.TaskCompleted{TaskID: taskA})
		_, err := Replay(b.events)
		assertFinding(t, asInvalid(t, err), codeResolutionBeforeMaterialization, string(taskA), []event.ID{orphan}, at(5))
	})

	t.Run("a correction that moves a completion before its task", func(t *testing.T) {
		b, id := bootstrapped(t)
		completion := b.add(100, event.TaskCompleted{TaskID: id})
		base := b.split()
		b.add(120, event.EventCorrected{Target: completion, Fields: fieldsOf("effective_at", at(5).Format(instantLayout))})
		_, err := Candidate(base, b.added(base))
		inv := asInvalid(t, err)
		assertFinding(t, inv, codeResolutionBeforeMaterialization, string(id),
			[]event.ID{completion, b.members(bid(2))[0]}, at(5), at(10), at(120))
		if !strings.Contains(inv.Message, "the new event") {
			t.Errorf("message %q does not name the new correction", inv.Message)
		}
	})
}

func TestMaterializedBeforeProfileIsTaskBeforeProfile(t *testing.T) {
	t.Run("a profile that comes later", func(t *testing.T) {
		b := newLog(t)
		b.batch(0, dayOf(day1), b.daily("post-plan", day1))
		b.batch(30, profileOf("work"))
		profile := b.members(bid(2))[0]
		materialization := b.members(bid(1))[1]
		_, err := Replay(b.events)
		assertFinding(t, asInvalid(t, err), codeTaskBeforeProfile, string(taskA),
			[]event.ID{materialization, profile}, at(0), at(30))
	})
	t.Run("no profile at all", func(t *testing.T) {
		b := newLog(t)
		b.batch(0, dayOf(day1), b.daily("post-plan", day1))
		materialization := b.members(bid(1))[1]
		_, err := Replay(b.events)
		assertFinding(t, asInvalid(t, err), codeTaskBeforeProfile, string(taskA), []event.ID{materialization}, at(0))
	})
	t.Run("the same instant follows the log order", func(t *testing.T) {
		b := newLog(t)
		b.batch(0, dayOf(day1), b.daily("post-plan", day1), profileOf("work"))
		_, err := Replay(b.events) // the profile is recorded after the task
		if inv := asInvalid(t, err); inv.Code != codeTaskBeforeProfile {
			t.Errorf("Code = %q, want %q", inv.Code, codeTaskBeforeProfile)
		}
	})
	t.Run("a profile retracted leaves the task before any profile", func(t *testing.T) {
		b := newLog(t)
		b.batch(0, dayOf(day1))
		profiles := b.batch(5, profileOf("work"))
		b.batch(10, b.daily("post-plan", day1))
		base := b.split()
		b.add(20, event.EventRetracted{TargetBatch: profiles})
		_, err := Candidate(base, b.added(base))
		if inv := asInvalid(t, err); inv.Code != codeTaskBeforeProfile || !strings.Contains(inv.Message, "the new event") {
			t.Errorf("Code = %q, message %q, want task_before_profile naming the new event", inv.Code, inv.Message)
		}
	})
}

func TestTaskWithoutPeriodChange(t *testing.T) {
	t.Run("a period the log never started", func(t *testing.T) {
		b := newLog(t)
		b.batch(0, profileOf("work"), dayOf(day1))
		b.batch(10, b.daily("post-plan", day2))
		materialization := b.members(bid(2))[0]
		_, err := Replay(b.events)
		assertFinding(t, asInvalid(t, err), codeTaskWithoutPeriod, "day:2026-10-08:post-plan", []event.ID{materialization}, at(10))
	})
	t.Run("a task of another kind of period", func(t *testing.T) {
		b := newLog(t)
		b.batch(0, profileOf("work"), dayOf(day1))
		b.batch(10, b.taskIn("review", due.Weekly, day1))
		_, err := Replay(b.events)
		if inv := asInvalid(t, err); inv.Code != codeTaskWithoutPeriod {
			t.Errorf("Code = %q, want %q: a day period is not the week the task is for", inv.Code, codeTaskWithoutPeriod)
		}
	})
	t.Run("retracting the period batch while the task stays", func(t *testing.T) {
		b := newLog(t)
		b.batch(0, profileOf("work"))
		periods := b.batch(5, dayOf(day1))
		b.batch(10, b.daily("post-plan", day1))
		base := b.split()
		retraction := b.add(20, event.EventRetracted{TargetBatch: periods})
		_, err := Candidate(base, b.added(base))
		inv := asInvalid(t, err)
		assertFinding(t, inv, codeTaskWithoutPeriod, string(taskA), []event.ID{b.members(bid(3))[0]}, at(10), at(20))
		if !strings.Contains(inv.Message, "the new event") || strings.Contains(inv.Message, string(retraction)) {
			t.Errorf("message %q must call the retraction the new event and never cite %s", inv.Message, retraction)
		}
	})
}

func TestReinstatementOfATaskThatIsNotWithdrawn(t *testing.T) {
	t.Run("an open task", func(t *testing.T) {
		b, id := bootstrapped(t)
		b.batch(100, reinstatedTask(id))
		reinstatement := b.members(bid(3))[0]
		_, err := Replay(b.events)
		assertFinding(t, asInvalid(t, err), codeTaskNotWithdrawn, string(id), []event.ID{reinstatement}, at(100))
	})
	t.Run("a task reinstated twice", func(t *testing.T) {
		b, id := bootstrapped(t)
		b.batch(100, withdrawnTask(id))
		b.batch(110, reinstatedTask(id))
		b.batch(120, reinstatedTask(id))
		second := b.members(bid(5))[0]
		_, err := Replay(b.events)
		assertFinding(t, asInvalid(t, err), codeTaskNotWithdrawn, string(id), []event.ID{second}, at(120))
	})
	t.Run("a reinstatement that sorts before the withdrawal", func(t *testing.T) {
		b, id := bootstrapped(t)
		b.batch(100, withdrawnTask(id))
		b.batch(90, reinstatedTask(id))
		reinstatement := b.members(bid(4))[0]
		_, err := Replay(b.events)
		assertFinding(t, asInvalid(t, err), codeTaskNotWithdrawn, string(id), []event.ID{reinstatement}, at(90))
	})
}

func TestSecondMaterializationIsTaskMaterializedTwice(t *testing.T) {
	b, id := bootstrapped(t)
	first := b.members(bid(2))[0]
	base := b.split()
	b.batch(30, b.daily("post-plan", day1))
	second := b.members(bid(3))[0]

	_, err := Replay(b.events)
	inv := asInvalid(t, err)
	assertFinding(t, inv, codeTaskMaterializedTwice, string(id), []event.ID{first, second}, at(10), at(30))

	_, err = Candidate(base, b.added(base))
	inv = asInvalid(t, err)
	assertFinding(t, inv, codeTaskMaterializedTwice, string(id), []event.ID{first}, at(10), at(30))
	if strings.Contains(inv.Message, string(second)) || !strings.Contains(inv.Message, "the new event") {
		t.Errorf("message %q must call the second materialization the new event and never cite %s", inv.Message, second)
	}
}

func TestResolutionAfterWithdrawalIsTaskWithdrawn(t *testing.T) {
	b, id := bootstrapped(t)
	b.batch(100, withdrawnTask(id))
	withdrawal := b.members(bid(3))[0]
	base := b.split()
	b.add(120, event.TaskCompleted{TaskID: id})

	_, err := Candidate(base, b.added(base))
	inv := asInvalid(t, err)
	assertFinding(t, inv, codeTaskWithdrawn, string(id), []event.ID{withdrawal}, at(100), at(120))
	if !strings.Contains(inv.Message, "the new event") {
		t.Errorf("message %q does not call the completion the new event", inv.Message)
	}

	t.Run("a resolution that precedes the withdrawal supersedes it", func(t *testing.T) {
		b, id := bootstrapped(t)
		b.batch(100, withdrawnTask(id))
		b.add(50, event.TaskCompleted{TaskID: id})
		if got := mustTask(t, mustReplay(t, b.events), id).Status; got != Completed {
			t.Errorf("Status = %q, want completed", got)
		}
	})
}

// oneSentence reports whether msg is a single plain sentence.
func oneSentence(msg string) bool {
	first := []rune(msg)[0]
	return unicode.IsUpper(first) && strings.HasSuffix(msg, ".") && !strings.Contains(msg, ". ") &&
		!strings.Contains(msg, "\n") && !strings.Contains(msg, "  ")
}

func TestMessagesNameTheActiveDayPeriodZoneNextToEveryInstant(t *testing.T) {
	b, id := bootstrapped(t)
	b.add(5, event.TaskCompleted{TaskID: id})
	_, err := Replay(b.events)
	inv := asInvalid(t, err)
	// 13:35Z on 2026-10-07 is 09:35 in New York, and at(10) is 09:40.
	for _, want := range []string{"2026-10-07T13:35:00.000Z (09:35 America/New_York)", "2026-10-07T13:40:00.000Z (09:40 America/New_York)"} {
		if !strings.Contains(inv.Message, want) {
			t.Errorf("message %q lacks the instant %q", inv.Message, want)
		}
	}

	t.Run("an instant on another civil date there says so", func(t *testing.T) {
		b, id := bootstrapped(t)
		b.add(-690, event.TaskCompleted{TaskID: id}) // 02:00Z on the 7th: 22:00 on the 6th in New York
		_, err := Replay(b.events)
		if inv := asInvalid(t, err); !strings.Contains(inv.Message, "(2026-10-06 22:00 America/New_York)") {
			t.Errorf("message %q lacks the local date of an instant on the 6th", inv.Message)
		}
	})
}

func TestNoCarryOver(t *testing.T) {
	b := newLog(t)
	b.batch(0, profileOf("work"), dayOf(day1))
	b.batch(10, b.daily("post-plan", day1), b.daily("wrap-up", day1))
	old1, old2 := event.TaskID("day:2026-10-07:post-plan"), event.TaskID("day:2026-10-07:wrap-up")
	b.batch(875, missedTask(old1), skippedTask(old2, true), dayOf(day2), b.daily("post-plan", day2), b.daily("wrap-up", day2))

	m := mustReplay(t, b.events)
	if got := mustTask(t, m, old1).Status; got != Missed {
		t.Errorf("the open task is %q after the rollover, want missed", got)
	}
	if got := mustTask(t, m, old2).Status; got != Skipped {
		t.Errorf("the skipped task is %q after the rollover, want skipped", got)
	}
	newOnes := 0
	for _, task := range m.Tasks() {
		if task.PeriodStart == day1 {
			continue
		}
		newOnes++
		if task.PeriodStart != day2 || task.Status != Open || !strings.HasPrefix(string(task.ID), "day:2026-10-08:") {
			t.Errorf("new task %+v: want an open task of %s with its own id", task, day2)
		}
		if task.ID == old1 || task.ID == old2 {
			t.Errorf("new task %s reuses the id of a task of the left period", task.ID)
		}
	}
	if newOnes != 2 {
		t.Errorf("%d tasks in the new period, want 2", newOnes)
	}

	// Nothing but a resolution changes a left task, and a resolution can
	// never make it open.
	base := b.split()
	tests := []struct {
		name   string
		task   event.TaskID
		add    func(*logb)
		status TaskStatus
		code   Code
	}{
		{"complete the missed task", old1, func(b *logb) { b.add(900, event.TaskCompleted{TaskID: old1}) }, Completed, ""},
		{"skip the missed task", old1, func(b *logb) { b.add(900, skippedTask(old1, false)("")) }, Skipped, ""},
		{"reinstate the missed task", old1, func(b *logb) { b.batch(900, reinstatedTask(old1)) }, "", codeTaskNotWithdrawn},
		{"materialize the missed task again", old1, func(b *logb) { b.batch(900, b.daily("post-plan", day1)) }, "", codeTaskMaterializedTwice},
		{"withdraw the missed task", old1, func(b *logb) { b.batch(900, withdrawnTask(old1)) }, Withdrawn, ""},
		{"complete the skipped task", old2, func(b *logb) { b.add(900, event.TaskCompleted{TaskID: old2}) }, "", codeTaskAlreadyResolved},
		{"reinstate the skipped task", old2, func(b *logb) { b.batch(900, reinstatedTask(old2)) }, "", codeTaskNotWithdrawn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b2 := &logb{t: t, events: slices.Clone(base), batches: b.batches}
			tt.add(b2)
			m, err := Replay(b2.events)
			if tt.code != "" {
				if inv := asInvalid(t, err); inv.Code != tt.code {
					t.Errorf("Code = %q, want %q", inv.Code, tt.code)
				}
				return
			}
			if err != nil {
				t.Fatalf("Replay: %v", err)
			}
			if got := mustTask(t, m, tt.task).Status; got != tt.status || got == Open {
				t.Errorf("Status = %q, want %q and never open", got, tt.status)
			}
		})
	}
}

// goldenLogs are the synthetic logs under testdata/logs/tasks, built with the
// real encoder, each with what replaying it must give.
func goldenLogs(t *testing.T) []golden {
	t.Helper()
	var out []golden

	b, id := bootstrapped(t)
	b.batch(100, withdrawnTask(id))
	b.batch(200, reinstatedTask(id))
	b.add(300, event.TaskCompleted{TaskID: id})
	out = append(out, golden{name: "profile-withdraw-reinstate.jsonl", events: b.events, check: func(t *testing.T, m *Model, err error) {
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		if got := mustTask(t, m, id).Status; got != Completed || m.Profile() != "work" {
			t.Errorf("status %q, profile %q, want a completed task under work", got, m.Profile())
		}
	}})

	b, id = bootstrapped(t)
	rollover(b, id)
	b.addAt(at(900), at(390), event.TaskCompleted{TaskID: id}) // recorded after the rollover, effective the day before
	out = append(out, golden{name: "rollover-late-completion.jsonl", events: b.events, check: func(t *testing.T, m *Model, err error) {
		newID := event.NewTaskID(due.Daily, day2, "post-plan")
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		if old, fresh := mustTask(t, m, id).Status, mustTask(t, m, newID).Status; old != Completed || fresh != Open {
			t.Errorf("old task %q, new task %q, want completed and open", old, fresh)
		}
		// The new day's task is due a day after the old day's.
		if old, fresh := mustTask(t, m, id).Due, mustTask(t, m, newID).Due; !fresh.Equal(old.Add(24 * time.Hour)) {
			t.Errorf("the new task is due %v, want a day after the old task's %v", fresh, old)
		}
		if p, _ := m.Period(Day); p.Start != day2 {
			t.Errorf("current day = %s, want %s", p.Start, day2)
		}
	}})

	b, id = bootstrapped(t)
	b.add(100, event.TaskCompleted{TaskID: id})
	b.add(110, skippedTask(id, false)(""))
	out = append(out, golden{name: "two-resolutions.jsonl", events: b.events, check: func(t *testing.T, _ *Model, err error) {
		if inv := asInvalid(t, err); inv.Code != codeTaskAlreadyResolved {
			t.Errorf("Code = %q, want %q", inv.Code, codeTaskAlreadyResolved)
		}
	}})

	// Each change is its own batch, recorded at noon UTC of the day given, in
	// log order and after the profile. The change to the 1st is backdated to
	// the 1st and the change to the 9th to the 3rd, so the 9th sorts before
	// the 8th although it was recorded after it.
	b = newLog(t)
	b.batch(0, profileOf("work"))
	for _, p := range []struct{ start, recorded, effective int }{{1, 8, 1}, {8, 8, 8}, {9, 9, 3}} {
		recorded := instantOn(civil.Date{Year: 2026, Month: time.October, Day: p.recorded}, 12)
		b.batches++
		bt := bid(b.batches)
		b.addAt(recorded, instantOn(civil.Date{Year: 2026, Month: time.October, Day: p.effective}, 12),
			event.PeriodChanged{Kind: "day", Start: civil.Date{Year: 2026, Month: time.October, Day: p.start}, TZ: "America/New_York", Batch: bt})
		b.addAt(recorded, recorded, event.BatchCommitted{Batch: bt})
	}
	out = append(out, golden{name: "backdated-period.jsonl", events: b.events, check: func(t *testing.T, _ *Model, err error) {
		if inv := asInvalid(t, err); inv.Code != codePeriodOutOfOrder {
			t.Errorf("Code = %q, want %q", inv.Code, codePeriodOutOfOrder)
		}
	}})
	return out
}

type golden struct {
	name   string
	events []event.Event
	check  func(t *testing.T, m *Model, err error)
}

func TestGoldenTaskLogs(t *testing.T) {
	checkGoldenLogs(t, filepath.Join("..", "..", "testdata", "logs", "tasks"), goldenLogs(t))
}

// checkGoldenLogs compares each golden log with the file of its name in dir,
// rewriting the file under -update, then decodes the file and replays it.
func checkGoldenLogs(t *testing.T, dir string, logs []golden) {
	t.Helper()
	for _, g := range logs {
		t.Run(g.name, func(t *testing.T) {
			var want []byte
			for _, e := range g.events {
				line, err := event.Encode(e)
				if err != nil {
					t.Fatalf("Encode: %v", err)
				}
				want = append(want, append(line, '\n')...)
			}
			path := filepath.Join(dir, g.name)
			if *updateGolden {
				if err := os.MkdirAll(dir, 0o755); err != nil {
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
			m, err := Replay(events)
			g.check(t, m, err)
		})
	}
}
