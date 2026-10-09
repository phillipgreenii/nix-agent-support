package projection

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/zone"
)

// The tests in this file pin boundaries the rest of the suite left open:
// which event a finding names and how its sentence reads, the order of ids,
// and the edges of the lookups the command layer relies on.

// wantMessage fails unless the finding's message holds every part.
func wantMessage(t *testing.T, inv *Invalid, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(inv.Message, p) {
			t.Errorf("message %q does not contain %q", inv.Message, p)
		}
	}
}

func TestDoubleResolutionNamesHowEachResolved(t *testing.T) {
	b, task := bootstrapped(t)
	b.add(20, skippedTask(task, false)(""))
	b.add(30, event.TaskCompleted{TaskID: task})
	_, err := Replay(b.events)
	inv := assertCode(t, err, codeTaskAlreadyResolved)
	wantMessage(t, inv, "is skipped by", "and completed by")
}

func TestDoubleResolutionAtOneInstantListsTheInstantOnce(t *testing.T) {
	b, task := bootstrapped(t)
	b.add(20, event.TaskCompleted{TaskID: task})
	b.add(20, event.TaskCompleted{TaskID: task})
	_, err := Replay(b.events)
	inv := assertCode(t, err, codeTaskAlreadyResolved)
	if len(inv.Events) != 2 || len(inv.Instants) != 1 || !inv.Instants[0].Equal(at(20)) {
		t.Errorf("Events %v, Instants %v; want both events and the one instant", inv.Events, inv.Instants)
	}
}

func TestMarkersOfATaskNeverMaterializedDescribeNoTask(t *testing.T) {
	b, task := bootstrapped(t)
	ghost := event.TaskID("day:2026-10-07:never-materialized")
	b.batch(20, missedTask(ghost))
	m := mustReplay(t, b.events)
	if _, ok := m.Task(ghost); ok {
		t.Errorf("a task that was only marked missed is in the model")
	}
	if got := m.Tasks(); len(got) != 1 || got[0].ID != task {
		t.Errorf("Tasks() = %v, want only %s", got, task)
	}
}

func TestResolutionAfterASecondWithdrawalNamesTheFirst(t *testing.T) {
	b, task := bootstrapped(t)
	first := b.add(20, event.TaskWithdrawn{TaskID: task})
	second := b.add(25, event.TaskWithdrawn{TaskID: task})
	b.add(30, event.TaskCompleted{TaskID: task})
	_, err := Replay(b.events)
	inv := assertCode(t, err, codeTaskWithdrawn)
	wantMessage(t, inv, string(first))
	if strings.Contains(inv.Message, string(second)) {
		t.Errorf("message %q names the second withdrawal, which changed nothing", inv.Message)
	}
}

func TestBatchEventsOfTheEmptyIDIsEmpty(t *testing.T) {
	b, task := bootstrapped(t)
	b.add(20, event.TaskCompleted{TaskID: task})
	m := mustReplay(t, b.events)
	if got := m.BatchEvents(""); len(got) != 0 {
		t.Errorf("BatchEvents(\"\") = %v, want none: the empty id names no batch", got)
	}
}

func TestNewestEventIgnoresStartsInterruptingAnotherCycle(t *testing.T) {
	if cycleA >= cycleC {
		t.Fatalf("the test needs cycleA to sort before cycleC")
	}
	b := dayLog(t)
	b.add(-20, startOf(cycleA, 50))
	stop := b.add(-15, stopOf(cycleA))
	b.add(0, startOf(cycleC, 50))
	b.add(10, interruptOf(cycleB, cycleC, 25))
	m := mustReplay(t, b.events)
	if e, ok := m.NewestEvent(string(cycleA)); !ok || e.ID != stop {
		t.Errorf("NewestEvent(A) = %s, %v; want A's stop %s", e.ID, ok, stop)
	}
}

func TestCycleDependentsOfAStartThatOpensTheLog(t *testing.T) {
	b := newLog(t)
	start := b.add(0, startOf(cycleA, 50))
	pause := b.add(10, pauseOf(cycleA))
	m := mustReplay(t, b.events)
	if got := m.CycleDependents(start); !slices.Equal(got, []event.ID{pause}) {
		t.Errorf("CycleDependents(the first event) = %v, want [%s]", got, pause)
	}
}

func TestCycleDependentsListOnlyTheirOwnCycle(t *testing.T) {
	// A sorts before B, and each has later events of its own.
	b := dayLog(t)
	startA := b.add(0, startOf(cycleA, 50))
	stopA := b.add(10, stopOf(cycleA))
	startB := b.add(20, startOf(cycleB, 50))
	stopB := b.add(30, stopOf(cycleB))
	m := mustReplay(t, b.events)
	if got := m.CycleDependents(startA); !slices.Equal(got, []event.ID{stopA}) {
		t.Errorf("CycleDependents(A) = %v, want only A's stop", got)
	}
	if got := m.CycleDependents(startB); !slices.Equal(got, []event.ID{stopB}) {
		t.Errorf("CycleDependents(B) = %v, want only B's stop", got)
	}

	// A start that interrupts C, whose id sorts after A's, is no dependent
	// of A.
	if cycleA >= cycleC {
		t.Fatalf("the test needs cycleA to sort before cycleC")
	}
	b.add(40, startOf(cycleC, 50))
	b.add(50, interruptOf(cycleD, cycleC, 25))
	m = mustReplay(t, b.events)
	if got := m.CycleDependents(startA); !slices.Equal(got, []event.ID{stopA}) {
		t.Errorf("CycleDependents(A) = %v, want only A's stop, not the start that interrupts C", got)
	}
}

func TestCycleTitleOfARetractedStartThatOpensTheLog(t *testing.T) {
	b := newLog(t)
	start := b.add(0, startOf(cycleA, 50))
	b.add(5, event.EventRetracted{Target: start})
	m := mustReplay(t, b.events)
	if title, ok := m.CycleTitle(cycleA); !ok || title != "Deep work" {
		t.Errorf("CycleTitle(A) = %q, %v; want the retracted start's title", title, ok)
	}
}

func TestSwitchBackToALowerIDRecordsTheDisplacement(t *testing.T) {
	if cycleA >= cycleB {
		t.Fatalf("the test needs cycleA to sort before cycleB")
	}
	b := dayLog(t)
	b.add(0, startOf(cycleA, 50))
	b.add(10, interruptOf(cycleB, cycleA, 25))
	b.switchTo(20, cycleB, cycleA)
	m := mustReplay(t, b.events)
	if got := mustCycle(t, m, cycleB).InterruptedBy; got != cycleA {
		t.Errorf("after the switch to A, B.InterruptedBy = %q, want %s", got, cycleA)
	}
}

func TestEarliestOfTwoBatchRetractionsRetractsTheBatch(t *testing.T) {
	b := dayLog(t)
	b.add(0, startOf(cycleA, 50))
	brk := b.backfill(30, 10, 20, cycleA)
	first := b.add(40, event.EventRetracted{TargetBatch: brk})
	b.add(50, event.EventRetracted{TargetBatch: brk})
	m := mustReplay(t, b.events)
	for _, id := range m.BatchEvents(brk) {
		v, ok := m.Event(id)
		if !ok || !v.Retracted || v.RetractedBy != first {
			t.Errorf("member %s: Retracted %v by %s, want retracted by the earlier %s", id, v.Retracted, v.RetractedBy, first)
		}
	}
}

func TestAnInterruptAtTheStartInstantSaysWhoInterrupts(t *testing.T) {
	b := dayLog(t)
	b.add(10, startOf(cycleA, 50))
	b.add(10, interruptOf(cycleB, cycleA, 25))
	_, err := Replay(b.events)
	inv := asInvalid(t, err)
	wantMessage(t, inv, "is interrupted by the start of")
}

func TestOverlappingStartsBlameTheOneLoggedLater(t *testing.T) {
	// B starts later in time but is logged first; A, which starts earlier,
	// is the event that made the overlap.
	b := dayLog(t)
	b.add(10, startOf(cycleB, 50))
	b.add(0, startOf(cycleA, 50))
	_, err := Replay(b.events)
	inv := assertCode(t, err, codeAnotherCycleRunning)
	if inv.Entity != string(cycleA) {
		t.Errorf("Entity = %q, want %s, whose start is later in the log (%s)", inv.Entity, cycleA, inv.Message)
	}
}

func TestEventsOfACycleBeforeItsStartInstant(t *testing.T) {
	// Each event is logged before the start but takes effect at the start's
	// instant, so it sorts before the start.
	cases := []struct {
		name string
		p    event.Payload
		code Code
	}{
		{"a stop", stopOf(cycleA), codeStopNotAfterStart},
		{"a pause", pauseOf(cycleA), codeCycleEventBeforeStart},
		{"an annotation", noteOf(cycleA, "early"), codeCycleEventBeforeStart},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := dayLog(t)
			b.add(10, tc.p)
			start := b.add(10, startOf(cycleA, 50))
			_, err := Replay(b.events)
			inv := assertCode(t, err, tc.code)
			wantMessage(t, inv, string(start))
		})
	}
	t.Run("a pause earlier than the start names the start", func(t *testing.T) {
		b := dayLog(t)
		start := b.add(10, startOf(cycleA, 50))
		b.add(20, interruptOf(cycleB, cycleA, 25))
		b.add(5, pauseOf(cycleA))
		_, err := Replay(b.events)
		inv := assertCode(t, err, codeCycleEventBeforeStart)
		wantMessage(t, inv, string(start), "that starts it")
	})
}

func TestInterruptingACycleThatIsNotRunningSaysWhy(t *testing.T) {
	cases := []struct {
		name  string
		build func(b *logb)
		want  string
	}{
		{"paused by another start", func(b *logb) {
			b.add(0, startOf(cycleA, 50))
			b.add(10, interruptOf(cycleB, cycleA, 25))
			b.add(20, interruptOf(cycleC, cycleA, 25))
		}, "is paused since the start of"},
		{"stopped", func(b *logb) {
			b.add(0, startOf(cycleA, 50))
			b.add(10, stopOf(cycleA))
			b.add(20, interruptOf(cycleB, cycleA, 25))
		}, "was stopped by"},
		{"itself", func(b *logb) {
			b.add(0, interruptOf(cycleA, cycleA, 25))
		}, "is the cycle it starts"},
		{"not started yet", func(b *logb) {
			b.add(0, interruptOf(cycleB, cycleA, 25))
			b.add(10, startOf(cycleA, 50))
		}, "starts only with"},
		{"never started", func(b *logb) {
			b.add(0, interruptOf(cycleB, cycleA, 25))
		}, "has no live cycle.started"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := dayLog(t)
			tc.build(b)
			_, err := Replay(b.events)
			inv := assertCode(t, err, codeInterruptedCycleNotRunning)
			wantMessage(t, inv, tc.want)
		})
	}
}

func TestANewBreakOnAStoppedCycleSaysWhichEdgeHitsTheStop(t *testing.T) {
	for _, tc := range []struct {
		name     string
		from, to int
		edge     string
	}{
		{"the break begins after the stop", 65, 70, "begins with"},
		{"the break ends after the stop", 30, 70, "ends with"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := stoppedAt60(t)
			base := b.split()
			b.backfill(80, tc.from, tc.to, cycleA)
			_, err := Candidate(base, b.added(base))
			inv := assertCode(t, err, codeBreakEndsAtStop)
			wantMessage(t, inv, tc.edge)
		})
	}
}

func TestAStoredBreakOnAStoppedCycleNamesItsBatch(t *testing.T) {
	b, _ := stoppedAt60(t)
	brk := b.backfill(80, 65, 70, cycleA)
	_, err := Replay(b.events)
	inv := assertCode(t, err, codeCycleStopped)
	wantMessage(t, inv, "a member of batch "+string(brk))

	b, _ = stoppedAt60(t)
	b.add(65, pauseOf(cycleA))
	_, err = Replay(b.events)
	inv = assertCode(t, err, codeCycleStopped)
	if strings.Contains(inv.Message, "a member of batch") {
		t.Errorf("message %q calls a pause that is in no batch a batch member", inv.Message)
	}
}

func TestASwitchInvolvingAStoppedCycleIsCycleStopped(t *testing.T) {
	// The ids on either side of the stopped cycle B: A sorts before it and
	// C after it. A switch batch pauses one cycle and resumes another; when
	// either is the stopped B, the batch is no break and the code is
	// cycle_stopped, never break_ends_at_stop.
	cases := []struct {
		name            string
		other           event.CycleID
		pause, resume   event.CycleID
		otherPausedAt30 bool
	}{
		{"resume the stopped B, pausing the lower A", cycleA, cycleA, cycleB, false},
		{"resume the stopped B, pausing the higher C", cycleC, cycleC, cycleB, false},
		{"pause the stopped B, resuming the lower A", cycleA, cycleB, cycleA, true},
		{"pause the stopped B, resuming the higher C", cycleC, cycleB, cycleC, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := dayLog(t)
			b.add(0, startOf(cycleB, 25))
			b.add(10, stopOf(cycleB))
			b.add(20, startOf(tc.other, 25))
			if tc.otherPausedAt30 {
				b.add(30, pauseOf(tc.other))
			}
			base := b.split()
			b.batch(40, pauseIn(tc.pause), resumeIn(tc.resume))
			_, err := Candidate(base, b.added(base))
			_ = assertCode(t, err, codeCycleStopped)
		})
	}
}

func TestACorrectionNamesATargetWithoutAnEntityPlainly(t *testing.T) {
	b := dayLog(t)
	target := b.events[0].ID // the profile.changed of the first batch
	if b.events[0].Payload.EventType() != event.TypeProfileChanged {
		t.Fatalf("the first event is %s, want profile.changed", b.events[0].Payload.EventType())
	}
	b.add(0, event.EventCorrected{Target: target, Fields: fieldsOf("profile", "other")})
	_, err := Replay(b.events)
	inv := assertCode(t, err, codeInvalidCorrection)
	wantMessage(t, inv, "event "+string(target)+" (profile.changed)")
}

func TestATargetLaterInTheLogIsUnknown(t *testing.T) {
	t.Run("a stored target", func(t *testing.T) {
		b := dayLog(t)
		later := eid(len(b.events) + 2)
		b.add(0, event.EventRetracted{Target: later})
		b.add(5, startOf(cycleA, 50))
		_, err := Replay(b.events)
		inv := assertCode(t, err, codeUnknownEvent)
		wantMessage(t, inv, "event "+string(later)+", which comes later in the log")
	})
	t.Run("a new target", func(t *testing.T) {
		b := dayLog(t)
		base := b.split()
		later := eid(len(b.events) + 2)
		b.add(0, event.EventRetracted{Target: later})
		b.add(5, startOf(cycleA, 50))
		_, err := Candidate(base, b.added(base))
		inv := assertCode(t, err, codeUnknownEvent)
		wantMessage(t, inv, "a new event that comes later in the log")
	})
}

func TestCheckCorrectionRefusesAnUnreadableEffectiveAt(t *testing.T) {
	b := dayLog(t)
	b.add(0, startOf(cycleA, 50))
	target := b.add(10, boostOf(cycleA, 5))
	m := mustReplay(t, b.events)
	var ev event.Event
	for _, e := range m.Log() {
		if e.ID == target {
			ev = e
		}
	}
	pr := CheckCorrection(ev, map[string]json.RawMessage{"effective_at": json.RawMessage(`"not an instant"`)})
	if pr == nil || pr.Identity || pr.Err == nil || !strings.Contains(pr.Err.Error(), "effective_at") {
		t.Errorf("CheckCorrection with an unreadable effective_at = %+v, want a problem with effective_at", pr)
	}
}

func TestFormatInstantGivesTheLocalDateWhenItDiffers(t *testing.T) {
	ny, tokyo := newZone(t, "America/New_York"), newZone(t, "Asia/Tokyo")
	cases := []struct {
		z    zone.Zone
		t    time.Time
		want string
	}{
		{ny, time.Date(2026, 10, 7, 14, 0, 0, 0, time.UTC), "2026-10-07T14:00:00.000Z (10:00 America/New_York)"},
		{ny, time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC), "2026-10-08T02:00:00.000Z (2026-10-07 22:00 America/New_York)"},
		{tokyo, time.Date(2026, 10, 7, 20, 0, 0, 0, time.UTC), "2026-10-07T20:00:00.000Z (2026-10-08 05:00 Asia/Tokyo)"},
	}
	for _, tc := range cases {
		if got := formatInstant(&tc.z, tc.t); got != tc.want {
			t.Errorf("formatInstant(%s, %s) = %q, want %q", tc.z.Name(), tc.t.Format(time.RFC3339), got, tc.want)
		}
	}
}

func TestMessagesUseTheZoneOfTheLatestDay(t *testing.T) {
	b := newLog(t)
	b.batch(-120, profileOf("work"), dayOf(day1))
	b.batch(-60, periodOf("day", day2, nil, "Asia/Tokyo", ""))
	b.add(0, startOf(cycleA, 50))
	b.add(0, stopOf(cycleA))
	_, err := Replay(b.events)
	inv := assertCode(t, err, codeStopNotAfterStart)
	wantMessage(t, inv, "Asia/Tokyo")
	if strings.Contains(inv.Message, "America/New_York") {
		t.Errorf("message %q uses the zone of the earlier day", inv.Message)
	}
}
