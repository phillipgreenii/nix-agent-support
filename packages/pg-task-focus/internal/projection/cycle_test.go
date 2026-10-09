package projection

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

const (
	cycleC = event.CycleID("01J9ZZZZZZZZZZZZZZZZZZZZZC")
	cycleD = event.CycleID("01J9ZZZZZZZZZZZZZZZZZZZZZD")
	cycleE = event.CycleID("01J9ZZZZZZZZZZZZZZZZZZZZZE")
)

func startOf(id event.CycleID, planned int) event.Payload {
	return event.CycleStarted{CycleID: id, Type: "deep-work", Title: "Deep work", PlannedMinutes: planned}
}

func interruptOf(id, of event.CycleID, planned int) event.Payload {
	return event.CycleStarted{CycleID: id, Type: "notifications", Title: "Notifications", PlannedMinutes: planned, Interrupts: of}
}

func pauseOf(id event.CycleID) event.Payload  { return event.CyclePaused{CycleID: id} }
func resumeOf(id event.CycleID) event.Payload { return event.CycleResumed{CycleID: id} }
func stopOf(id event.CycleID) event.Payload   { return event.CycleStopped{CycleID: id} }

func boostOf(id event.CycleID, minutes int) event.Payload {
	return event.CycleBoosted{CycleID: id, Minutes: minutes}
}

// noteOf is an annotation with a note and alternating keys and values.
func noteOf(id event.CycleID, note string, kv ...string) event.Payload {
	p := event.CycleAnnotated{CycleID: id, Note: note}
	for i := 0; i < len(kv); i += 2 {
		p.KV = append(p.KV, event.KV{Key: kv[i], Value: kv[i+1]})
	}
	return p
}

func pauseIn(id event.CycleID) func(event.ID) event.Payload {
	return func(bt event.ID) event.Payload { return event.CyclePaused{CycleID: id, Batch: bt} }
}

func resumeIn(id event.CycleID) func(event.ID) event.Payload {
	return func(bt event.ID) event.Payload { return event.CycleResumed{CycleID: id, Batch: bt} }
}

// switchTo appends a switch at min: from, the running cycle, pauses and to
// resumes, in one batch.
func (b *logb) switchTo(min int, from, to event.CycleID) event.ID {
	return b.batch(min, pauseIn(from), resumeIn(to))
}

// backfill appends a break of cycle id from from to to, recorded at recorded.
func (b *logb) backfill(recorded, from, to int, id event.CycleID) event.ID {
	b.batches++
	bt := bid(b.batches)
	b.addAt(at(recorded), at(from), event.CyclePaused{CycleID: id, Batch: bt})
	b.addAt(at(recorded), at(to), event.CycleResumed{CycleID: id, Batch: bt})
	b.addAt(at(recorded), at(recorded), event.BatchCommitted{Batch: bt})
	return bt
}

// dayLog is a log whose first batch, two hours before the epoch, sets the
// profile and the day, so every message names the day period's zone.
func dayLog(t *testing.T) *logb {
	t.Helper()
	b := newLog(t)
	b.batch(-120, profileOf("work"), dayOf(day1))
	return b
}

func (b *logb) fork(t *testing.T) *logb {
	return &logb{t: t, events: slices.Clone(b.events), batches: b.batches}
}

func mustCycle(t *testing.T, m *Model, id event.CycleID) Cycle {
	t.Helper()
	c, ok := m.Cycle(id)
	if !ok {
		t.Fatalf("Cycle(%s) not found", id)
	}
	return c
}

// seg is a closed segment from a to b minutes after the epoch.
func seg(a, b int) [2]int { return [2]int{a, b} }

// open is the end of an open segment in a segs list.
const open = 1 << 30

// assertSegments checks a cycle's segments against [start, end] pairs in
// minutes, with end open for an open segment.
func assertSegments(t *testing.T, c Cycle, want ...[2]int) {
	t.Helper()
	var got [][2]int
	for _, s := range c.Segments {
		end := open
		if s.End != nil {
			end = int(s.End.Sub(epoch) / time.Minute)
		}
		got = append(got, [2]int{int(s.Start.Sub(epoch) / time.Minute), end})
	}
	if !slices.Equal(got, want) {
		t.Errorf("cycle %s segments = %v, want %v", c.ID, got, want)
	}
}

func assertCode(t *testing.T, err error, code Code) *Invalid {
	t.Helper()
	inv := asInvalid(t, err)
	if inv.Code != code {
		t.Fatalf("Code = %q (%s), want %q", inv.Code, inv.Message, code)
	}
	return inv
}

func TestStateTable(t *testing.T) {
	// Cycle A starts at 10 and, for the paused and stopped states, is paused
	// or stopped at 20. The operation is the request, at 30; for "not
	// started" it is at 5, before the start. Where an operation needs another
	// cycle running, B starts at 25 (at 0 for "not started").
	setup := map[CycleStatus]func(b *logb){
		NotStarted: func(b *logb) { b.add(10, startOf(cycleA, 25)) },
		Running:    func(b *logb) { b.add(10, startOf(cycleA, 25)) },
		Paused:     func(b *logb) { b.add(10, startOf(cycleA, 25)); b.add(20, pauseOf(cycleA)) },
		Stopped:    func(b *logb) { b.add(10, startOf(cycleA, 25)); b.add(20, stopOf(cycleA)) },
	}
	when := func(s CycleStatus) int {
		if s == NotStarted {
			return 5
		}
		return 30
	}
	ops := map[string]func(b *logb, min int){
		"pause":    func(b *logb, min int) { b.add(min, pauseOf(cycleA)) },
		"resume":   func(b *logb, min int) { b.add(min, resumeOf(cycleA)) },
		"boost":    func(b *logb, min int) { b.add(min, boostOf(cycleA, 5)) },
		"stop":     func(b *logb, min int) { b.add(min, stopOf(cycleA)) },
		"annotate": func(b *logb, min int) { b.add(min, noteOf(cycleA, "notes")) },
		"switch": func(b *logb, min int) {
			b.add(min-5, startOf(cycleB, 25))
			b.switchTo(min, cycleB, cycleA)
		},
		"resume while another cycle runs": func(b *logb, min int) {
			b.add(min-5, startOf(cycleB, 25))
			b.add(min, resumeOf(cycleA))
		},
	}
	// want is the code candidate replay gives, or the status of A after the
	// operation when it is accepted. A no-op cell is the command layer's: it
	// writes no event, and the events it would mean are refused here.
	tests := []struct {
		state CycleStatus
		op    string
		code  Code
		after CycleStatus
		noop  bool
	}{
		{NotStarted, "pause", codeCycleEventBeforeStart, "", false},
		{NotStarted, "resume", codeCycleEventBeforeStart, "", false},
		{NotStarted, "boost", codeCycleEventBeforeStart, "", false},
		{NotStarted, "stop", codeCycleEventBeforeStart, "", false},
		{NotStarted, "annotate", codeCycleEventBeforeStart, "", false},
		{NotStarted, "switch", codeCycleEventBeforeStart, "", false},

		{Running, "pause", "", Paused, false},
		{Running, "resume", codeCycleSegmentsOverlap, "", true},
		{Running, "boost", "", Running, false},
		{Running, "stop", "", Stopped, false},
		{Running, "annotate", "", Running, false},

		{Paused, "pause", codeCycleSegmentsOverlap, "", true},
		{Paused, "resume", "", Running, false},
		{Paused, "resume while another cycle runs", codeAnotherCycleRunning, "", false},
		{Paused, "boost", "", Paused, false},
		{Paused, "stop", "", Stopped, false},
		{Paused, "annotate", "", Paused, false},
		{Paused, "switch", "", Running, false},

		{Stopped, "pause", codeCycleStopped, "", false},
		{Stopped, "resume", codeCycleStopped, "", false},
		{Stopped, "boost", codeCycleStopped, "", false},
		{Stopped, "stop", codeCycleStopped, "", true},
		{Stopped, "annotate", "", Stopped, false},
		{Stopped, "switch", codeCycleStopped, "", false},
	}
	// A switch to the running cycle is the command layer's no-op and has no
	// events of its own (it would pause and resume the focus), so its cell
	// is not replayed.
	for _, tt := range tests {
		t.Run(string(tt.state)+"/"+tt.op, func(t *testing.T) {
			b := dayLog(t)
			setup[tt.state](b)
			base := b.split()
			ops[tt.op](b, when(tt.state))
			m, err := Candidate(base, b.added(base))
			if tt.code != "" {
				inv := assertCode(t, err, tt.code)
				if inv.Entity != string(cycleA) && tt.code != codeAnotherCycleRunning {
					t.Errorf("Entity = %q, want %s", inv.Entity, cycleA)
				}
				return
			}
			if err != nil {
				t.Fatalf("candidate replay: %v", err)
			}
			if got := mustCycle(t, m, cycleA).Status; got != tt.after {
				t.Errorf("Status = %q, want %q", got, tt.after)
			}
		})
	}
}

func TestInterruptPausesNamedCycleAtItsEffectiveAt(t *testing.T) {
	t.Run("a backdated interrupt pauses at its own effective_at", func(t *testing.T) {
		b := dayLog(t)
		b.add(0, startOf(cycleA, 50))
		b.addAt(at(40), at(30), interruptOf(cycleB, cycleA, 15))
		m := mustReplay(t, b.events)
		a, bc := mustCycle(t, m, cycleA), mustCycle(t, m, cycleB)
		assertSegments(t, a, seg(0, 30))
		assertSegments(t, bc, seg(30, open))
		if a.Status != Paused || bc.Status != Running {
			t.Errorf("statuses A %q, B %q, want paused and running", a.Status, bc.Status)
		}
		if a.InterruptedBy != cycleB {
			t.Errorf("A.InterruptedBy = %q, want %s", a.InterruptedBy, cycleB)
		}
	})
	t.Run("the pause is strictly later than the last resume", func(t *testing.T) {
		b := dayLog(t)
		b.add(0, startOf(cycleA, 50))
		b.add(10, pauseOf(cycleA))
		b.add(20, resumeOf(cycleA))
		b.add(25, interruptOf(cycleB, cycleA, 15))
		m := mustReplay(t, b.events)
		assertSegments(t, mustCycle(t, m, cycleA), seg(0, 10), seg(20, 25))
	})
	for _, tt := range []struct {
		name string
		at   int
	}{{"at the instant of the last resume", 20}, {"at the instant of the start", 0}} {
		t.Run(tt.name+" is empty_running_segment", func(t *testing.T) {
			b := dayLog(t)
			b.add(0, startOf(cycleA, 50))
			if tt.at == 20 {
				b.add(10, pauseOf(cycleA))
				b.add(20, resumeOf(cycleA))
			}
			base := b.split()
			b.add(tt.at, interruptOf(cycleB, cycleA, 15))
			_, err := Candidate(base, b.added(base))
			inv := assertCode(t, err, codeEmptyRunningSegment)
			if inv.Entity != string(cycleA) {
				t.Errorf("Entity = %q, want the interrupted cycle %s", inv.Entity, cycleA)
			}
		})
	}
}

// interruptStack is the chain of interrupted cycles below the running one,
// read from the InterruptedBy links: the running cycle first.
func interruptStack(m *Model) []event.CycleID {
	run, ok := m.Running()
	if !ok {
		return nil
	}
	stack := []event.CycleID{run.ID}
	for {
		var next event.CycleID
		for _, c := range m.Dimmed() {
			if c.InterruptedBy == stack[len(stack)-1] {
				next = c.ID
				break
			}
		}
		if next == "" {
			return stack
		}
		stack = append(stack, next)
	}
}

func TestInterruptStack(t *testing.T) {
	b := dayLog(t)
	b.add(0, startOf(cycleA, 50))
	b.add(10, interruptOf(cycleB, cycleA, 15))
	b.add(20, interruptOf(cycleC, cycleB, 15))
	m := mustReplay(t, b.events)
	if got, want := interruptStack(m), []event.CycleID{cycleC, cycleB, cycleA}; !slices.Equal(got, want) {
		t.Errorf("stack = %v, want %v", got, want)
	}
	if a, bc, c := mustCycle(t, m, cycleA), mustCycle(t, m, cycleB), mustCycle(t, m, cycleC); a.InterruptedBy != cycleB || bc.InterruptedBy != cycleC || c.InterruptedBy != "" {
		t.Errorf("links A->%q B->%q C->%q, want A->B, B->C, none for C", a.InterruptedBy, bc.InterruptedBy, c.InterruptedBy)
	}

	b.add(30, stopOf(cycleC))
	m = mustReplay(t, b.events)
	if bc := mustCycle(t, m, cycleB); bc.InterruptedBy != cycleC {
		t.Errorf("after the stop of C, B.InterruptedBy = %q, want %s: the stop of the displacing cycle keeps the link", bc.InterruptedBy, cycleC)
	}
	b.add(30, resumeOf(cycleB))
	m = mustReplay(t, b.events)
	if a, bc := mustCycle(t, m, cycleA), mustCycle(t, m, cycleB); bc.InterruptedBy != "" || a.InterruptedBy != cycleB {
		t.Errorf("after B resumes, links A->%q B->%q, want A->B and none for B", a.InterruptedBy, bc.InterruptedBy)
	}
	if got, want := interruptStack(m), []event.CycleID{cycleB, cycleA}; !slices.Equal(got, want) {
		t.Errorf("stack = %v, want %v", got, want)
	}
}

func TestTwoRunningCyclesAreAnotherCycleRunning(t *testing.T) {
	t.Run("a backdated start under a running cycle", func(t *testing.T) {
		b := dayLog(t)
		startA := b.add(0, startOf(cycleA, 50))
		startB := b.addAt(at(20), at(10), startOf(cycleB, 25))
		inv := assertCode(t, mustFail(Replay(b.events)), codeAnotherCycleRunning)
		if !sameCycles(inv.Cycles, cycleA, cycleB) {
			t.Errorf("Cycles = %v, want both %s and %s", inv.Cycles, cycleA, cycleB)
		}
		if inv.Entity != string(cycleB) || !slices.Contains(inv.Events, startA) || !slices.Contains(inv.Events, startB) {
			t.Errorf("Entity %q, Events %v: want the backdated cycle %s and both starts", inv.Entity, inv.Events, cycleB)
		}
		if !slices.ContainsFunc(inv.Instants, at(10).Equal) {
			t.Errorf("Instants = %v lack the instant both run, %s", inv.Instants, at(10))
		}

		t.Run("through candidate replay the new cycle is not listed", func(t *testing.T) {
			base := b.events[:len(b.events)-1]
			_, err := Candidate(base, b.events[len(b.events)-1:])
			inv := assertCode(t, err, codeAnotherCycleRunning)
			if !slices.Equal(inv.Cycles, []event.CycleID{cycleA}) || inv.Entity != "" {
				t.Errorf("Cycles %v, Entity %q: want only the stored cycle %s and no entity", inv.Cycles, inv.Entity, cycleA)
			}
			if !strings.Contains(inv.Message, "the new cycle") || strings.Contains(inv.Message, string(cycleB)) {
				t.Errorf("message %q does not call the new cycle the new cycle", inv.Message)
			}
		})
	})
	t.Run("a correction that moves a stop before a later resume", func(t *testing.T) {
		b := dayLog(t)
		b.add(0, startOf(cycleA, 50))
		b.add(10, pauseOf(cycleA))
		resume := b.add(20, resumeOf(cycleA))
		stop := b.add(30, stopOf(cycleA))
		base := b.split()
		b.add(40, event.EventCorrected{Target: stop, Fields: fieldsOf("effective_at", event.At(at(15)))})
		_, err := Candidate(base, b.added(base))
		inv := assertCode(t, err, codeCycleStopped)
		if !slices.Contains(inv.Events, resume) || !slices.Contains(inv.Events, stop) {
			t.Errorf("Events = %v, want the resume %s and the moved stop %s", inv.Events, resume, stop)
		}
		if !strings.Contains(inv.Message, "the new event") {
			t.Errorf("message %q does not name the correction as the new event", inv.Message)
		}
	})
}

func mustFail(_ *Model, err error) error { return err }

func sameCycles(got []event.CycleID, want ...event.CycleID) bool {
	g, w := slices.Clone(got), slices.Clone(want)
	slices.Sort(g)
	slices.Sort(w)
	return slices.Equal(g, w)
}

func TestResumeOfStoppedCycleIsCycleStopped(t *testing.T) {
	b := dayLog(t)
	b.add(0, startOf(cycleA, 50))
	stop := b.add(10, stopOf(cycleA))
	base := b.split()
	resume := b.add(20, resumeOf(cycleA))

	inv := assertCode(t, mustFail(Replay(b.events)), codeCycleStopped)
	if !slices.Equal(inv.Events, []event.ID{resume, stop}) || inv.Entity != string(cycleA) {
		t.Errorf("Events %v, Entity %q, want [%s %s] and %s", inv.Events, inv.Entity, resume, stop, cycleA)
	}
	_, err := Candidate(base, b.added(base))
	inv = assertCode(t, err, codeCycleStopped)
	if !slices.Equal(inv.Events, []event.ID{stop}) {
		t.Errorf("Events = %v, want only the stored stop %s", inv.Events, stop)
	}
}

// stoppedAt60 is a cycle that started at 0 and stopped at 60.
func stoppedAt60(t *testing.T) (*logb, event.ID) {
	b := dayLog(t)
	b.add(0, startOf(cycleA, 50))
	return b, b.add(60, stopOf(cycleA))
}

func TestBackfilledBreakEndingAtStopInstantIsBreakEndsAtStop(t *testing.T) {
	// The break's closing resume and the stop share the instant 60; the stop
	// is earlier in the log, so it sorts first and the resume lands on a
	// stopped cycle.
	b, stop := stoppedAt60(t)
	base := b.split()
	b.backfill(70, 30, 60, cycleA)
	_, err := Candidate(base, b.added(base))
	inv := assertCode(t, err, codeBreakEndsAtStop)
	if !strings.Contains(inv.Message, "end at") {
		t.Errorf("message %q does not point at end at", inv.Message)
	}
	if !slices.Equal(inv.Events, []event.ID{stop}) || inv.Entity != string(cycleA) {
		t.Errorf("Events %v, Entity %q, want only the stored stop %s and %s", inv.Events, inv.Entity, stop, cycleA)
	}
}

func TestBackfilledBreakInAStoredLogIsCycleStopped(t *testing.T) {
	b, stop := stoppedAt60(t)
	batch := b.backfill(70, 30, 60, cycleA)
	inv := assertCode(t, mustFail(Replay(b.events)), codeCycleStopped)
	if !strings.Contains(inv.Message, string(batch)) {
		t.Errorf("message %q does not name the break's batch %s", inv.Message, batch)
	}
	if !slices.Contains(inv.Events, stop) {
		t.Errorf("Events = %v lack the stop %s", inv.Events, stop)
	}
}

func TestBackfilledBreakStartingAtOrAfterTheStopIsBreakEndsAtStop(t *testing.T) {
	for _, tt := range []struct {
		name     string
		from, to int
	}{
		{"starting at the stop", 60, 70},
		{"starting after the stop", 65, 70},
		{"starting before and ending after the stop", 30, 75},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := stoppedAt60(t)
			base := b.split()
			b.backfill(80, tt.from, tt.to, cycleA)
			_, err := replay(append(slices.Clone(base), b.added(base)...), len(base))
			if inv := assertCode(t, err, codeBreakEndsAtStop); !strings.Contains(inv.Message, "end at") {
				t.Errorf("message %q does not point at end at", inv.Message)
			}
		})
	}
}

func TestStopNotAfterStart(t *testing.T) {
	b := dayLog(t)
	start := b.add(10, startOf(cycleA, 50))
	base := b.split()
	stop := b.add(10, stopOf(cycleA))
	inv := assertCode(t, mustFail(Replay(b.events)), codeStopNotAfterStart)
	if !slices.Contains(inv.Events, start) || !slices.Contains(inv.Events, stop) {
		t.Errorf("Events = %v, want the start and the stop", inv.Events)
	}
	_, err := Candidate(base, b.added(base))
	assertCode(t, err, codeStopNotAfterStart)
}

func TestStopAtTheInstantOfAResumeIsEmptyRunningSegment(t *testing.T) {
	b := dayLog(t)
	b.add(0, startOf(cycleA, 50))
	b.add(10, pauseOf(cycleA))
	resume := b.add(20, resumeOf(cycleA))
	base := b.split()
	b.add(20, stopOf(cycleA))
	_, err := Candidate(base, b.added(base))
	inv := assertCode(t, err, codeEmptyRunningSegment)
	if !slices.Equal(inv.Events, []event.ID{resume}) {
		t.Errorf("Events = %v, want the resume %s that opened the segment", inv.Events, resume)
	}
}

func TestEventBeforeCycleStartIsCycleEventBeforeStart(t *testing.T) {
	// A starts at 10 and runs a full life after it; each subtest corrects
	// one of its events to 5, before the start.
	b := dayLog(t)
	b.add(10, startOf(cycleA, 50))
	ids := map[string]event.ID{
		"pause":      b.add(20, pauseOf(cycleA)),
		"resume":     b.add(30, resumeOf(cycleA)),
		"boost":      b.add(35, boostOf(cycleA, 5)),
		"stop":       b.add(40, stopOf(cycleA)),
		"annotation": b.add(45, noteOf(cycleA, "notes")),
	}
	mustReplay(t, b.events)
	for _, name := range []string{"pause", "resume", "boost", "stop", "annotation"} {
		t.Run("a "+name+" corrected to before the start", func(t *testing.T) {
			c := b.fork(t)
			base := c.split()
			c.add(50, event.EventCorrected{Target: ids[name], Fields: fieldsOf("effective_at", event.At(at(5)))})
			_, err := Candidate(base, c.added(base))
			inv := assertCode(t, err, codeCycleEventBeforeStart)
			if !slices.Contains(inv.Events, ids[name]) || inv.Entity != string(cycleA) {
				t.Errorf("Events %v, Entity %q, want the moved event %s and %s", inv.Events, inv.Entity, ids[name], cycleA)
			}
		})
	}
	t.Run("a cycle whose start is retracted while later events remain", func(t *testing.T) {
		c := dayLog(t)
		start := c.add(10, startOf(cycleA, 50))
		pause := c.add(20, pauseOf(cycleA))
		base := c.split()
		c.add(30, event.EventRetracted{Target: start})
		_, err := Candidate(base, c.added(base))
		inv := assertCode(t, err, codeCycleEventBeforeStart)
		if !slices.Equal(inv.Events, []event.ID{pause}) || !strings.Contains(inv.Message, "the new event") {
			t.Errorf("Events %v, message %q: want the orphaned pause and the retraction as the new event", inv.Events, inv.Message)
		}
	})
}

func TestInterruptOfACycleThatIsNotRunning(t *testing.T) {
	tests := []struct {
		name  string
		setup func(b *logb)
	}{
		{"the named cycle is paused", func(b *logb) { b.add(0, startOf(cycleA, 50)); b.add(10, pauseOf(cycleA)) }},
		{"the named cycle is stopped", func(b *logb) { b.add(0, startOf(cycleA, 50)); b.add(10, stopOf(cycleA)) }},
		{"the named cycle starts later", func(b *logb) { b.add(40, startOf(cycleA, 50)) }},
		{"the named cycle never started", func(*logb) {}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := dayLog(t)
			tt.setup(b)
			b.add(30, interruptOf(cycleB, cycleA, 15))
			inv := assertCode(t, mustFail(Replay(b.events)), codeInterruptedCycleNotRunning)
			if inv.Entity != string(cycleB) || !strings.Contains(inv.Message, string(cycleA)) {
				t.Errorf("Entity %q, message %q: want the interrupting cycle %s and the message naming %s", inv.Entity, inv.Message, cycleB, cycleA)
			}
		})
	}
}

func TestPauseOfAPausedCycleIsCycleSegmentsOverlap(t *testing.T) {
	t.Run("a pause of a paused cycle", func(t *testing.T) {
		b := dayLog(t)
		b.add(0, startOf(cycleA, 50))
		first := b.add(10, pauseOf(cycleA))
		second := b.add(20, pauseOf(cycleA))
		inv := assertCode(t, mustFail(Replay(b.events)), codeCycleSegmentsOverlap)
		if !slices.Contains(inv.Events, first) || !slices.Contains(inv.Events, second) {
			t.Errorf("Events = %v, want both pauses", inv.Events)
		}
	})
	t.Run("a resume of a running cycle", func(t *testing.T) {
		b := dayLog(t)
		b.add(0, startOf(cycleA, 50))
		b.add(10, resumeOf(cycleA))
		assertCode(t, mustFail(Replay(b.events)), codeCycleSegmentsOverlap)
	})
	t.Run("a break inside an existing pause", func(t *testing.T) {
		b := dayLog(t)
		b.add(0, startOf(cycleA, 50))
		b.add(10, pauseOf(cycleA))
		b.add(40, resumeOf(cycleA))
		base := b.split()
		b.backfill(50, 20, 30, cycleA)
		_, err := Candidate(base, b.added(base))
		assertCode(t, err, codeCycleSegmentsOverlap)
	})
	t.Run("a second start of one cycle", func(t *testing.T) {
		b := dayLog(t)
		b.add(0, startOf(cycleA, 50))
		b.add(10, startOf(cycleA, 50))
		assertCode(t, mustFail(Replay(b.events)), codeCycleSegmentsOverlap)
	})
}

func TestStatusAtGivesTheStateAtAnyInstant(t *testing.T) {
	b := dayLog(t)
	b.add(10, startOf(cycleA, 50))
	b.add(20, pauseOf(cycleA))
	b.add(30, resumeOf(cycleA))
	b.add(40, stopOf(cycleA))
	c := mustCycle(t, mustReplay(t, b.events), cycleA)
	for _, tt := range []struct {
		min  int
		want CycleStatus
	}{
		{5, NotStarted},
		{10, Running},
		{15, Running},
		{20, Paused},
		{25, Paused},
		{30, Running},
		{35, Running},
		{40, Stopped},
		{100, Stopped},
	} {
		if got := c.StatusAt(at(tt.min)); got != tt.want {
			t.Errorf("StatusAt(%d) = %q, want %q", tt.min, got, tt.want)
		}
	}
	if c.Status != Stopped || c.StoppedAt == nil || !c.StoppedAt.Equal(at(40)) {
		t.Errorf("Status %q, StoppedAt %v, want stopped at %s", c.Status, c.StoppedAt, at(40))
	}
	if got := c.StatusAt(at(10).Add(-time.Millisecond)); got != NotStarted {
		t.Errorf("StatusAt one millisecond before the start = %q, want not_started", got)
	}
}

func TestAnnotationReplacesAndWorksAfterStop(t *testing.T) {
	b := dayLog(t)
	b.add(0, startOf(cycleA, 50))
	b.add(5, noteOf(cycleA, "first", "topic", "plan"))
	b.add(10, stopOf(cycleA))
	b.add(20, noteOf(cycleA, "second", "pr", "https://example.test/pr/1", "pr", "https://example.test/pr/2"))
	c := mustCycle(t, mustReplay(t, b.events), cycleA)
	want := []event.KV{{Key: "pr", Value: "https://example.test/pr/1"}, {Key: "pr", Value: "https://example.test/pr/2"}}
	if c.Note != "second" || !slices.Equal(c.KV, want) {
		t.Errorf("Note %q, KV %v, want the latest annotation whole: %q and %v", c.Note, c.KV, "second", want)
	}

	b.add(30, noteOf(cycleA, "", "topic", "review"))
	c = mustCycle(t, mustReplay(t, b.events), cycleA)
	if c.Note != "" || !slices.Equal(c.KV, []event.KV{{Key: "topic", Value: "review"}}) {
		t.Errorf("Note %q, KV %v, want the note cleared by an annotation without one", c.Note, c.KV)
	}
}

// switching is A running since 0 with B paused: B ran from -30 to -20.
func switching(t *testing.T) *logb {
	b := dayLog(t)
	b.add(-30, startOf(cycleB, 25))
	b.add(-20, pauseOf(cycleB))
	b.add(0, startOf(cycleA, 50))
	return b
}

func TestSwitchBatchReplaysAsPauseThenResume(t *testing.T) {
	b := switching(t)
	b.switchTo(60, cycleA, cycleB)
	m := mustReplay(t, b.events)
	a, bc := mustCycle(t, m, cycleA), mustCycle(t, m, cycleB)
	assertSegments(t, a, seg(0, 60))
	assertSegments(t, bc, seg(-30, -20), seg(60, open))
	if a.InterruptedBy != cycleB || bc.InterruptedBy != "" {
		t.Errorf("links A->%q B->%q, want A->B and none for B", a.InterruptedBy, bc.InterruptedBy)
	}
	if run, ok := m.Running(); !ok || run.ID != cycleB {
		t.Errorf("Running = %v %v, want B", run.ID, ok)
	}
}

func TestSwitchBackAtTheSameInstantIsEmptyRunningSegment(t *testing.T) {
	b := switching(t)
	b.switchTo(60, cycleA, cycleB)
	base := b.split()
	b.switchTo(60, cycleB, cycleA)
	_, err := Candidate(base, b.added(base))
	if inv := assertCode(t, err, codeEmptyRunningSegment); inv.Entity != string(cycleB) {
		t.Errorf("Entity = %q, want %s, whose segment would have no length", inv.Entity, cycleB)
	}
}

func TestSwitchBackAndForthAtLaterInstants(t *testing.T) {
	b := switching(t)
	b.switchTo(60, cycleA, cycleB)
	b.switchTo(70, cycleB, cycleA)
	b.switchTo(80, cycleA, cycleB)
	m := mustReplay(t, b.events)
	a, bc := mustCycle(t, m, cycleA), mustCycle(t, m, cycleB)
	assertSegments(t, a, seg(0, 60), seg(70, 80))
	assertSegments(t, bc, seg(-30, -20), seg(60, 70), seg(80, open))
	if got := a.Elapsed(at(90)); got != 70*time.Minute {
		t.Errorf("A elapsed = %v, want 70m", got)
	}
	if got := bc.Elapsed(at(90)); got != 30*time.Minute {
		t.Errorf("B elapsed = %v, want 30m", got)
	}
}

func TestSwitchUpdatesInterruptLinks(t *testing.T) {
	t.Run("an interrupted cycle switched back to and then paused by hand has no link", func(t *testing.T) {
		b := dayLog(t)
		b.add(0, startOf(cycleA, 50))
		b.add(10, interruptOf(cycleB, cycleA, 15))
		b.switchTo(20, cycleB, cycleA)
		b.add(30, stopOf(cycleB))
		b.add(40, pauseOf(cycleA))
		if a := mustCycle(t, mustReplay(t, b.events), cycleA); a.InterruptedBy != "" {
			t.Errorf("A.InterruptedBy = %q, want none", a.InterruptedBy)
		}
	})
	t.Run("a cycle switched away from keeps the link after the displacing cycle stops", func(t *testing.T) {
		b := dayLog(t)
		b.add(0, startOf(cycleA, 50))
		b.add(10, pauseOf(cycleA))
		b.add(20, startOf(cycleB, 25))
		b.switchTo(30, cycleB, cycleA)
		b.switchTo(40, cycleA, cycleB)
		b.add(50, stopOf(cycleB))
		if a := mustCycle(t, mustReplay(t, b.events), cycleA); a.InterruptedBy != cycleB {
			t.Errorf("A.InterruptedBy = %q, want %s", a.InterruptedBy, cycleB)
		}
	})
	t.Run("the scripted day", func(t *testing.T) {
		// The epoch is 09:30 in New York: deep-work starts 09:10, notifications
		// interrupts it 09:40, a switch to deep-work 09:50 and back 09:55,
		// notifications stops 09:58 and deep-work resumes 09:58.
		deep, notif := cycleA, cycleB
		b := dayLog(t)
		b.add(-20, startOf(deep, 50))
		b.add(10, interruptOf(notif, deep, 15))
		b.switchTo(20, notif, deep)
		b.switchTo(25, deep, notif)
		link := func() event.CycleID { return mustCycle(t, mustReplay(t, b.events), deep).InterruptedBy }
		if got := link(); got != notif {
			t.Errorf("after the 09:55 switch deep-work's link is %q, want %s", got, notif)
		}
		b.add(28, stopOf(notif))
		if got := link(); got != notif {
			t.Errorf("after notifications stops deep-work's link is %q, want %s", got, notif)
		}
		b.add(28, resumeOf(deep))
		if got := link(); got != "" {
			t.Errorf("after the 09:58 resume deep-work's link is %q, want none", got)
		}
		m := mustReplay(t, b.events)
		if got := mustCycle(t, m, notif).Elapsed(at(60)); got != 13*time.Minute {
			t.Errorf("notifications elapsed = %v, want 13m (09:40-09:50 and 09:55-09:58)", got)
		}
	})
}

func TestDimmedIsPausedAndNotStopped(t *testing.T) {
	b := dayLog(t)
	b.add(-50, startOf(cycleD, 25))
	b.add(-45, pauseOf(cycleD)) // paused by hand
	b.add(-30, startOf(cycleE, 25))
	b.add(-25, stopOf(cycleE)) // stopped: never dimmed
	b.add(0, startOf(cycleA, 50))
	b.add(10, interruptOf(cycleB, cycleA, 15)) // A interrupted
	b.add(20, pauseOf(cycleB))
	b.add(30, startOf(cycleC, 25))
	b.switchTo(40, cycleC, cycleB) // C switched away from
	m := mustReplay(t, b.events)
	var got []event.CycleID
	for _, c := range m.Dimmed() {
		got = append(got, c.ID)
	}
	if want := []event.CycleID{cycleC, cycleA, cycleD}; !slices.Equal(got, want) {
		t.Errorf("Dimmed = %v, want %v (most recently paused first, no stopped cycle)", got, want)
	}
	if run, ok := m.Running(); !ok || run.ID != cycleB {
		t.Errorf("Running = %v %v, want B", run.ID, ok)
	}
	if run, ok := m.RunningAt(at(35)); !ok || run.ID != cycleC {
		t.Errorf("RunningAt(35) = %v %v, want C", run.ID, ok)
	}
	if _, ok := m.RunningAt(at(-35)); ok {
		t.Error("RunningAt(-35) found a running cycle, want none")
	}
	if got := len(m.Cycles()); got != 5 {
		t.Errorf("%d cycles, want 5", got)
	}
}

func TestDomainIgnoresHistory(t *testing.T) {
	direct := dayLog(t)
	direct.add(0, startOf(cycleA, 50))
	direct.add(15, pauseOf(cycleA))
	direct.add(20, noteOf(cycleA, "notes"))

	corrected := dayLog(t)
	corrected.add(0, startOf(cycleA, 50))
	pause := corrected.add(25, pauseOf(cycleA))
	corrected.add(20, noteOf(cycleA, "notes"))
	boost := corrected.add(30, boostOf(cycleA, 10))
	corrected.add(40, event.EventCorrected{Target: pause, Fields: fieldsOf("effective_at", event.At(at(15)))})
	corrected.add(41, event.EventRetracted{Target: boost})

	a, b := mustReplay(t, direct.events).Domain(), mustReplay(t, corrected.events).Domain()
	if !reflect.DeepEqual(a, b) {
		t.Errorf("Domain differs:\n direct    %+v\n corrected %+v", a, b)
	}
	if len(a.Cycles) != 1 || len(a.Periods) != 1 || a.Profile != "work" {
		t.Errorf("Domain = %+v, want one cycle, the day period and the profile", a)
	}
}

func TestOrderByEffectiveAtNotById(t *testing.T) {
	// The ids are assigned against the log order: a later event in the log
	// has a lower id, so sorting by id would put the pause before the start.
	b := dayLog(t)
	ev := func(n, recorded, effective int, p event.Payload) {
		b.events = append(b.events, event.Event{
			Envelope: event.Envelope{V: event.SchemaVersion, ID: eid(n), At: event.At(at(recorded)), EffectiveAt: event.At(at(effective)), Type: p.EventType()},
			Payload:  p,
		})
	}
	ev(90, 0, 0, startOf(cycleA, 50))
	ev(80, 10, 10, pauseOf(cycleA))
	ev(70, 20, 20, noteOf(cycleA, "first at 20"))
	ev(60, 21, 20, noteOf(cycleA, "second at 20"))
	ev(50, 22, 15, noteOf(cycleA, "backdated to 15"))
	c := mustCycle(t, mustReplay(t, b.events), cycleA)
	assertSegments(t, c, seg(0, 10))
	if c.Note != "second at 20" {
		t.Errorf("Note = %q, want the annotation latest by effective_at, then log position", c.Note)
	}
}

func TestCyclesAreInStartOrderAndCopied(t *testing.T) {
	b := dayLog(t)
	b.add(10, startOf(cycleB, 25))
	b.add(20, interruptOf(cycleA, cycleB, 15))
	b.add(30, noteOf(cycleA, "notes", "k", "v"))
	m := mustReplay(t, b.events)
	cycles := m.Cycles()
	if len(cycles) != 2 || cycles[0].ID != cycleB || cycles[1].ID != cycleA {
		t.Fatalf("Cycles = %v, want B then A", cycles)
	}
	cycles[1].KV[0].Value = "tampered"
	*cycles[0].Segments[0].End = at(99)
	if again := mustCycle(t, m, cycleA); again.KV[0].Value != "v" {
		t.Error("Cycles hands out the model's own slices: a caller changed an annotation")
	}
	if again := mustCycle(t, m, cycleB); !again.Segments[0].End.Equal(at(20)) {
		t.Error("Cycles hands out the model's own segment ends")
	}
	if _, ok := m.Cycle("unknown"); ok {
		t.Error("Cycle(unknown) found a cycle")
	}
	c := mustCycle(t, m, cycleA)
	if c.Type != "notifications" || c.Title != "Notifications" || c.PlannedMinutes != 15 || c.Segments[0].OpenedBy != eid(5) {
		t.Errorf("cycle %+v does not carry its start snapshot and opener", c)
	}
}

// cycleGoldenLogs are the synthetic logs under testdata/logs/cycles, built
// with the real encoder, each with what replaying it must give.
func cycleGoldenLogs(t *testing.T) []golden {
	t.Helper()
	var out []golden

	deep, notif := cycleA, cycleB
	b := dayLog(t)
	b.add(-20, startOf(deep, 50))
	b.add(10, interruptOf(notif, deep, 15))
	b.switchTo(20, notif, deep)
	b.switchTo(25, deep, notif)
	b.add(28, stopOf(notif))
	b.add(28, resumeOf(deep))
	b.backfill(215, 150, 210, deep)
	b.add(240, stopOf(deep))
	b.add(241, noteOf(deep, "Reviewed the plan", "pr", "https://example.test/pr/1", "pr", "https://example.test/pr/2"))
	out = append(out, golden{name: "interrupt-switch-break.jsonl", events: b.events, check: func(t *testing.T, m *Model, err error) {
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		// 09:10-09:40, 09:50-09:55, 09:58-12:00 and 13:00-13:30.
		if got := mustCycle(t, m, deep).Elapsed(at(300)); got != 187*time.Minute {
			t.Errorf("deep-work elapsed = %v, want 187m", got)
		}
		if got := mustCycle(t, m, notif).Elapsed(at(300)); got != 13*time.Minute {
			t.Errorf("notifications elapsed = %v, want 13m", got)
		}
	}})

	b, _ = stoppedAt60(t)
	b.backfill(70, 30, 60, cycleA)
	out = append(out, golden{name: "break-ending-at-stop.jsonl", events: b.events, check: func(t *testing.T, _ *Model, err error) {
		assertCode(t, err, codeCycleStopped)
	}})

	b = dayLog(t)
	b.add(0, startOf(cycleA, 50))
	b.addAt(at(20), at(10), startOf(cycleB, 25))
	out = append(out, golden{name: "two-running.jsonl", events: b.events, check: func(t *testing.T, _ *Model, err error) {
		assertCode(t, err, codeAnotherCycleRunning)
	}})
	return out
}

func TestGoldenCycleLogs(t *testing.T) {
	checkGoldenLogs(t, filepath.Join("..", "..", "testdata", "logs", "cycles"), cycleGoldenLogs(t))
}
