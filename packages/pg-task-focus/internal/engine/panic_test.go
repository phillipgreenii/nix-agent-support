package engine_test

import (
	"errors"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
)

// The contract for a panic in user code during a commit (an Observer method,
// an OnCommit callback, the AdoptFault seam): a panic is never swallowed, and
// it never leaves a writable store behind its model. A panic AFTER the
// candidate is adopted (Observer.Appended, Observer.Corrected, OnCommit)
// leaves the engine CONSISTENT: the request is recorded, the model and the
// version match the file, and the store stays writable. A panic BEFORE the
// candidate is adopted leaves the engine READ-ONLY with ReasonAdopt, as an
// adoption failure does, with the request recorded.

// panicky is a fakeObserver whose Appended, Recovered, Corrected or
// AppendFailed panics while armed.
type panicky struct {
	*fakeObserver
	appended, recovered, corrected, appendFailed atomic.Bool
}

func (p *panicky) Corrected(kind string) {
	if p.corrected.Load() {
		panic("injected observer panic")
	}
	p.fakeObserver.Corrected(kind)
}

func (p *panicky) AppendFailed(stage string) {
	if p.appendFailed.Load() {
		panic("injected observer panic in AppendFailed")
	}
	p.fakeObserver.AppendFailed(stage)
}

func (p *panicky) Appended(t event.Type, s store.AppendStats) {
	if p.appended.Load() {
		panic("injected observer panic")
	}
	p.fakeObserver.Appended(t, s)
}

func (p *panicky) Recovered(r store.Recovery) {
	if p.recovered.Load() {
		panic("injected observer panic")
	}
	p.fakeObserver.Recovered(r)
}

// openWith closes the engine and opens a new one on the directory with obs.
func (h *harness) openWith(obs engine.Observer) {
	h.t.Helper()
	if err := h.e.Close(); err != nil {
		h.t.Fatalf("Close: %v", err)
	}
	h.e = nil
	opts := h.options()
	opts.Observer = obs
	e, err := engine.Open(opts)
	if err != nil {
		h.t.Fatalf("Open: %v", err)
	}
	h.e = e
}

// panicOf runs f, which MUST panic, and returns the value it panicked with.
func panicOf(t *testing.T, f func()) (v any) {
	t.Helper()
	defer func() {
		if v = recover(); v == nil {
			t.Fatal("the call returned, want a panic")
		}
	}()
	f()
	return nil
}

// consistentAfter checks the engine after a panicking request c: the request
// is recorded once, the model and the version match the file, and a retry
// with the same id replays it.
func consistentAfter(t *testing.T, h *harness, c command.CompleteTask) {
	t.Helper()
	if n := h.countID(c.ID); n != 1 {
		t.Errorf("event %s is %d times in the log, want once", c.ID, n)
	}
	snap := h.e.Snapshot()
	if v := h.e.Version(); v.LogLines != h.lines() || snap.Version != v || snap.Model.Lines() != h.lines() {
		t.Errorf("file %d lines, Version %+v, Snapshot %+v with a model of %d lines: want them equal",
			h.lines(), v, snap.Version, snap.Model.Lines())
	}
	retry, err := h.try(c)
	if err != nil {
		t.Fatalf("the same-id retry: %v", err)
	}
	if !retry.Replayed || !slices.Equal(retry.EventIDs, []event.ID{c.ID}) {
		t.Errorf("the same-id retry %+v, want a replay of %s", retry, c.ID)
	}
	if n := h.countID(c.ID); n != 1 {
		t.Errorf("after the retry event %s is %d times in the log, want once", c.ID, n)
	}
}

func TestPanicAfterAdoptionLeavesTheEngineConsistentAndWritable(t *testing.T) {
	planDay := taskOf(7, "plan-day")
	arms := map[string]func(h *harness, obs *panicky){
		"Observer.Appended": func(_ *harness, obs *panicky) { obs.appended.Store(true) },
		"OnCommit": func(h *harness, _ *panicky) {
			h.e.OnCommit(func(engine.Version) { panic("injected callback panic") })
		},
	}
	for name, arm := range arms {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil)
			obs := &panicky{fakeObserver: newObserver()}
			h.openWith(obs)
			h.bootstrap()
			arm(h, obs)
			c := command.CompleteTask{ID: clientID(), TaskID: planDay}
			h.clock.Set(local(9, 5))
			panicOf(t, func() { _, _ = h.try(c) })
			obs.appended.Store(false)

			// Consistent, not read-only: the model is not behind the log.
			if hl := h.e.Health(); hl != (engine.Health{}) {
				t.Errorf("Health %+v, want a writable store", hl)
			}
			if got := h.task(planDay).Status; got != projection.Completed {
				t.Errorf("the model did not adopt the change: task %s", got)
			}
			if name == "OnCommit" {
				return // the callback keeps panicking on every commit; the rest needs none
			}
			consistentAfter(t, h, c)
			h.at(local(9, 10), command.SkipTask{TaskID: taskOf(7, "post-plan"), Reason: "not today"})
			h.reopen()
			consistentAfter(t, h, c)
		})
	}
	t.Run("OnCommit, then reopened", func(t *testing.T) {
		h := newHarness(t, nil)
		h.bootstrap()
		h.e.OnCommit(func(engine.Version) { panic("injected callback panic") })
		c := command.CompleteTask{ID: clientID(), TaskID: planDay}
		h.clock.Set(local(9, 5))
		panicOf(t, func() { _, _ = h.try(c) })
		consistentAfter(t, h, c) // a replay is not a commit: OnCommit does not run
		h.reopen()
		consistentAfter(t, h, c)
	})
}

// TestPanicInCorrectedLeavesTheEngineConsistentAndWritable checks the one
// Observer method the table above cannot reach, which only a correction or a
// retraction calls.
func TestPanicInCorrectedLeavesTheEngineConsistentAndWritable(t *testing.T) {
	planDay := taskOf(7, "plan-day")
	h := newHarness(t, nil)
	obs := &panicky{fakeObserver: newObserver()}
	h.openWith(obs)
	h.bootstrap()
	h.at(local(9, 5), command.CompleteTask{TaskID: planDay})
	completed := h.fileEvents()[h.lines()-1].ID
	obs.corrected.Store(true)
	c := command.Retract{ID: clientID(), Target: completed}
	h.clock.Set(local(9, 10))
	panicOf(t, func() { _, _ = h.try(c) })
	obs.corrected.Store(false)

	if hl := h.e.Health(); hl != (engine.Health{}) {
		t.Errorf("Health %+v, want a writable store", hl)
	}
	if got := h.task(planDay).Status; got != projection.Open {
		t.Errorf("the model did not adopt the retraction: task %s", got)
	}
	if n := h.countID(c.ID); n != 1 {
		t.Errorf("event %s is %d times in the log, want once", c.ID, n)
	}
	if v := h.e.Version(); v.LogLines != h.lines() || h.e.Snapshot().Model.Lines() != h.lines() {
		t.Errorf("file %d lines, Version %+v, model %d lines: want them equal", h.lines(), v, h.e.Snapshot().Model.Lines())
	}
	retry, err := h.try(c)
	if err != nil || !retry.Replayed || !slices.Equal(retry.EventIDs, []event.ID{c.ID}) {
		t.Errorf("the same-id retry %+v, %v, want a replay of %s", retry, err, c.ID)
	}
	h.at(local(9, 15), command.SkipTask{TaskID: planDay, Reason: "not today"})
}

// TestCommitAfterAPanickingOnCommitCallback checks that the request that
// panicked in OnCommit left the write path usable: the next request commits
// and runs the callback again.
func TestCommitAfterAPanickingOnCommitCallback(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	var calls atomic.Int32
	var last atomic.Int64
	h.e.OnCommit(func(v engine.Version) {
		last.Store(int64(v.LogLines))
		if calls.Add(1) == 1 {
			panic("injected callback panic")
		}
	})
	first := command.CompleteTask{ID: clientID(), TaskID: taskOf(7, "plan-day")}
	h.clock.Set(local(9, 5))
	panicOf(t, func() { _, _ = h.try(first) })
	consistentAfter(t, h, first)

	h.at(local(9, 10), command.SkipTask{TaskID: taskOf(7, "post-plan"), Reason: "not today"})
	if got := calls.Load(); got != 2 {
		t.Errorf("OnCommit ran %d times, want the panicking one and the next commit's", got)
	}
	if v := h.e.Version(); int64(v.LogLines) != last.Load() || v.LogLines != h.lines() {
		t.Errorf("Version %+v, last OnCommit %d, file %d lines", v, last.Load(), h.lines())
	}
	if got := h.task(taskOf(7, "post-plan")).Status; got != projection.Skipped {
		t.Errorf("the second commit did not take: task %s", got)
	}
}

// TestPanicWhileMarkingReadOnlyRaisesTheOriginal checks that a panic in
// Observer.AppendFailed or OnHealthChange, run while the store is made
// read-only after a panic before adoption, does not replace that panic, and
// that the store is read-only whichever of them panics.
func TestPanicWhileMarkingReadOnlyRaisesTheOriginal(t *testing.T) {
	for name, arm := range map[string]func(h *harness, obs *panicky){
		"Observer.AppendFailed": func(_ *harness, obs *panicky) { obs.appendFailed.Store(true) },
		"OnHealthChange": func(h *harness, _ *panicky) {
			h.e.OnHealthChange(func(engine.Health) { panic("injected health panic") })
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil)
			obs := &panicky{fakeObserver: newObserver()}
			h.openWith(obs)
			h.bootstrap()
			arm(h, obs)
			h.adopt = func() error { panic("injected adoption panic") }
			c := command.CompleteTask{ID: clientID(), TaskID: taskOf(7, "plan-day")}
			h.clock.Set(local(9, 5))
			got := panicOf(t, func() { _, _ = h.try(c) })
			h.adopt = nil
			if got != "injected adoption panic" {
				t.Errorf("panic value %v, want the adoption panic that started it", got)
			}
			if hl := h.e.Health(); !hl.ReadOnly || hl.Reason != store.ReasonAdopt {
				t.Errorf("Health %+v, want read-only with %s", hl, store.ReasonAdopt)
			}
		})
	}
}

func TestPanicBeforeAdoptionMakesTheStoreReadOnly(t *testing.T) {
	planDay := taskOf(7, "plan-day")
	h := newHarness(t, nil)
	h.bootstrap()
	var seen []engine.Health
	h.e.OnHealthChange(func(hl engine.Health) { seen = append(seen, hl) })
	h.adopt = func() error { panic("injected adoption panic") }
	c := command.CompleteTask{ID: clientID(), TaskID: planDay}
	h.clock.Set(local(9, 5))
	panicOf(t, func() { _, _ = h.try(c) })
	h.adopt = nil

	// Read-only, as an adoption failure: the model is behind the log.
	want := engine.Health{ReadOnly: true, Reason: store.ReasonAdopt, Since: local(9, 5)}
	if hl := h.e.Health(); hl != want {
		t.Errorf("Health %+v, want %+v", hl, want)
	}
	if !slices.Equal(seen, []engine.Health{want}) {
		t.Errorf("OnHealthChange saw %+v, want once %+v", seen, want)
	}
	if !slices.Equal(h.obs.appendFailed, []string{"project"}) {
		t.Errorf("AppendFailed %v, want [project]", h.obs.appendFailed)
	}
	if h.lines() != h.e.Version().LogLines+1 {
		t.Errorf("file %d lines, version %+v: want the change stored and the model behind", h.lines(), h.e.Version())
	}
	// The request is recorded: a same-id retry replays, read-only or not.
	retry := h.do(c)
	if !retry.Replayed || !slices.Equal(retry.EventIDs, []event.ID{c.ID}) {
		t.Errorf("the same-id retry %+v, want a replay of %s", retry, c.ID)
	}
	r := h.reject(command.SkipTask{TaskID: taskOf(7, "post-plan"), Reason: "x"}, command.ReasonStoreUnavailable)
	if r.Message != readOnlySentence(store.ReasonAdopt) {
		t.Errorf("message %q, want the READ-ONLY sentence", r.Message)
	}

	h.reopen()
	consistentAfter(t, h, c)
	if hl := h.e.Health(); hl != (engine.Health{}) {
		t.Errorf("after reopening Health %+v, want healthy", hl)
	}
}

func TestPanicInOpenReleasesTheDirectory(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	if err := h.e.Close(); err != nil {
		t.Fatal(err)
	}
	h.e = nil
	obs := &panicky{fakeObserver: newObserver()}
	obs.recovered.Store(true)
	opts := h.options()
	opts.Observer = obs
	panicOf(t, func() { _, _ = engine.Open(opts) })

	e, err := engine.Open(h.options())
	if errors.Is(err, store.ErrLocked) {
		t.Fatalf("Open after a panicking Open: %v, want the directory released", err)
	}
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h.e = e
}
