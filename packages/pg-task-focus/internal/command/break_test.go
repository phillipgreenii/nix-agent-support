package command_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
)

// runningFrom0 is the bootstrapped log with cycle A started at t0.
func runningFrom0(t *testing.T) *logb {
	t.Helper()
	b := bootstrapped(t)
	b.add(0, startOf(cycleA, deepWork))
	return b
}

// stoppedAt90 is runningFrom0 with cycle A stopped 90 minutes after t0, and
// the id of that stop.
func stoppedAt90(t *testing.T) (*logb, event.ID) {
	t.Helper()
	b := runningFrom0(t)
	return b, b.add(90, stopOf(cycleA))
}

func TestBackfillBreakEvents(t *testing.T) {
	b, _ := stoppedAt90(t)
	env := envOf(t, b, at(120))
	c := command.BackfillBreak{ID: clientID, CycleID: cycleA, From: at(30), To: at(60)}
	p := mustPlan(t, env, c)

	want := []event.Type{event.TypeCyclePaused, event.TypeCycleResumed, event.TypeBatchCommitted}
	if got := typesOf(p); !slices.Equal(got, want) {
		t.Fatalf("types = %v, want %v", got, want)
	}
	if p.BatchID != clientID {
		t.Errorf("batch = %s, want the request id %s", p.BatchID, clientID)
	}
	hash := hashOf(t, c)
	effective := []time.Time{at(30), at(60), at(120)}
	for i, e := range p.Events {
		if e.Payload.BatchID() != clientID {
			t.Errorf("%s has batch %q, want %q", e.Type, e.Payload.BatchID(), clientID)
		}
		if !e.EffectiveAt.Time().Equal(effective[i]) || !e.At.Time().Equal(at(120)) {
			t.Errorf("%s at %v effective %v, want at %v effective %v", e.Type, e.At.Time(), e.EffectiveAt.Time(), at(120), effective[i])
		}
		if e.ReqHash != hash {
			t.Errorf("%s req_hash = %q, want %q", e.Type, e.ReqHash, hash)
		}
		if e.ID != idOf('N', uint32(i+1)) {
			t.Errorf("%s id = %s, want the fresh id %s", e.Type, e.ID, idOf('N', uint32(i+1)))
		}
	}
	if p.Events[0].Payload.(event.CyclePaused).CycleID != cycleA || p.Events[1].Payload.(event.CycleResumed).CycleID != cycleA {
		t.Errorf("the break does not pause and resume cycle %s: %+v", cycleA, p.Events)
	}
	c2, ok := p.Candidate.Cycle(cycleA)
	if !ok {
		t.Fatal("the candidate has no cycle A")
	}
	if got := c2.Elapsed(at(120)); got != 60*time.Minute {
		t.Errorf("elapsed = %v, want 60m: 90 minutes less the 30-minute break", got)
	}

	t.Run("without a client id the batch id is fresh and nothing is hashed", func(t *testing.T) {
		p := mustPlan(t, envOf(t, b, at(120)), command.BackfillBreak{CycleID: cycleA, From: at(30), To: at(60)})
		if p.BatchID == "" || p.BatchID == clientID {
			t.Fatalf("batch = %q, want a fresh id", p.BatchID)
		}
		for _, e := range p.Events {
			if e.ReqHash != "" || e.Payload.BatchID() != p.BatchID {
				t.Errorf("%s req_hash %q batch %q", e.Type, e.ReqHash, e.Payload.BatchID())
			}
		}
	})

	t.Run("the golden log of a break built by Build replays", func(t *testing.T) {
		env, _ := begun(t, loadConfig(t, nil))
		env, _ = apply(t, env, command.StartCycle{Type: deepWork}, at(0))
		started := env.Model.Cycles()[0].ID
		env, _ = apply(t, env, command.StopCycle{CycleID: started}, at(90))
		_, p := apply(t, env, command.BackfillBreak{ID: clientID, CycleID: started, From: at(30), To: at(60)}, at(120))
		m := writeGoldenIn(t, correctionsDir, "break-backfill.jsonl", p.Candidate.Log())
		c, ok := m.Cycle(started)
		if !ok || c.Elapsed(at(120)) != 60*time.Minute {
			t.Errorf("replayed golden: cycle %+v", c)
		}
	})
}

// apply builds c against env read at now, which MUST succeed, and returns the
// environment after its events are adopted, with the plan.
func apply(t *testing.T, env command.Env, c command.Command, now time.Time) (command.Env, command.Plan) {
	t.Helper()
	env.Now = now
	p := mustPlan(t, env, c)
	if p.NoOp {
		t.Fatalf("Build(%T) is a no-op: %s", c, p.Note)
	}
	return then(t, env, p, now), p
}

func TestBreakToMustPrecedeStop(t *testing.T) {
	tests := []struct {
		name     string
		from, to int
	}{
		{"the break begins before the stop and ends at it", 60, 90},
		{"the break begins before the stop and ends after it", 60, 100},
		{"the break begins at the stop", 90, 100},
		{"the break begins after the stop", 95, 100},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, stop := stoppedAt90(t)
			r := mustReject(t, envOf(t, b, at(120)), command.BackfillBreak{CycleID: cycleA, From: at(tc.from), To: at(tc.to)}, command.ReasonBreakEndsAtStop)
			if r.Reason.Status() != 409 {
				t.Errorf("status %d, want 409", r.Reason.Status())
			}
			if !strings.Contains(r.Message, "end at") {
				t.Errorf("message %q does not point at end at", r.Message)
			}
			if r.Entity != string(cycleA) || !slices.Contains(r.Events, stop) {
				t.Errorf("Entity %q, Events %v: want cycle %s and the stored stop %s", r.Entity, r.Events, cycleA, stop)
			}
		})
	}
}

func TestBreakInsideExistingPause(t *testing.T) {
	paused := func(t *testing.T) *logb {
		b := runningFrom0(t)
		b.add(30, pauseOf(cycleA))
		b.add(60, resumeOf(cycleA))
		return b
	}
	for _, span := range [][2]int{{40, 50}, {20, 40}, {40, 70}, {20, 70}} {
		r := mustReject(t, envOf(t, paused(t), at(120)), command.BackfillBreak{CycleID: cycleA, From: at(span[0]), To: at(span[1])}, command.ReasonCycleSegmentsOverlap)
		if r.Entity != string(cycleA) {
			t.Errorf("break %v: Entity %q, want %s", span, r.Entity, cycleA)
		}
	}
}

func TestBreakEndingAtStopInstant(t *testing.T) {
	b, stop := stoppedAt90(t)
	r := mustReject(t, envOf(t, b, at(120)), command.BackfillBreak{CycleID: cycleA, From: at(60), To: at(90)}, command.ReasonBreakEndsAtStop)
	if !strings.Contains(r.Message, "end at") || !strings.Contains(r.Message, "the new event") {
		t.Errorf("message %q: want it to call the closing resume the new event and point at end at", r.Message)
	}
	if !sameIDs(r.Events, stop) {
		t.Errorf("Events = %v, want only the stored stop %s", r.Events, stop)
	}
	if !slices.ContainsFunc(r.Instants, at(90).Equal) {
		t.Errorf("Instants = %v, want the closing resume's, which is the stop's", r.Instants)
	}

	// The same events, stored with no event added, are a stopped cycle that
	// changes: cycle_stopped, naming the batch.
	batch := b.id()
	b.addAt(at(120), at(60), event.CyclePaused{CycleID: cycleA, Batch: batch})
	b.addAt(at(120), at(90), event.CycleResumed{CycleID: cycleA, Batch: batch})
	b.addAt(at(120), at(120), event.BatchCommitted{Batch: batch})
	_, err := projection.Replay(b.events)
	var inv *projection.Invalid
	if !errors.As(err, &inv) || inv.Code != projection.Code(command.ReasonCycleStopped) {
		t.Fatalf("Replay = %v, want cycle_stopped", err)
	}
	if !strings.Contains(inv.Message, string(batch)) {
		t.Errorf("message %q does not name the batch %s", inv.Message, batch)
	}
}

func TestEndAtBeforeTheCycleStartIsStopNotAfterStart(t *testing.T) {
	// An end at the very instant of the start is stop_not_after_start; one
	// before the start sorts before it and is cycle_event_before_start.
	b := runningFrom0(t)
	r := mustReject(t, envOf(t, b, at(30)), command.StopCycle{CycleID: cycleA, EffectiveAt: ptr(at(0))}, command.ReasonStopNotAfterStart)
	if r.Reason.Status() != 422 || r.Entity != string(cycleA) {
		t.Errorf("status %d, entity %q", r.Reason.Status(), r.Entity)
	}
	mustReject(t, envOf(t, b, at(30)), command.StopCycle{CycleID: cycleA, EffectiveAt: ptr(at(-5))}, command.ReasonCycleEventBeforeStart)
}

func TestEndAtBeforeALaterResumeIsCycleStopped(t *testing.T) {
	b := runningFrom0(t)
	b.add(10, pauseOf(cycleA))
	resume := b.add(20, resumeOf(cycleA))
	for _, end := range []int{15, 5} {
		r := mustReject(t, envOf(t, b, at(40)), command.StopCycle{CycleID: cycleA, EffectiveAt: ptr(at(end))}, command.ReasonCycleStopped)
		if r.Entity != string(cycleA) {
			t.Errorf("end at %d: Entity %q", end, r.Entity)
		}
		if end == 15 && !slices.Contains(r.Events, resume) {
			t.Errorf("end at 15: Events %v do not name the later resume %s", r.Events, resume)
		}
	}
	p := mustPlan(t, envOf(t, b, at(40)), command.StopCycle{CycleID: cycleA, EffectiveAt: ptr(at(30))})
	if got := status(t, p.Candidate, cycleA, at(30)); got != projection.Stopped {
		t.Errorf("an end at after the resume leaves the cycle %s", got)
	}
}

func TestEndAtOnAStoppedCycle(t *testing.T) {
	b, stop := stoppedAt90(t)
	note := mustNoOp(t, envOf(t, b, at(120)), command.StopCycle{CycleID: cycleA, EffectiveAt: ptr(at(100))})
	if !strings.Contains(note, "stopped") {
		t.Errorf("note %q does not say the cycle is stopped", note)
	}
	r := mustReject(t, envOf(t, b, at(120)), command.StopCycle{CycleID: cycleA, EffectiveAt: ptr(at(60))}, command.ReasonCycleStopped)
	if !strings.Contains(r.Message, "correct the time of its stop") || !strings.Contains(r.Message, string(stop)) {
		t.Errorf("message %q does not say to correct the stop %s", r.Message, stop)
	}
}

func TestBreakFromNotBeforeToIsInvalidRequest(t *testing.T) {
	b := runningFrom0(t)
	tests := []struct {
		name string
		cmd  command.BackfillBreak
	}{
		{"from equals to", command.BackfillBreak{CycleID: cycleA, From: at(30), To: at(30)}},
		{"from after to", command.BackfillBreak{CycleID: cycleA, From: at(40), To: at(30)}},
		{"from equals to at the millisecond", command.BackfillBreak{CycleID: cycleA, From: at(30), To: at(30).Add(time.Microsecond)}},
		{"no from", command.BackfillBreak{CycleID: cycleA, To: at(30)}},
		{"no to", command.BackfillBreak{CycleID: cycleA, From: at(30)}},
		{"no cycle", command.BackfillBreak{From: at(20), To: at(30)}},
		{"a cycle id that is not valid UTF-8", command.BackfillBreak{CycleID: "a\xff", From: at(20), To: at(30)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := mustReject(t, envOf(t, b, at(60)), tc.cmd, command.ReasonInvalidRequest)
			if r.Reason.Status() != 400 {
				t.Errorf("status %d, want 400", r.Reason.Status())
			}
		})
	}
}

func TestBreakInTheFutureIsFutureEffectiveAt(t *testing.T) {
	b := runningFrom0(t)
	now := at(30)
	for name, span := range map[string][2]time.Time{
		"the break ends in the future":         {at(20), now.Add(2 * time.Minute)},
		"the whole break is in the future":     {now.Add(2 * time.Minute), now.Add(3 * time.Minute)},
		"the break ends a millisecond too far": {at(20), now.Add(time.Minute + time.Millisecond)},
	} {
		t.Run(name, func(t *testing.T) {
			r := mustReject(t, envOf(t, b, now), command.BackfillBreak{CycleID: cycleA, From: span[0], To: span[1]}, command.ReasonFutureEffectiveAt)
			if !slices.ContainsFunc(r.Instants, span[1].Equal) {
				t.Errorf("Instants = %v, want the break's end %v", r.Instants, span[1])
			}
		})
	}
	t.Run("a break ending exactly at the skew is accepted", func(t *testing.T) {
		mustPlan(t, envOf(t, b, now), command.BackfillBreak{CycleID: cycleA, From: at(20), To: now.Add(time.Minute)})
	})
}
