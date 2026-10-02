package discover

import (
	"context"
	"errors"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/event"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/query"
)

// Tests for the log-size limit's producer side (bead pg2-5d3ui): the soft step
// (the queue's in-memory emittersHalted flag, consulted next to the gate check) and
// the non-fatal handling of a hard-limit / unwritable admission refusal.

// limitQueue is a queue over a MemStore whose reported size the test pins, with
// soft=900 and hard=1000.
func limitQueue(t *testing.T) (*eventqueue.Queue, *eventqueue.MemStore) {
	t.Helper()
	mem := eventqueue.NewMemStore()
	q, err := eventqueue.New(mem, eventqueue.WithLogLimits(900, 1000))
	if err != nil {
		t.Fatal(err)
	}
	return q, mem
}

func polled(name, typ, id string, nonBlocking ...string) query.Source {
	return query.Source{Name: name, Query: fakeQuery{
		Meta:   query.Meta{EmitTypes: []string{typ}, Trig: query.PeriodTrigger{}},
		events: []event.Event{itemEvt(typ, id)},
	}, NonBlockingGates: nonBlocking}
}

func timer() query.Source {
	return query.Source{Name: "tick", Query: query.TimerQuery{Meta: query.Meta{EmitTypes: []string{"timer.tick"}}}}
}

// While emittersHalted: polled sources are not polled and are named in
// ProduceReport.Halted; the timer emitter is NOT halted and its event is still
// admitted; a polled source's NonBlockingGates exemption (a gate concept) does not
// exempt it. No gate is involved at all.
func TestProduce_emittersHaltedSkipsPolledSourcesButNotTimer(t *testing.T) {
	q, mem := limitQueue(t)
	mem.SetLogSize(950) // over soft (900), under hard (1000)
	q.EnforceLogLimits()
	if !q.EmittersHalted() {
		t.Fatal("setup: emitters not halted")
	}
	sources := query.SourceSet{
		polled("poll-a", "a.ready", "a1"),
		polled("poll-exempt", "b.ready", "b1", "SYSTEM_PAUSE", "ALPHA"), // a gate exemption is irrelevant here
		timer(),
	}
	rpt, err := Produce(context.Background(), query.Env{}, sources, q, core.NewBindings("a.ready", "b.ready", "timer.tick"))
	if err != nil {
		t.Fatal(err)
	}
	depth := q.DepthByType()
	if depth["a.ready"] != 0 || depth["b.ready"] != 0 {
		t.Fatalf("a halted polled source was polled: depth = %v", depth)
	}
	if depth["timer.tick"] != 1 {
		t.Fatalf("the timer emitter must keep running while only soft-halted: depth = %v", depth)
	}
	if len(rpt.Halted) != 2 || rpt.Halted["poll-a"] != eventqueue.StateEmittersHalted || rpt.Halted["poll-exempt"] != eventqueue.StateEmittersHalted {
		t.Fatalf("Halted = %v, want poll-a and poll-exempt by %s", rpt.Halted, eventqueue.StateEmittersHalted)
	}
	if len(rpt.Blocked) != 0 {
		t.Fatalf("Blocked = %v; the soft step is not a gate", rpt.Blocked)
	}
	if _, polledAt := rpt.LastTick["poll-a"]; polledAt {
		t.Fatal("a halted source's LastTick must stay untouched so it fires on the first pass after the halt clears")
	}
	if len(q.ActiveGates()) != 0 {
		t.Fatalf("gates appeared: %v", q.ActiveGates())
	}

	// Once the log shrinks the next pass polls again.
	mem.SetLogSize(100)
	q.EnforceLogLimits()
	if _, err := Produce(context.Background(), query.Env{}, sources, q, core.NewBindings("a.ready", "b.ready", "timer.tick")); err != nil {
		t.Fatal(err)
	}
	if d := q.DepthByType(); d["a.ready"] != 1 || d["b.ready"] != 1 {
		t.Fatalf("after the halt cleared depth = %v, want both polled sources produced", d)
	}
}

// A threshold-triggered source is an emitter too and is halted the same way.
func TestProduce_emittersHaltedSkipsThresholdSource(t *testing.T) {
	q, mem := limitQueue(t)
	// An upstream event already queued so the threshold would otherwise be met.
	if _, err := q.Enqueue(eventqueue.Event{ID: "u1", Type: "up", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	mem.SetLogSize(950)
	q.EnforceLogLimits()
	sources := query.SourceSet{{Name: "down-source", Query: fakeQuery{
		Meta:   query.Meta{EmitTypes: []string{"down"}, Trig: query.ThresholdTrigger{Binds: []string{"up"}, Count: 1}},
		events: []event.Event{itemEvt("down", "d1")},
	}}}
	rpt, err := Produce(context.Background(), query.Env{}, sources, q, core.NewBindings("up", "down"))
	if err != nil {
		t.Fatal(err)
	}
	if q.DepthByType()["down"] != 0 || rpt.Halted["down-source"] == "" {
		t.Fatalf("depth=%v Halted=%v, want the threshold source halted", q.DepthByType(), rpt.Halted)
	}
}

// THE WEDGE: at the hard limit every Enqueue is refused. A never-gated timer
// source (disk-check-tick fires every few minutes) must not turn that into a
// pass-aborting error — it is recorded and the pass carries on, so the tick that
// called Produce still expires, dispatches and compacts (the only way the
// condition ever clears).
func TestProduce_hardLimitRefusalIsNonFatalAndIsolatedPerSource(t *testing.T) {
	q, mem := limitQueue(t)
	mem.SetLogSize(1000) // at the hard limit; no EnforceLogLimits call, so nothing is soft-halted yet
	sources := query.SourceSet{
		timer(),
		polled("poll-a", "a.ready", "a1"),
		{Name: "poll-two", Query: fakeQuery{
			Meta:   query.Meta{EmitTypes: []string{"a.ready"}, Trig: query.PeriodTrigger{}},
			events: []event.Event{itemEvt("a.ready", "a2"), itemEvt("a.ready", "a3")},
		}},
	}
	rpt, err := Produce(context.Background(), query.Env{}, sources, q, core.NewBindings("a.ready", "timer.tick"))
	if err != nil {
		t.Fatalf("a log_full refusal must be non-fatal to the produce pass, got %v", err)
	}
	if rpt.LogRejected["tick"] != 1 || rpt.LogRejected["poll-a"] != 1 || rpt.LogRejected["poll-two"] != 2 {
		t.Fatalf("LogRejected = %v, want tick:1 poll-a:1 poll-two:2 (every refused event counted)", rpt.LogRejected)
	}
	for _, name := range []string{"tick", "poll-a", "poll-two"} {
		serr := rpt.SourceErrors[name]
		if serr == nil {
			t.Fatalf("no SourceError for %s", name)
		}
		var full *eventqueue.ErrLogFull
		if !errors.As(serr, &full) {
			t.Fatalf("SourceErrors[%s] = %v, want it to wrap *eventqueue.ErrLogFull", name, serr)
		}
	}
	if len(rpt.Emitted) != 0 {
		t.Fatalf("Emitted = %v, want nothing admitted", rpt.Emitted)
	}
	if st := q.LimitStatus(); st.RejectedLogFull != 4 {
		t.Fatalf("queue-level reject count = %d, want 4 (push and pull are counted together at Enqueue)", st.RejectedLogFull)
	}
	// Once there is room again the very next pass produces normally.
	mem.SetLogSize(10)
	rpt, err = Produce(context.Background(), query.Env{}, sources, q, core.NewBindings("a.ready", "timer.tick"))
	if err != nil || len(rpt.SourceErrors) != 0 {
		t.Fatalf("after space returned: err=%v SourceErrors=%v", err, rpt.SourceErrors)
	}
}

// A non-admission Enqueue failure keeps its old meaning: it still aborts the pass
// (a store failure that is not a classified refusal is not a source failure).
func TestProduce_unclassifiedEnqueueFailureStillAbortsThePass(t *testing.T) {
	q, err := eventqueue.New(failEncodeStore{eventqueue.NewMemStore()})
	if err != nil {
		t.Fatal(err)
	}
	sources := query.SourceSet{polled("poll-a", "a.ready", "a1")}
	if _, err := Produce(context.Background(), query.Env{}, sources, q, core.NewBindings("a.ready")); err == nil {
		t.Fatal("an unclassified Enqueue failure must still propagate")
	}
}

// failEncodeStore fails every append with an EncodeError — a fault in the record,
// which the queue does not classify as an unwritable log.
type failEncodeStore struct{ eventqueue.Store }

func (failEncodeStore) Append(eventqueue.Record) error {
	return &eventqueue.EncodeError{Err: errors.New("cannot encode")}
}

func (failEncodeStore) AppendBatch([]eventqueue.Record) error {
	return &eventqueue.EncodeError{Err: errors.New("cannot encode")}
}
