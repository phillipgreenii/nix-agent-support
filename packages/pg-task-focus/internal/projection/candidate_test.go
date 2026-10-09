package projection

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// asInvalidDirect is asInvalid for an error that MUST be the *Invalid itself,
// not one wrapping it: Candidate returns the finding of the replay unchanged.
func asInvalidDirect(t *testing.T, err error) *Invalid {
	t.Helper()
	if _, ok := err.(*Invalid); !ok {
		t.Fatalf("error = %v (%T), want the *Invalid itself", err, err)
	}
	return asInvalid(t, err)
}

func TestCandidateAcceptsValidTailAppend(t *testing.T) {
	b := dayLog(t)
	b.add(0, startOf(cycleA, 50))
	base := b.split()
	b.add(10, pauseOf(cycleA))
	b.add(20, resumeOf(cycleA))
	add := b.added(base)

	m, err := Candidate(base, add)
	if err != nil {
		t.Fatalf("Candidate: %v", err)
	}
	if got, want := m.Lines(), len(base)+len(add); got != want {
		t.Errorf("Lines() = %d, want %d", got, want)
	}
	assertSegments(t, mustCycle(t, m, cycleA), seg(0, 10), seg(20, open))

	// The adopted model is the one a replay of the whole log builds.
	full, err := Replay(m.Log())
	if err != nil {
		t.Fatalf("Replay of the candidate's log: %v", err)
	}
	if !reflect.DeepEqual(m.Domain(), full.Domain()) {
		t.Error("the candidate model differs from a replay of its own log")
	}

	t.Run("an empty addition is the replay of the base", func(t *testing.T) {
		m, err := Candidate(base, nil)
		if err != nil {
			t.Fatalf("Candidate: %v", err)
		}
		if m.Lines() != len(base) {
			t.Errorf("Lines() = %d, want %d", m.Lines(), len(base))
		}
	})
	t.Run("an empty base", func(t *testing.T) {
		m, err := Candidate(nil, base[:2])
		if err != nil {
			t.Fatalf("Candidate: %v", err)
		}
		if m.Lines() != 2 {
			t.Errorf("Lines() = %d, want 2", m.Lines())
		}
	})
}

func TestCandidateRejectsBackdatedOverlap(t *testing.T) {
	b := dayLog(t)
	startA := b.add(0, startOf(cycleA, 50))
	base := b.split()
	// Recorded at 20, effective at 10: it starts under the running cycle A.
	b.addAt(at(20), at(10), startOf(cycleB, 25))

	m, err := Candidate(base, b.added(base))
	if m != nil {
		t.Error("a rejected candidate returned a model")
	}
	inv := assertCode(t, err, codeAnotherCycleRunning)
	if !slices.Equal(inv.Cycles, []event.CycleID{cycleA}) || inv.Entity != "" {
		t.Errorf("Cycles %v, Entity %q: want only the stored cycle %s and no entity", inv.Cycles, inv.Entity, cycleA)
	}
	if !slices.Equal(inv.Events, []event.ID{startA}) {
		t.Errorf("Events = %v, want only the stored start %s", inv.Events, startA)
	}
	if !strings.Contains(inv.Message, "the new cycle") || strings.Contains(inv.Message, string(cycleB)) {
		t.Errorf("message %q must call the new cycle \"the new cycle\" and never cite %s", inv.Message, cycleB)
	}
	if !slices.ContainsFunc(inv.Instants, at(10).Equal) {
		t.Errorf("Instants = %v lack the new event's effective_at %s", inv.Instants, at(10))
	}
}

func TestCandidateRejectsCorrectionCreatingTwoRunningCycles(t *testing.T) {
	b := dayLog(t)
	startA := b.add(0, startOf(cycleA, 50))
	stopA := b.add(15, stopOf(cycleA))
	startB := b.add(20, startOf(cycleB, 25))
	base := b.split()
	// Moving the stop of A to 25 leaves A running while B runs from 20. The
	// correction is effective at 30, the instant it is recorded.
	correction := b.add(30, event.EventCorrected{Target: stopA, Fields: fieldsOf("effective_at", event.At(at(25)))})

	_, err := Candidate(base, b.added(base))
	inv := assertCode(t, err, codeAnotherCycleRunning)
	if !sameCycles(inv.Cycles, cycleA, cycleB) {
		t.Errorf("Cycles = %v, want both stored cycles %s and %s", inv.Cycles, cycleA, cycleB)
	}
	for _, id := range []event.ID{startA, startB} { // the segments of A and B that overlap are opened by their starts
		if !slices.Contains(inv.Events, id) {
			t.Errorf("Events = %v, want the stored event %s", inv.Events, id)
		}
	}
	if slices.Contains(inv.Events, stopA) || strings.Contains(inv.Message, string(stopA)) {
		t.Errorf("the stop %s is moved by the correction, not a party to the overlap (%v, %q)", stopA, inv.Events, inv.Message)
	}
	if slices.Contains(inv.Events, correction) || strings.Contains(inv.Message, string(correction)) {
		t.Errorf("the correction %s is the new event: it is never listed or cited (%v, %q)", correction, inv.Events, inv.Message)
	}
	if !strings.Contains(inv.Message, "the new event") || !slices.ContainsFunc(inv.Instants, at(30).Equal) {
		t.Errorf("message %q, Instants %v: want the correction as the new event with its effective_at %s", inv.Message, inv.Instants, at(30))
	}
}

func TestCandidateRejectsRetractionLeavingTaskWithoutPeriod(t *testing.T) {
	b := newLog(t)
	b.batch(0, profileOf("work"))
	periods := b.batch(5, dayOf(day1))
	b.batch(10, b.daily("post-plan", day1))
	materialization := b.members(bid(3))[0]
	base := b.split()
	retraction := b.add(20, event.EventRetracted{TargetBatch: periods})

	_, err := Candidate(base, b.added(base))
	inv := assertCode(t, err, codeTaskWithoutPeriod)
	if inv.Entity != string(taskA) || !slices.Equal(inv.Events, []event.ID{materialization}) {
		t.Errorf("Entity %q, Events %v: want %s and the stored materialization %s", inv.Entity, inv.Events, taskA, materialization)
	}
	if strings.Contains(inv.Message, string(retraction)) {
		t.Errorf("message %q cites the new event %s", inv.Message, retraction)
	}
}

func TestCandidateAssignsLogPositions(t *testing.T) {
	b := dayLog(t)
	b.add(0, startOf(cycleA, 50))
	for i := range b.events { // a stored log carries the line each event was read from
		b.events[i].Line = i + 1
	}
	base := b.split()
	b.add(10, pauseOf(cycleA))
	b.add(10, resumeOf(cycleA)) // the same effective instant as the pause
	add := b.added(base)

	m, err := Candidate(base, add)
	if err != nil {
		t.Fatalf("Candidate: %v", err)
	}
	log := m.Log()
	if len(log) != len(base)+len(add) {
		t.Fatalf("Log() has %d events, want %d", len(log), len(base)+len(add))
	}
	for i, e := range log {
		if want := i + 1; e.Line != want {
			t.Errorf("Log()[%d].Line = %d, want %d: the base keeps its lines and the added events follow them", i, e.Line, want)
		}
		if i < len(base) && e.ID != base[i].ID || i >= len(base) && e.ID != add[i-len(base)].ID {
			t.Errorf("Log()[%d] is event %s, want the base followed by the added events in order", i, e.ID)
		}
	}
	// The pause is logged before the resume, and equal instants follow the log.
	assertSegments(t, mustCycle(t, m, cycleA), seg(0, 10), seg(10, open))

	t.Run("the reverse order at one instant is a different timeline", func(t *testing.T) {
		_, err := Candidate(base, []event.Event{add[1], add[0]})
		_ = assertCode(t, err, codeCycleSegmentsOverlap) // the resume now lands on a running cycle
	})
}

func TestCandidateDoesNotMutateBase(t *testing.T) {
	b := dayLog(t)
	b.add(0, startOf(cycleA, 50))
	// A base with spare capacity: an append onto it would write past its length.
	base := slices.Grow(b.split(), 8)
	spare := base[len(base):cap(base)]
	b.add(10, pauseOf(cycleA))
	b.add(20, resumeOf(cycleA))
	add := b.added(b.events[:len(base)])
	wantBase, wantAdd := slices.Clone(base), slices.Clone(add)

	first, err := Candidate(base, add[:1])
	if err != nil {
		t.Fatalf("Candidate: %v", err)
	}
	wantLog, wantDomain := slices.Clone(first.Log()), first.Domain()

	second, err := Candidate(base, add)
	if err != nil {
		t.Fatalf("second Candidate: %v", err)
	}
	if _, err := Candidate(base, add[1:]); err == nil {
		t.Fatal("a resume of a running cycle was accepted")
	}

	if !reflect.DeepEqual(base, wantBase) {
		t.Error("Candidate changed the base slice")
	}
	if !reflect.DeepEqual(add, wantAdd) {
		t.Error("Candidate changed the added events, whose Line it assigns on a copy")
	}
	for i, e := range spare {
		if !reflect.DeepEqual(e, event.Event{}) {
			t.Fatalf("Candidate wrote into the spare capacity of the base at %d", i)
		}
	}
	if !reflect.DeepEqual(first.Log(), wantLog) || !reflect.DeepEqual(first.Domain(), wantDomain) {
		t.Error("a later Candidate changed a model returned earlier")
	}
	if second.Lines() != len(base)+2 || first.Lines() != len(base)+1 {
		t.Errorf("Lines() = %d and %d, want %d and %d", first.Lines(), second.Lines(), len(base)+1, len(base)+2)
	}

	t.Run("the log the model returns is a copy", func(t *testing.T) {
		log := first.Log()
		log[0].ID = ""
		if first.Log()[0].ID == "" {
			t.Error("writing to the slice Log() returned changed the model")
		}
	})
}

func TestInvalidNamesCodeEntityEventsAndInstants(t *testing.T) {
	t.Run("a correction", func(t *testing.T) {
		b := dayLog(t)
		b.add(0, startOf(cycleA, 50))
		b.add(10, pauseOf(cycleA))
		resume := b.add(20, resumeOf(cycleA))
		stop := b.add(30, stopOf(cycleA))
		base := b.split()
		b.add(40, event.EventCorrected{Target: stop, Fields: fieldsOf("effective_at", event.At(at(15)))})

		_, err := Candidate(base, b.added(base))
		inv := asInvalidDirect(t, err)
		if inv.Code != codeCycleStopped || inv.Entity != string(cycleA) {
			t.Errorf("Code %q, Entity %q, want %q and %s", inv.Code, inv.Entity, codeCycleStopped, cycleA)
		}
		if !slices.Contains(inv.Events, resume) || !slices.Contains(inv.Events, stop) {
			t.Errorf("Events = %v, want the resume %s and the moved stop %s", inv.Events, resume, stop)
		}
		for _, want := range []int{15, 20} {
			if !slices.ContainsFunc(inv.Instants, at(want).Equal) {
				t.Errorf("Instants = %v lack %s", inv.Instants, at(want))
			}
		}
		if !strings.Contains(inv.Message, "America/New_York") {
			t.Errorf("message %q names no zone though a day period exists", inv.Message)
		}
	})
	t.Run("a retraction that is the only cause", func(t *testing.T) {
		b := newLog(t)
		b.batch(0, profileOf("work"))
		periods := b.batch(5, dayOf(day1))
		b.batch(10, b.daily("post-plan", day1))
		base := b.split()
		b.add(20, event.EventRetracted{TargetBatch: periods})

		_, err := Candidate(base, b.added(base))
		inv := asInvalidDirect(t, err)
		const tail = " once the new event effective at "
		if !strings.Contains(inv.Message, tail) || !strings.HasSuffix(inv.Message, " is applied.") {
			t.Errorf("message %q: want it to end %q... is applied.", inv.Message, tail)
		}
		if !slices.ContainsFunc(inv.Instants, at(20).Equal) {
			t.Errorf("Instants = %v lack the new event's effective_at %s", inv.Instants, at(20))
		}
	})
}

func TestCandidateAndReplayAgreeOnCodes(t *testing.T) {
	// Each case is one log: its last event or events are the ones a request
	// would add. A stored log replayed alone and the same events arriving as an
	// addition get the same code.
	tests := []struct {
		name   string
		code   Code
		stored Code // the code plain Replay gives when it differs
		build  func(t *testing.T) (base, add []event.Event)
	}{
		{"a backdated start under a running cycle", codeAnotherCycleRunning, "", func(t *testing.T) ([]event.Event, []event.Event) {
			b := dayLog(t)
			b.add(0, startOf(cycleA, 50))
			base := b.split()
			b.addAt(at(20), at(10), startOf(cycleB, 25))
			return base, b.added(base)
		}},
		{"a retraction of the only period", codeTaskWithoutPeriod, "", func(t *testing.T) ([]event.Event, []event.Event) {
			b := newLog(t)
			b.batch(0, profileOf("work"))
			periods := b.batch(5, dayOf(day1))
			b.batch(10, b.daily("post-plan", day1))
			base := b.split()
			b.add(20, event.EventRetracted{TargetBatch: periods})
			return base, b.added(base)
		}},
		{"a second resolution", codeTaskAlreadyResolved, "", func(t *testing.T) ([]event.Event, []event.Event) {
			b, id := bootstrapped(t)
			b.add(100, event.TaskCompleted{TaskID: id})
			base := b.split()
			b.add(110, event.TaskCompleted{TaskID: id})
			return base, b.added(base)
		}},
		{"a pause of a paused cycle", codeCycleSegmentsOverlap, "", func(t *testing.T) ([]event.Event, []event.Event) {
			b := dayLog(t)
			b.add(0, startOf(cycleA, 50))
			b.add(10, pauseOf(cycleA))
			base := b.split()
			b.add(20, pauseOf(cycleA))
			return base, b.added(base)
		}},
		{"a break closing at the stop, among the added events", codeBreakEndsAtStop, codeCycleStopped, func(t *testing.T) ([]event.Event, []event.Event) {
			b := dayLog(t)
			b.add(0, startOf(cycleA, 50))
			b.add(40, stopOf(cycleA))
			base := b.split()
			b.backfill(50, 30, 40, cycleA)
			return base, b.added(base)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, add := tt.build(t)
			_, err := Candidate(base, add)
			if got := assertCode(t, err, tt.code); got.Code != tt.code {
				t.Fatalf("Candidate: Code = %q, want %q", got.Code, tt.code)
			}
			want := tt.stored
			if want == "" {
				want = tt.code
			}
			_, err = Replay(slices.Concat(base, add))
			_ = assertCode(t, err, want)
		})
	}
}
