package discover

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/event"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/query"
)

// The SourceFailureObserver's success and pause hooks feed the per-source
// last-success gauge (bead pg2-tv11a, DEC-OBS-4): a succeeded pass and a
// deliberately paused pass both notify; a failed pass notifies only
// OnSourceFailure.

func healthySource(name string) query.Source {
	return query.Source{Name: name, Query: fakeQuery{
		Meta:   query.Meta{EmitTypes: []string{"work.ready"}, Trig: query.PeriodTrigger{}},
		events: []event.Event{itemEvt("work.ready", "wk-"+name)},
	}}
}

func failingSource(name string) query.Source {
	calls := 0
	return query.Source{Name: name, Query: flakyQuery{
		Meta:      query.Meta{EmitTypes: []string{"work.ready"}, Trig: query.PeriodTrigger{}},
		failTimes: 100,
		calls:     &calls,
	}}
}

func produceObserved(t *testing.T, sources query.SourceSet, q *eventqueue.Queue, obs *recordingSourceFailureObserver) {
	t.Helper()
	if _, err := produce(context.Background(), query.Env{}, sources, q, core.NewBindings("work.ready"), Cadence{}, realSleep, time.Now, obs, nil); err != nil {
		t.Fatal(err)
	}
}

func TestProduce_successNotifiesOnSourceSucceededAndFailureDoesNot(t *testing.T) {
	obs := &recordingSourceFailureObserver{}
	produceObserved(t, query.SourceSet{healthySource("ok"), failingSource("bad")}, newQueue(t), obs)

	if want := []string{"ok"}; !equalStrings(obs.succeeded, want) {
		t.Fatalf("succeeded = %v, want %v (a failed pass must never count as a success)", obs.succeeded, want)
	}
	if want := []string{"bad"}; !equalStrings(obs.sources, want) {
		t.Fatalf("failures = %v, want %v", obs.sources, want)
	}
	if len(obs.paused) != 0 {
		t.Fatalf("paused = %v, want none", obs.paused)
	}
}

func TestProduce_gateBlockedPassNotifiesOnSourcePausedNotFailure(t *testing.T) {
	q := newQueue(t)
	if _, err := q.SetGate(eventqueue.GateRequest{Type: "ALPHA"}); err != nil {
		t.Fatal(err)
	}
	obs := &recordingSourceFailureObserver{}
	produceObserved(t, query.SourceSet{healthySource("gated")}, q, obs)

	if want := []string{"gated"}; !equalStrings(obs.paused, want) {
		t.Fatalf("paused = %v, want %v", obs.paused, want)
	}
	if len(obs.succeeded) != 0 || len(obs.sources) != 0 {
		t.Fatalf("succeeded = %v failures = %v, want neither for a paused pass", obs.succeeded, obs.sources)
	}
}

func TestProduce_emittersHaltedPassNotifiesOnSourcePausedButTimerDoesNot(t *testing.T) {
	q, mem := limitQueue(t)
	mem.SetLogSize(950)
	q.EnforceLogLimits()
	obs := &recordingSourceFailureObserver{}
	sources := query.SourceSet{polled("poll-a", "work.ready", "a1"), timer()}
	if _, err := produce(context.Background(), query.Env{}, sources, q, core.NewBindings("work.ready", "timer.tick"), Cadence{}, realSleep, time.Now, obs, nil); err != nil {
		t.Fatal(err)
	}

	if want := []string{"poll-a"}; !equalStrings(obs.paused, want) {
		t.Fatalf("paused = %v, want %v (the halted polled source)", obs.paused, want)
	}
	if want := []string{"tick"}; !equalStrings(obs.succeeded, want) {
		t.Fatalf("succeeded = %v, want only the never-halted timer %v", obs.succeeded, want)
	}
}

// A threshold source blocked by a gate is paused too, via the second loop.
func TestProduce_gatedThresholdSourceNotifiesOnSourcePaused(t *testing.T) {
	q := newQueue(t)
	if _, err := q.SetGate(eventqueue.GateRequest{Type: "ALPHA"}); err != nil {
		t.Fatal(err)
	}
	up := healthySource("up")
	up.NonBlockingGates = []string{"ALPHA"}
	down := query.Source{Name: "down", Query: fakeQuery{
		Meta:   query.Meta{EmitTypes: []string{"work.ready"}, Trig: query.ThresholdTrigger{Binds: []string{"work.ready"}, Count: 1}},
		events: []event.Event{itemEvt("work.ready", "d1")},
	}}
	obs := &recordingSourceFailureObserver{}
	produceObserved(t, query.SourceSet{up, down}, q, obs)

	if want := []string{"down"}; !equalStrings(obs.paused, want) {
		t.Fatalf("paused = %v, want %v", obs.paused, want)
	}
	if want := []string{"up"}; !equalStrings(obs.succeeded, want) {
		t.Fatalf("succeeded = %v, want %v", obs.succeeded, want)
	}
}
