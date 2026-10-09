package engine_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
)

// outcome is what one goroutine got from Do.
type outcome struct {
	r   engine.Result
	err error
}

// concurrently runs n requests at once and returns their outcomes in order.
func (h *harness) concurrently(n int, mk func(i int) command.Command) []outcome {
	out := make([]outcome, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			out[i].r, out[i].err = h.try(mk(i))
		})
	}
	close(start)
	wg.Wait()
	return out
}

// linesOf is the number of log lines a result appended.
func linesOf(r engine.Result) int {
	if !r.Changed || r.Replayed {
		return 0
	}
	n := len(r.EventIDs)
	if r.BatchID != "" {
		n++ // the batch.committed
	}
	return n
}

func TestConcurrentDoIsSerialized(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	a := h.start(local(9, 0), deepWork)
	h.clock.Set(local(9, 10))
	before := h.lines()
	planDay := taskOf(7, "plan-day")

	outs := h.concurrently(50, func(i int) command.Command {
		switch {
		case i%10 == 0:
			return command.CompleteTask{TaskID: planDay}
		case i%2 == 0:
			return command.AnnotateCycle{CycleID: a, Note: fmt.Sprintf("note %d", i)}
		}
		return command.BoostCycle{CycleID: a, Minutes: 5}
	})
	accepted, completions := 0, 0
	for i, o := range outs {
		if o.err != nil {
			rejectionOf(t, o.err, command.ReasonTaskAlreadyResolved)
			continue
		}
		if i%10 == 0 {
			completions++
		}
		accepted += linesOf(o.r)
	}
	if completions != 1 {
		t.Errorf("%d completions of one task succeeded, want 1", completions)
	}
	if got := h.lines(); got != before+accepted || h.e.Version().LogLines != got {
		t.Errorf("file %d lines, Version %+v, want %d + %d accepted", got, h.e.Version(), before, accepted)
	}
	m, err := projection.Replay(h.fileEvents())
	if err != nil {
		t.Fatalf("the log does not replay: %v", err)
	}
	if !reflect.DeepEqual(m.Domain(), h.e.Snapshot().Model.Domain()) {
		t.Error("the replayed log and the engine's model differ")
	}
	if got := len(h.cycle(a).Boosts); got != 25 {
		t.Errorf("%d boosts, want 25", got)
	}
}

func TestConcurrentSameIDProducesOneSetOfEvents(t *testing.T) {
	t.Run("a lone event", func(t *testing.T) {
		h := newHarness(t, nil)
		h.bootstrap()
		h.clock.Set(local(9, 0))
		c := command.StartCycle{ID: clientID(), Type: deepWork}
		outs := h.concurrently(8, func(int) command.Command { return c })
		fresh := 0
		for _, o := range outs {
			if o.err != nil {
				t.Fatalf("Do: %v", o.err)
			}
			if !slices.Equal(o.r.EventIDs, []event.ID{c.ID}) {
				t.Errorf("EventIDs %v, want [%s]", o.r.EventIDs, c.ID)
			}
			if !o.r.Replayed {
				fresh++
			}
		}
		if fresh != 1 || h.countID(c.ID) != 1 || len(h.e.Snapshot().Model.Cycles()) != 1 {
			t.Errorf("%d fresh results, %d events %s, %d cycles; want one of each", fresh, h.countID(c.ID), c.ID, len(h.e.Snapshot().Model.Cycles()))
		}
	})
	t.Run("a batch", func(t *testing.T) {
		h := newHarness(t, nil)
		c := bootstrapCmd(clientID())
		outs := h.concurrently(8, func(int) command.Command { return c })
		for _, o := range outs {
			if o.err != nil {
				t.Fatalf("Do: %v", o.err)
			}
			if o.r.BatchID != c.ID || !slices.Equal(o.r.EventIDs, outs[0].r.EventIDs) {
				t.Errorf("Result %+v, want batch %s with the same events as every other", o.r, c.ID)
			}
		}
		if h.lines() != len(outs[0].r.EventIDs)+1 {
			t.Errorf("the file has %d lines, want one batch of %d events and its marker", h.lines(), len(outs[0].r.EventIDs))
		}
	})
}

func TestConcurrentCompleteSameTaskDifferentIDs(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	h.clock.Set(local(9, 5))
	before := h.lines()
	planDay := taskOf(7, "plan-day")
	ids := []event.ID{clientID(), clientID()}
	outs := h.concurrently(2, func(i int) command.Command { return command.CompleteTask{ID: ids[i], TaskID: planDay} })
	ok, refused := 0, 0
	for _, o := range outs {
		if o.err == nil {
			ok++
			continue
		}
		rejectionOf(t, o.err, command.ReasonTaskAlreadyResolved)
		refused++
	}
	if ok != 1 || refused != 1 || h.lines() != before+1 {
		t.Errorf("%d successes, %d refusals, %d new lines; want one of each and one line", ok, refused, h.lines()-before)
	}
}

func TestDoChecksTheContextBeforeWaitingForTheWritePath(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h.e.OnCommit(func(engine.Version) {
		once.Do(func() { close(entered) })
		<-release
	})
	writer := make(chan error, 1)
	h.clock.Set(local(9, 5))
	go func() { _, err := h.try(command.CompleteTask{TaskID: taskOf(7, "plan-day")}); writer <- err }()
	<-entered // the write path is held by the commit's callback

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { _, err := h.e.Do(ctx, command.StartCycle{Type: deepWork}); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Do with a cancelled context = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("Do with a cancelled context waited for the write path")
		close(release)
		<-done
		<-writer
		return
	}
	close(release)
	if err := <-writer; err != nil {
		t.Fatalf("the held commit: %v", err)
	}
}

// TestReadersSeeConsistentSnapshotsWhileWriting runs Snapshot, State and
// Version against Do and SetConfig; under -race it also checks the locking.
// Every snapshot's version MUST describe its model and its configuration.
func TestReadersSeeConsistentSnapshotsWhileWriting(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	a := h.start(local(9, 0), deepWork)
	h.clock.Set(local(9, 10))

	var gens sync.Map // *config.Config -> its generation
	gens.Store(h.cfg, h.gen)
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				snap := h.e.Snapshot()
				if snap.Version.LogLines != snap.Model.Lines() {
					t.Errorf("Snapshot version %+v with a model of %d lines", snap.Version, snap.Model.Lines())
					return
				}
				if g, ok := gens.Load(snap.Config); !ok || g.(int64) != snap.Version.ConfigGeneration {
					t.Errorf("Snapshot config of generation %v (known %v), version %+v", g, ok, snap.Version)
					return
				}
				_ = h.e.State(local(9, 10))
				_ = h.e.Version()
			}
		})
	}
	var writers sync.WaitGroup
	writers.Go(func() {
		for range 40 {
			if _, err := h.try(command.BoostCycle{CycleID: a, Minutes: 5}); err != nil {
				t.Errorf("boost: %v", err)
				return
			}
		}
	})
	writers.Go(func() {
		for i := range 20 {
			next := loadConfig(t, nil)
			gen := h.gen + int64(i) + 1
			gens.Store(next, gen)
			if err := h.e.SetConfig(next, gen); err != nil {
				t.Errorf("SetConfig: %v", err)
				return
			}
		}
	})
	writers.Wait()
	close(stop)
	readers.Wait()
	if v := h.e.Version(); v.LogLines != h.lines() {
		t.Errorf("Version %+v, file %d lines", v, h.lines())
	}
}
