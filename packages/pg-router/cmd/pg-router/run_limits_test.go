package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/event"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/item"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// Tests for the drive loop's side of the log-size limit (bead pg2-5d3ui): a tick
// must keep expiring, dispatching, running the limit controller and publishing
// even when producing is refused or errors — otherwise the very work that frees
// the log would stop at the hard limit.

// countingQuery is a polled source that counts how often it is polled.
type countingQuery struct {
	query.Meta
	polls *atomic.Int64
}

func (countingQuery) Validate() error        { return nil }
func (countingQuery) BackingCommand() string { return "" }
func (c countingQuery) Run(context.Context, query.Env) ([]event.Event, error) {
	n := c.polls.Add(1)
	return []event.Event{{ID: fmt.Sprintf("poll-%d", n), Type: "t1", Item: item.Item{ID: fmt.Sprintf("p%d", n), Type: "task"}}}, nil
}

// takeListener accepts every offered event and reports each id on a channel.
type takeListener struct {
	id   string
	typ  string
	took chan string
}

func (l *takeListener) ID() string                      { return l.id }
func (l *takeListener) Matches(e eventqueue.Event) bool { return e.Type == l.typ }
func (l *takeListener) Offer(o eventqueue.Offering) eventqueue.OfferResult {
	l.took <- o.Event.ID
	return eventqueue.OfferResult{Accepted: true}
}

func limitTickOrchestrator(polls *atomic.Int64) *orchestrator.Orchestrator {
	cfg := config.Config{Queries: query.SourceSet{
		{Name: "poller", Query: countingQuery{Meta: query.Meta{EmitTypes: []string{"t1"}, Trig: query.PeriodTrigger{}}, polls: polls}},
		{Name: "tick", Query: query.TimerQuery{Meta: query.Meta{EmitTypes: []string{"t1"}}}},
	}}
	return &orchestrator.Orchestrator{Cfg: cfg, Bindings: core.NewBindings("t1")}
}

func waitInFlight(t *testing.T, q *eventqueue.Queue) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if !q.WaitForInFlightDrain(ctx, 5*time.Millisecond) {
		t.Fatal("a Kick-launched offer never settled")
	}
}

// THE WEDGE (acceptance): at the hard limit the never-gated timer source is
// refused on every tick. The tick must STILL run the limit controller, Kick,
// Expire and PublishTick, with the halted poller left unpolled.
func TestRunOneTick_hardLimitDoesNotWedgeTheTick(t *testing.T) {
	mem := eventqueue.NewMemStore()
	q, err := eventqueue.New(mem, eventqueue.WithLogLimits(900, 1000))
	if err != nil {
		t.Fatal(err)
	}
	took := make(chan string, 16)
	q.Register(&takeListener{id: "r1", typ: "t1", took: took})
	// Queued before the log filled: one awaiting dispatch, one already expired and
	// unbound (so Expire retires it).
	if _, err := q.Enqueue(eventqueue.Event{ID: "queued", Type: "t1", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(eventqueue.Event{ID: "orphan", Type: "orphan", ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	mem.SetLogSize(1000) // now at the hard limit

	var polls atomic.Int64
	o := limitTickOrchestrator(&polls)
	svc := &core.Service{}
	var stderr strings.Builder
	runOneTick(context.Background(), config.Config{Queries: o.Cfg.Queries}, o, svc, q, "", &stderr)

	if got := polls.Load(); got != 0 {
		t.Fatalf("the polled source was polled %d time(s) although the log is full: the controller must run BEFORE producing", got)
	}
	if st := q.LimitStatus(); st.State != eventqueue.StateLogFull || !st.EmittersHalted {
		t.Fatalf("limit status = %+v, want log_full with emitters halted (the controller did not run)", st)
	}
	if st := q.LimitStatus(); st.RejectedLogFull != 1 {
		t.Fatalf("RejectedLogFull = %d, want 1: the timer tick is refused, counted, and the tick carries on", st.RejectedLogFull)
	}
	select {
	case id := <-took:
		if id != "queued" {
			t.Fatalf("listener took %q, want the already-queued event", id)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Kick did not dispatch the queued event: the tick wedged")
	}
	waitInFlight(t, q)
	if depth := q.DepthByType()["orphan"]; depth != 0 {
		t.Fatalf("Expire did not run: orphan depth = %d", depth)
	}
	if svc.CurrentTick() == nil {
		t.Fatal("PublishTick did not run")
	}
}

// A ProduceTick ERROR must not skip Kick, Expire or PublishTick either.
func TestRunOneTick_produceErrorStillKicksExpiresAndPublishes(t *testing.T) {
	q, err := eventqueue.New(encodeFailStore{eventqueue.NewMemStore()})
	if err != nil {
		t.Fatal(err)
	}
	var polls atomic.Int64
	o := limitTickOrchestrator(&polls)
	svc := &core.Service{}
	var stderr strings.Builder
	runOneTick(context.Background(), config.Config{Queries: o.Cfg.Queries}, o, svc, q, "", &stderr)
	if polls.Load() == 0 {
		t.Fatal("setup: the source was never polled, so the produce error path was not exercised")
	}
	if svc.CurrentTick() == nil {
		t.Fatal("a ProduceTick error must not skip PublishTick")
	}
}

// encodeFailStore fails every append with an EncodeError (a fault in the record,
// which Enqueue returns as a plain, non-admission error and ProduceTick propagates).
type encodeFailStore struct{ eventqueue.Store }

func (encodeFailStore) Append(eventqueue.Record) error {
	return &eventqueue.EncodeError{Err: errors.New("cannot encode")}
}

func (encodeFailStore) AppendBatch([]eventqueue.Record) error {
	return &eventqueue.EncodeError{Err: errors.New("cannot encode")}
}

// Soft step, end to end through the tick: past soft the poller is not polled, but
// the timer still ticks into the queue and the (gate-unexempted) listener still
// gets dispatched — the soft halt is not a gate.
func TestRunOneTick_softHaltSkipsPollerButTimerAndListenerKeepRunning(t *testing.T) {
	mem := eventqueue.NewMemStore()
	q, err := eventqueue.New(mem, eventqueue.WithLogLimits(900, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	took := make(chan string, 16)
	q.Register(&takeListener{id: "r1", typ: "t1", took: took})
	mem.SetLogSize(950)

	var polls atomic.Int64
	o := limitTickOrchestrator(&polls)
	svc := &core.Service{}
	var stderr strings.Builder
	runOneTick(context.Background(), config.Config{Queries: o.Cfg.Queries}, o, svc, q, "", &stderr)

	if polls.Load() != 0 {
		t.Fatalf("polled %d time(s) while soft-halted", polls.Load())
	}
	select {
	case id := <-took:
		if !strings.HasPrefix(id, "timer:t1:") {
			t.Fatalf("listener took %q, want the timer tick", id)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the timer tick was not dispatched while only soft-halted")
	}
	waitInFlight(t, q)
	if gates := q.ActiveGates(); len(gates) != 0 {
		t.Fatalf("the soft halt appeared as gates %v", gates)
	}

	// The operator's resume --all (gate clear) changes nothing about the halt, and the
	// next tick still skips the poller.
	if _, _, err := q.ClearGate("SYSTEM_PAUSE", "op"); err != nil {
		t.Fatal(err)
	}
	runOneTick(context.Background(), config.Config{Queries: o.Cfg.Queries}, o, svc, q, "", &stderr)
	if polls.Load() != 0 || !q.EmittersHalted() {
		t.Fatalf("after a gate clear: polls=%d halted=%v, want 0 and true", polls.Load(), q.EmittersHalted())
	}
	// Space returns: the controller clears the halt and the poller runs again.
	mem.SetLogSize(10)
	runOneTick(context.Background(), config.Config{Queries: o.Cfg.Queries}, o, svc, q, "", &stderr)
	if polls.Load() != 1 || q.EmittersHalted() {
		t.Fatalf("after space returned: polls=%d halted=%v, want 1 and false", polls.Load(), q.EmittersHalted())
	}
	// drain whatever the last tick launched
	for len(took) > 0 {
		<-took
	}
	waitInFlight(t, q)
}

// run-until-idle at the hard limit: the timer source is refused (non-fatal), the
// already-queued events are STILL drained — each dispatched and its accept record
// appended above the limit — and the run exits generic-failure only AFTER draining.
func TestRunUntilIdleBody_hardLimitStillDrainsQueuedWork(t *testing.T) {
	logDir := shortDir(t)
	path := filepath.Join(logDir, "queue.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	at := time.Now().UTC()
	pad := strings.Repeat("x", 1000)
	const seeded = 6
	for i := 0; i < seeded; i++ {
		if err := enc.Encode(map[string]any{
			"op": "enqueue", "eventId": fmt.Sprintf("seed-%d", i), "type": "t1",
			"at": at, "expiresAt": at.Add(time.Hour), "enqueuedAt": at, "payload": map[string]any{"pad": pad},
		}); err != nil {
			t.Fatal(err)
		}
	}
	_ = f.Close()
	before, _ := os.Stat(path)
	const hard = 4096
	if before.Size() <= hard {
		t.Fatalf("setup: seeded log is %d bytes, want it over the %d hard limit", before.Size(), hard)
	}

	fh := &fakeHandlerClient{}
	cfg := config.Config{
		LogDir:      logDir,
		MaxLogBytes: hard,
		Roles:       roles.RoleSet{{Name: "r1", Enabled: true, Binds: []string{"t1"}}},
		Queries:     query.SourceSet{{Name: "tick", Query: query.TimerQuery{Meta: query.Meta{EmitTypes: []string{"t1"}}}}},
	}
	o := &orchestrator.Orchestrator{Cfg: cfg, Handler: fh, Bindings: core.NewBindings("t1")}

	done := make(chan int, 1)
	go func() {
		done <- runUntilIdleBody(context.Background(), preparedRun{cfg: cfg, o: o, cleanup: func() {}, declaredRoles: cfg.Roles})
	}()
	select {
	case code := <-done:
		if code != exitGeneric {
			t.Fatalf("exit = %d, want %d: the refused timer tick is a partial produce, reported AFTER draining", code, exitGeneric)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("run-until-idle wedged at the hard log limit")
	}
	if got := len(fh.dispatchedCalls()); got != seeded {
		t.Fatalf("dispatched %d of %d queued events: the hard limit must not stop the drain", got, seeded)
	}
	after, _ := os.Stat(path)
	if after.Size() <= hard {
		t.Fatalf("log is %d bytes; the accept records should have been appended above the %d limit", after.Size(), hard)
	}
	raw, _ := os.ReadFile(path)
	if got := strings.Count(string(raw), `"op":"accept"`); got != seeded {
		t.Fatalf("%d accept records on disk, want %d: accept appends must still succeed above the hard limit", got, seeded)
	}
	if strings.Contains(string(raw), "timer:t1:") {
		t.Fatal("the refused timer tick was written to the log")
	}
}
