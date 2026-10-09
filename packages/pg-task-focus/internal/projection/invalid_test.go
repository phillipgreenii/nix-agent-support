package projection

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

func TestInvalidImplementsErrorAndCodesListsTheCodesDeclaredSoFar(t *testing.T) {
	var err error = &Invalid{
		Code:     codeUnknownEvent,
		Message:  "The new event corrects an event that is not in the log.",
		Entity:   "day:2026-10-07:post-plan",
		Events:   []event.ID{eid(1)},
		Instants: []time.Time{at(0)},
	}
	if got, want := err.Error(), "The new event corrects an event that is not in the log."; got != want {
		t.Errorf("Error() = %q, want the message %q", got, want)
	}
	var inv *Invalid
	if !errors.As(err, &inv) || inv.Entity != "day:2026-10-07:post-plan" {
		t.Errorf("errors.As did not recover the *Invalid: %+v", inv)
	}

	want := []Code{
		"unknown_event", "invalid_correction",
		"task_already_resolved", "task_withdrawn", "task_not_withdrawn", "task_materialized_twice",
		"task_without_period", "task_before_profile", "resolution_before_materialization",
		"period_unchanged", "period_out_of_order",
		"cycle_stopped", "another_cycle_running", "interrupted_cycle_not_running", "cycle_segments_overlap",
		"break_ends_at_stop", "stop_not_after_start", "empty_running_segment", "cycle_event_before_start",
	}
	if got := Codes(); !reflect.DeepEqual(got, want) {
		t.Errorf("Codes() = %v, want %v", got, want)
	}
	got := Codes()
	got[0] = "tampered"
	if Codes()[0] != "unknown_event" {
		t.Error("Codes() hands out its own backing array: a caller changed the list")
	}
}

func TestInvalidFieldsCarryTheCyclesOfAReplayFinding(t *testing.T) {
	inv := &Invalid{Code: codeInvalidCorrection, Cycles: []event.CycleID{cycleA}}
	if len(inv.Cycles) != 1 || inv.Cycles[0] != cycleA {
		t.Errorf("Cycles = %v, want [%s]", inv.Cycles, cycleA)
	}
}

// invalidCase is a log that gives one code: the stored log alone, or a base
// and the events a request would add.
type invalidCase struct {
	name   string
	code   Code
	entity string // "" only where the finding has no entity: an unknown target
	cycles []event.CycleID
	zone   bool // the stage that finds it gives every instant in the day period's zone too
	build  func(t *testing.T) (base, add []event.Event)
}

func stored(f func(t *testing.T) *logb) func(t *testing.T) ([]event.Event, []event.Event) {
	return func(t *testing.T) ([]event.Event, []event.Event) { return f(t).events, nil }
}

// invalidCases gives every code a replay can find, once.
func invalidCases() []invalidCase {
	date := func(day int) civil.Date { return civil.Date{Year: 2026, Month: time.October, Day: day} }
	return []invalidCase{
		{"unknown_event", codeUnknownEvent, "", nil, false, stored(func(t *testing.T) *logb {
			b := dayLog(t)
			b.add(10, event.EventRetracted{Target: eid(77)})
			return b
		})},
		{"invalid_correction", codeInvalidCorrection, string(cycleA), nil, false, stored(func(t *testing.T) *logb {
			b := dayLog(t)
			start := b.add(0, startOf(cycleA, 50))
			b.add(10, event.EventCorrected{Target: start, Fields: fieldsOf("cycle_id", string(cycleB))})
			return b
		})},
		{"task_already_resolved", codeTaskAlreadyResolved, string(taskA), nil, true, stored(func(t *testing.T) *logb {
			b, id := bootstrapped(t)
			b.add(100, event.TaskCompleted{TaskID: id})
			b.add(110, event.TaskCompleted{TaskID: id})
			return b
		})},
		{"resolution_before_materialization", codeResolutionBeforeMaterialization, string(taskA), nil, true, stored(func(t *testing.T) *logb {
			b, id := bootstrapped(t)
			b.add(5, event.TaskCompleted{TaskID: id})
			return b
		})},
		{"task_withdrawn", codeTaskWithdrawn, string(taskA), nil, true, stored(func(t *testing.T) *logb {
			b, id := bootstrapped(t)
			b.batch(100, withdrawnTask(id))
			b.add(120, event.TaskCompleted{TaskID: id})
			return b
		})},
		{"task_not_withdrawn", codeTaskNotWithdrawn, string(taskA), nil, true, stored(func(t *testing.T) *logb {
			b, id := bootstrapped(t)
			b.batch(100, reinstatedTask(id))
			return b
		})},
		{"task_materialized_twice", codeTaskMaterializedTwice, string(taskA), nil, true, stored(func(t *testing.T) *logb {
			b, _ := bootstrapped(t)
			b.batch(30, b.daily("post-plan", day1))
			return b
		})},
		{"task_without_period", codeTaskWithoutPeriod, "day:2026-10-08:post-plan", nil, true, stored(func(t *testing.T) *logb {
			b, _ := bootstrapped(t)
			b.batch(30, b.daily("post-plan", day2))
			return b
		})},
		{"task_before_profile", codeTaskBeforeProfile, string(taskA), nil, true, stored(func(t *testing.T) *logb {
			b := newLog(t)
			b.batch(0, dayOf(day1), b.daily("post-plan", day1))
			return b
		})},
		{"period_unchanged", codePeriodUnchanged, "day", nil, true, stored(func(t *testing.T) *logb {
			b, _ := bootstrapped(t)
			b.batch(30, dayOf(day1))
			return b
		})},
		{"period_out_of_order", codePeriodOutOfOrder, "day", nil, true, func(t *testing.T) ([]event.Event, []event.Event) {
			b := newLog(t)
			b.batch(0, profileOf("work"))
			change := func(recorded, effective, start int) {
				b.addAt(instantOn(date(recorded), 12), instantOn(date(effective), 12),
					event.PeriodChanged{Kind: "day", Start: date(start), TZ: "America/New_York", Batch: bid(2)})
			}
			change(1, 1, 1)
			change(8, 8, 8)
			base := b.split()
			change(9, 3, 9)
			return base, b.added(base)
		}},
		{"cycle_stopped", codeCycleStopped, string(cycleA), nil, true, stored(func(t *testing.T) *logb {
			b := dayLog(t)
			b.add(0, startOf(cycleA, 50))
			b.add(10, stopOf(cycleA))
			b.add(20, resumeOf(cycleA))
			return b
		})},
		{"another_cycle_running", codeAnotherCycleRunning, string(cycleB), []event.CycleID{cycleA, cycleB}, true, stored(func(t *testing.T) *logb {
			b := dayLog(t)
			b.add(0, startOf(cycleA, 50))
			b.add(10, startOf(cycleB, 25))
			return b
		})},
		{"interrupted_cycle_not_running", codeInterruptedCycleNotRunning, string(cycleB), nil, true, stored(func(t *testing.T) *logb {
			b := dayLog(t)
			b.add(0, startOf(cycleA, 50))
			b.add(10, pauseOf(cycleA))
			b.add(20, interruptOf(cycleB, cycleA, 15))
			return b
		})},
		{"cycle_segments_overlap", codeCycleSegmentsOverlap, string(cycleA), nil, true, stored(func(t *testing.T) *logb {
			b := dayLog(t)
			b.add(0, startOf(cycleA, 50))
			b.add(10, pauseOf(cycleA))
			b.add(20, pauseOf(cycleA))
			return b
		})},
		{"break_ends_at_stop", codeBreakEndsAtStop, string(cycleA), nil, true, func(t *testing.T) ([]event.Event, []event.Event) {
			b, _ := stoppedAt60(t)
			base := b.split()
			b.backfill(70, 30, 60, cycleA)
			return base, b.added(base)
		}},
		{"stop_not_after_start", codeStopNotAfterStart, string(cycleA), nil, true, stored(func(t *testing.T) *logb {
			b := dayLog(t)
			b.add(10, startOf(cycleA, 50))
			b.add(10, stopOf(cycleA))
			return b
		})},
		{"empty_running_segment", codeEmptyRunningSegment, string(cycleA), nil, true, stored(func(t *testing.T) *logb {
			b := dayLog(t)
			b.add(0, startOf(cycleA, 50))
			b.add(10, pauseOf(cycleA))
			b.add(20, resumeOf(cycleA))
			b.add(20, pauseOf(cycleA))
			return b
		})},
		{"cycle_event_before_start", codeCycleEventBeforeStart, string(cycleA), nil, true, stored(func(t *testing.T) *logb {
			b := dayLog(t)
			b.add(10, startOf(cycleA, 50))
			b.add(5, pauseOf(cycleA))
			return b
		})},
	}
}

func (c invalidCase) run(t *testing.T) *Invalid {
	t.Helper()
	base, add := c.build(t)
	var err error
	if add == nil {
		_, err = Replay(base)
	} else {
		_, err = candidateReplay(base, add)
	}
	return asInvalid(t, err) // entity, stored event ids and instants are in the message
}

func TestInvalidMessagesNameEntityInstantsAndEvents(t *testing.T) {
	for _, tt := range invalidCases() {
		t.Run(tt.name, func(t *testing.T) {
			inv := tt.run(t)
			if inv.Code != tt.code {
				t.Fatalf("Code = %q (%s), want %q", inv.Code, inv.Message, tt.code)
			}
			if inv.Entity != tt.entity {
				t.Errorf("Entity = %q, want %q", inv.Entity, tt.entity)
			}
			if len(inv.Events) == 0 || len(inv.Instants) == 0 {
				t.Errorf("Events %v, Instants %v: each MUST be set", inv.Events, inv.Instants)
			}
			if !sameCycles(inv.Cycles, tt.cycles...) {
				t.Errorf("Cycles = %v, want %v", inv.Cycles, tt.cycles)
			}
			for _, c := range inv.Cycles {
				if !strings.Contains(inv.Message, string(c)) {
					t.Errorf("message %q does not name the cycle %s", inv.Message, c)
				}
			}
			if !oneSentence(inv.Message) {
				t.Errorf("message %q is not one plain sentence", inv.Message)
			}
			if inv.Error() != inv.Message {
				t.Errorf("Error() = %q, want the message", inv.Error())
			}
			if tt.zone && !strings.Contains(inv.Message, "America/New_York") {
				t.Errorf("message %q names no zone though a day period exists", inv.Message)
			}
		})
	}
}

func TestCodesListsEveryCodeProduced(t *testing.T) {
	var produced []Code
	for _, tt := range invalidCases() {
		t.Run(tt.name, func(t *testing.T) {
			produced = append(produced, tt.run(t).Code)
		})
	}
	slices.Sort(produced)
	declared := Codes()
	slices.Sort(declared)
	if !slices.Equal(produced, declared) {
		t.Errorf("the codes produced %v differ from Codes() %v: every code is produced by a test, once", produced, declared)
	}
}
