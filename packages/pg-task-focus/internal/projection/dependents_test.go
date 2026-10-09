package projection

import (
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

func TestDependentsReferenceCreatedEntities(t *testing.T) {
	b := newLog(t)
	bootstrap := b.batch(0, profileOf("work"), dayOf(day1), b.daily("post-plan", day1), b.daily("plan-day", day1))
	oldPost := event.NewTaskID(due.Daily, day1, "post-plan")
	oldPlan := event.NewTaskID(due.Daily, day1, "plan-day")
	newPost := event.NewTaskID(due.Daily, day2, "post-plan")
	newPlan := event.NewTaskID(due.Daily, day2, "plan-day")
	rollover := b.batch(875, missedTask(oldPost), missedTask(oldPlan), dayOf(day2), b.daily("post-plan", day2), b.daily("plan-day", day2))
	marked := b.members(rollover)[:2]

	// A late completion of a task the rollover marked missed references a
	// task the bootstrap created, not one the rollover created.
	late := b.addAt(at(880), at(390), event.TaskCompleted{TaskID: oldPost})
	completed := b.add(900, event.TaskCompleted{TaskID: newPost})
	skipped := b.add(905, event.TaskSkipped{TaskID: newPlan, Reason: "not needed"})
	b.add(906, event.EventRetracted{Target: skipped})
	profile := b.batch(910, profileOf("light"), withdrawnTask(newPlan))
	withdrawn := b.members(profile)[1]

	b.add(920, startOf(cycleA, 50))
	b.add(925, interruptOf(cycleB, cycleA, 15))
	switched := b.switchTo(930, cycleB, cycleA)
	broke := b.backfill(950, 935, 940, cycleA)

	m := mustReplay(t, b.events)
	tests := []struct {
		name  string
		batch event.ID
		want  []event.ID
	}{
		{"the rollover's tasks are completed and withdrawn later; the retracted skip is not live", rollover, []event.ID{completed, withdrawn}},
		{"the bootstrap's tasks are marked missed and completed late", bootstrap, append(slices.Clone(marked), late)},
		{"a later batch nothing references", profile, nil},
		{"a switch creates no entity", switched, nil},
		{"a break creates no entity", broke, nil},
		{"an id that names no batch", eid(999), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := m.Dependents(tt.batch); !slices.Equal(got, tt.want) {
				t.Errorf("Dependents(%s) = %v, want %v", tt.batch, got, tt.want)
			}
		})
	}

	t.Run("the caller owns the result", func(t *testing.T) {
		got := m.Dependents(rollover)
		got[0] = eid(999)
		if again := m.Dependents(rollover); again[0] != completed {
			t.Errorf("Dependents shares its slice: %v", again)
		}
	})
}

func TestCycleDependentsListsLaterEventsAndInterruptingStarts(t *testing.T) {
	b := dayLog(t)
	startA := b.add(0, startOf(cycleA, 50))
	boost := b.add(5, boostOf(cycleA, 10))
	startB := b.add(10, interruptOf(cycleB, cycleA, 15))
	stopB := b.add(20, stopOf(cycleB))
	resumeA := b.add(21, resumeOf(cycleA))
	note := b.add(25, noteOf(cycleA, "first"))
	gone := b.add(26, noteOf(cycleA, "second"))
	b.add(27, event.EventRetracted{Target: gone})
	stopA := b.add(30, stopOf(cycleA))
	startC := b.add(40, startOf(cycleC, 25))

	m := mustReplay(t, b.events)
	tests := []struct {
		name  string
		start event.ID
		want  []event.ID
	}{
		{"the later live events of the cycle and the start that interrupts it", startA, []event.ID{boost, startB, resumeA, note, stopA}},
		{"an interrupting start's own cycle", startB, []event.ID{stopB}},
		{"a start nothing follows", startC, nil},
		{"an event that is not a start", boost, nil},
		{"an id not in the log", eid(999), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := m.CycleDependents(tt.start); !slices.Equal(got, tt.want) {
				t.Errorf("CycleDependents(%s) = %v, want %v", tt.start, got, tt.want)
			}
		})
	}

	t.Run("a retracted start has no dependents", func(t *testing.T) {
		c := dayLog(t)
		start := c.add(0, startOf(cycleA, 50))
		c.add(1, event.EventRetracted{Target: start})
		if got := mustReplay(t, c.events).CycleDependents(start); got != nil {
			t.Errorf("CycleDependents = %v, want none", got)
		}
	})

	// A stored log that retracts the start anyway is refused by replay with the
	// code of the structure it leaves.
	t.Run("replay of the retraction finds the cycle's events before its start", func(t *testing.T) {
		base := b.split()
		b.add(50, event.EventRetracted{Target: startA})
		_, err := Candidate(base, b.added(base))
		_ = assertCode(t, err, codeCycleEventBeforeStart)
	})
	t.Run("replay of the retraction finds the interrupting start's cycle not running", func(t *testing.T) {
		c := dayLog(t)
		start := c.add(0, startOf(cycleA, 50))
		c.add(10, interruptOf(cycleB, cycleA, 15))
		if got := mustReplay(t, c.events).CycleDependents(start); len(got) != 1 {
			t.Fatalf("CycleDependents = %v, want the interrupting start", got)
		}
		base := c.split()
		c.add(20, event.EventRetracted{Target: start})
		_, err := Candidate(base, c.added(base))
		_ = assertCode(t, err, codeInterruptedCycleNotRunning)
	})
}

func TestEntityOfNamesTheTaskCycleOrPeriodKind(t *testing.T) {
	tests := []struct {
		p    event.Payload
		want string
	}{
		{event.TaskCompleted{TaskID: taskA}, string(taskA)},
		{event.CycleBoosted{CycleID: cycleA, Minutes: 5}, string(cycleA)},
		{event.PeriodChanged{Kind: "week"}, "week"},
		{event.ProfileChanged{Profile: "work"}, ""},
		{event.EventRetracted{Target: eid(1)}, ""},
	}
	for _, tt := range tests {
		if got := EntityOf(tt.p); got != tt.want {
			t.Errorf("EntityOf(%T) = %q, want %q", tt.p, got, tt.want)
		}
	}
}
