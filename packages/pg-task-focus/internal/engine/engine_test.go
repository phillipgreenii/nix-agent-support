package engine_test

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store/storefault"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/view"
)

func TestOpenReplaysAndReportsRecovery(t *testing.T) {
	h := newHarness(t, nil)
	if !slices.Equal(h.obs.replayed, []int{0}) {
		t.Errorf("a fresh directory: Replayed calls %v, want one with 0 events", h.obs.replayed)
	}
	if !slices.Equal(h.obs.recovered, []store.Recovery{{}}) {
		t.Errorf("a fresh directory: Recovered calls %+v, want one with nothing recovered", h.obs.recovered)
	}
	h.bootstrap()
	n := h.lines()

	if err := h.e.Close(); err != nil {
		t.Fatal(err)
	}
	h.e = nil
	torn := []byte(`{"v":1,"id":"01J`)
	f, err := os.OpenFile(h.path(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(torn); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	h.open()

	if !slices.Equal(h.obs.replayed, []int{n}) {
		t.Errorf("Replayed calls %v, want one with the %d committed events", h.obs.replayed, n)
	}
	if len(h.obs.recovered) != 1 {
		t.Fatalf("Recovered calls %+v, want one", h.obs.recovered)
	}
	rec := h.obs.recovered[0]
	if !rec.TornTail || rec.TruncatedBytes != int64(len(torn)) || rec.Sidecar == "" {
		t.Errorf("Recovered(%+v), want the torn tail of %d bytes cut to a sidecar", rec, len(torn))
	}
	if got := h.e.Version().LogLines; got != n || h.lines() != n {
		t.Errorf("LogLines %d, file %d lines, want %d", got, h.lines(), n)
	}
	if p := h.e.Snapshot().Model.Profile(); p != "normal" {
		t.Errorf("the replayed profile is %q, want normal", p)
	}
}

func TestDoAppendsFsyncsThenAdopts(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	planDay := taskOf(7, "plan-day")
	before := h.e.Snapshot()
	id := clientID()
	complete := command.CompleteTask{ID: id, TaskID: planDay}

	h.fs.Inject(storefault.Rule{Op: storefault.OpWrite, Name: logName, Partial: 7})
	h.clock.Set(local(9, 5))
	_, err := h.try(complete)
	rejectionOf(t, err, command.ReasonStoreUnavailable)
	h.requireNoPending()
	failed := h.e.Snapshot()
	if !reflect.DeepEqual(failed.Model.Domain(), before.Model.Domain()) || failed.Version != before.Version {
		t.Errorf("a failed append changed the model or the version (%+v, want %+v)", failed.Version, before.Version)
	}
	if h.lines() != before.Version.LogLines {
		t.Errorf("the file has %d lines after a rolled-back append, want %d", h.lines(), before.Version.LogLines)
	}

	// At the moment of adoption the line is written and synced, and the
	// model and the version have not moved yet.
	h.fs.ResetCalls()
	var atAdopt struct {
		lines   int
		version engine.Version
		status  projection.TaskStatus
		calls   []storefault.Call
	}
	h.adopt = func() error {
		atAdopt.lines, atAdopt.version, atAdopt.calls = h.lines(), h.e.Version(), h.fs.Calls()
		atAdopt.status = h.task(planDay).Status
		return nil
	}
	r := h.do(complete)
	if atAdopt.lines != before.Version.LogLines+1 || atAdopt.version != before.Version || atAdopt.status != projection.Open {
		t.Errorf("at adoption: %d lines, version %+v, task %s; want the line durable and the model not adopted yet", atAdopt.lines, atAdopt.version, atAdopt.status)
	}
	var ops []storefault.Op
	for _, c := range atAdopt.calls {
		if strings.HasSuffix(c.Name, logName) {
			ops = append(ops, c.Op)
		}
	}
	if !slices.Equal(ops, []storefault.Op{storefault.OpWrite, storefault.OpSync}) {
		t.Errorf("calls on the log before adoption %v, want one Write and then one Sync", ops)
	}
	if !r.Changed || !slices.Equal(r.EventIDs, []event.ID{id}) || r.Replayed {
		t.Errorf("Result %+v, want the completion %s, changed", r, id)
	}
	if got := h.task(planDay).Status; got != projection.Completed {
		t.Errorf("after the commit the task is %s, want completed", got)
	}
	if v := h.e.Version(); v.LogLines != before.Version.LogLines+1 || v.LogLines != h.lines() || r.Version != v {
		t.Errorf("Version %+v, Result.Version %+v, file %d lines", v, r.Version, h.lines())
	}
}

// reasonCase reaches one reason through Do: run prepares the engine and
// returns the request that is refused.
type reasonCase struct {
	want command.Reason
	edit func(c map[string]any)
	run  func(h *harness) command.Command
}

// dropPostPlan takes post-plan out of the on-call profile, so changing to it
// withdraws that task.
func dropPostPlan(c map[string]any) {
	oncall := c["profiles"].(map[string]any)["on-call"].(map[string]any)
	oncall["daily"] = []any{"plan-day", "end-of-day-summary"}
}

// addPageReview adds a daily task that only the on-call profile lists, so
// changing to it materializes that task.
func addPageReview(c map[string]any) {
	c["tasks"].(map[string]any)["page-review"] = map[string]any{
		"title": "Review the pages", "cadence": "daily", "due": map[string]any{"at": "10:00", "tz": newYork},
	}
	oncall := c["profiles"].(map[string]any)["on-call"].(map[string]any)
	oncall["daily"] = append(oncall["daily"].([]any), "page-review")
}

// rawInstant is an instant as a correction field carries it.
func rawInstant(t *testing.T, at time.Time) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(event.At(at))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// profileEventOf is the stored profile.changed of the bootstrap.
func (h *harness) profileEventOf() event.ID {
	h.t.Helper()
	views := h.e.Snapshot().Model.Events(projection.EventQuery{Types: []event.Type{event.TypeProfileChanged}})
	if len(views) == 0 {
		h.t.Fatal("no profile.changed in the log")
	}
	return views[0].Original.ID
}

func reasonCases(t *testing.T) []reasonCase {
	planDay, postPlan := taskOf(7, "plan-day"), taskOf(7, "post-plan")
	unknownCycle := event.CycleID(idOf('X', 1))
	return []reasonCase{
		{want: command.ReasonInvalidRequest, run: func(*harness) command.Command {
			return command.SkipTask{TaskID: postPlan, Reason: "  "}
		}},
		{want: command.ReasonInvalidZone, run: func(*harness) command.Command {
			return command.ChangePeriods{Changes: []command.PeriodChange{{Kind: projection.Day, Start: dayOf(8), TZ: "ET"}}}
		}},
		{want: command.ReasonUnknownProfile, run: func(*harness) command.Command {
			return command.ChangeProfile{Profile: "weekend"}
		}},
		{want: command.ReasonUnknownCycleType, run: func(*harness) command.Command {
			return command.StartCycle{Type: "nap"}
		}},
		{want: command.ReasonReservedKey, run: func(h *harness) command.Command {
			c := h.start(local(9, 0), deepWork)
			return command.AnnotateCycle{CycleID: c, KV: []event.KV{{Key: "cycle_type", Value: "x"}}}
		}},
		{want: command.ReasonCycleAmbiguous, run: func(h *harness) command.Command {
			h.start(local(9, 0), deepWork)
			h.start(local(9, 10), review)
			h.clock.Set(local(9, 20))
			return command.ResumeCycle{}
		}},
		{want: command.ReasonUnknownTask, run: func(*harness) command.Command {
			return command.CompleteTask{TaskID: taskOf(7, "water-plants")}
		}},
		{want: command.ReasonUnknownCycle, run: func(*harness) command.Command {
			return command.PauseCycle{CycleID: unknownCycle}
		}},
		{want: command.ReasonUnknownEvent, run: func(*harness) command.Command {
			return command.Retract{Target: idOf('X', 2)}
		}},
		{want: command.ReasonNoRunningCycle, run: func(*harness) command.Command {
			return command.StopCycle{}
		}},
		{want: command.ReasonCycleStopped, run: func(h *harness) command.Command {
			c := h.start(local(9, 0), deepWork)
			h.at(local(9, 30), command.StopCycle{CycleID: c})
			h.clock.Set(local(9, 40))
			return command.PauseCycle{CycleID: c}
		}},
		{want: command.ReasonAnotherCycleRunning, run: func(h *harness) command.Command {
			a := h.start(local(9, 0), deepWork)
			h.at(local(9, 10), command.PauseCycle{CycleID: a})
			h.start(local(9, 15), review)
			h.clock.Set(local(9, 20))
			return command.ResumeCycle{CycleID: a}
		}},
		{want: command.ReasonInterruptedCycleNotRunning, run: func(h *harness) command.Command {
			a := h.start(local(9, 0), deepWork)
			h.start(local(9, 5), review)
			sw := h.at(local(9, 10), command.SwitchCycle{To: a})
			h.start(local(9, 15), notifications) // interrupts a, which only the switch set running
			h.clock.Set(local(9, 30))
			return command.Retract{TargetBatch: sw.BatchID}
		}},
		{want: command.ReasonCycleActive, run: func(h *harness) command.Command {
			h.start(local(9, 0), deepWork)
			h.clock.Set(local(9, 10))
			return rolloverCmd("", 8)
		}},
		{want: command.ReasonCycleSegmentsOverlap, run: func(h *harness) command.Command {
			c := h.start(local(9, 0), deepWork)
			h.at(local(9, 30), command.PauseCycle{CycleID: c})
			h.at(local(9, 40), command.ResumeCycle{CycleID: c})
			h.clock.Set(local(10, 0))
			return command.BackfillBreak{CycleID: c, From: local(9, 20), To: local(9, 35)}
		}},
		{want: command.ReasonBreakEndsAtStop, run: func(h *harness) command.Command {
			c := h.start(local(9, 0), deepWork)
			h.at(local(10, 0), command.StopCycle{CycleID: c})
			h.clock.Set(local(10, 5))
			return command.BackfillBreak{CycleID: c, From: local(9, 30), To: local(10, 0)}
		}},
		{want: command.ReasonClockBehindLog, run: func(h *harness) command.Command {
			h.clock.Set(local(8, 40)) // the bootstrap was recorded at 08:50
			return command.CompleteTask{TaskID: planDay}
		}},
		{want: command.ReasonTaskAlreadyResolved, run: func(h *harness) command.Command {
			h.at(local(9, 5), command.CompleteTask{TaskID: planDay})
			h.clock.Set(local(9, 10))
			return command.CompleteTask{TaskID: planDay}
		}},
		{want: command.ReasonTaskWithdrawn, edit: dropPostPlan, run: func(h *harness) command.Command {
			h.at(local(9, 0), command.ChangeProfile{Profile: "on-call"})
			h.clock.Set(local(9, 10))
			return command.CompleteTask{TaskID: postPlan}
		}},
		{want: command.ReasonTaskNotWithdrawn, edit: dropPostPlan, run: func(h *harness) command.Command {
			withdrawal := h.at(local(9, 0), command.ChangeProfile{Profile: "on-call"})
			h.at(local(9, 10), command.ChangeProfile{Profile: "normal"}) // reinstates post-plan
			h.clock.Set(local(9, 20))
			return command.Retract{TargetBatch: withdrawal.BatchID}
		}},
		{want: command.ReasonTaskMaterializedTwice, edit: addPageReview, run: func(h *harness) command.Command {
			first := h.at(local(9, 0), command.ChangeProfile{Profile: "on-call"})
			undo := h.at(local(9, 10), command.Retract{TargetBatch: first.BatchID})
			h.at(local(9, 20), command.ChangeProfile{Profile: "on-call"}) // materializes page-review again
			h.clock.Set(local(9, 30))
			return command.Retract{Target: undo.EventIDs[0]}
		}},
		{want: command.ReasonTaskWithoutPeriod, edit: addPageReview, run: func(h *harness) command.Command {
			roll := h.at(localOn(8, 9, 0), rolloverCmd("", 8))
			h.at(localOn(8, 9, 20), command.ChangeProfile{Profile: "on-call"})
			h.clock.Set(localOn(8, 9, 30))
			return command.Retract{TargetBatch: roll.BatchID}
		}},
		{want: command.ReasonTaskBeforeProfile, run: func(h *harness) command.Command {
			h.clock.Set(local(9, 0))
			return command.Correct{Target: h.profileEventOf(), Fields: map[string]json.RawMessage{"effective_at": rawInstant(t, local(8, 55))}}
		}},
		{want: command.ReasonPeriodUnchanged, run: func(*harness) command.Command {
			return rolloverCmd("", 7)
		}},
		{want: command.ReasonIDConflict, run: func(h *harness) command.Command {
			id := clientID()
			h.at(local(9, 5), command.CompleteTask{ID: id, TaskID: planDay})
			return command.SkipTask{ID: id, TaskID: postPlan, Reason: "not today"}
		}},
		{want: command.ReasonStalePreview, run: func(*harness) command.Command {
			return command.ChangeProfile{Profile: "on-call", ExpectedVersion: &command.Version{LogLines: 1, ConfigGeneration: 1}}
		}},
		{want: command.ReasonBatchHasDependents, run: func(h *harness) command.Command {
			roll := h.at(localOn(8, 9, 0), rolloverCmd("", 8))
			h.at(localOn(8, 9, 5), command.CompleteTask{TaskID: taskOf(8, "plan-day")})
			h.clock.Set(localOn(8, 9, 10))
			return command.Retract{TargetBatch: roll.BatchID}
		}},
		{want: command.ReasonCycleHasDependents, run: func(h *harness) command.Command {
			start := clientID()
			h.at(local(9, 0), command.StartCycle{ID: start, Type: deepWork})
			h.at(local(9, 10), command.PauseCycle{})
			h.clock.Set(local(9, 20))
			return command.Retract{Target: start}
		}},
		{want: command.ReasonInvalidCorrection, run: func(h *harness) command.Command {
			return command.Retract{Target: h.profileEventOf()} // a batch member, alone
		}},
		{want: command.ReasonFutureEffectiveAt, run: func(h *harness) command.Command {
			return command.CompleteTask{TaskID: planDay, EffectiveAt: ptr(h.clock.Now().Add(2 * time.Hour))}
		}},
		{want: command.ReasonStopNotAfterStart, run: func(h *harness) command.Command {
			c := h.start(local(9, 0), deepWork)
			h.clock.Set(local(9, 10))
			return command.StopCycle{CycleID: c, EffectiveAt: ptr(local(9, 0))}
		}},
		{want: command.ReasonEmptyRunningSegment, run: func(h *harness) command.Command {
			h.start(local(9, 0), deepWork)
			h.clock.Set(local(9, 10))
			return command.StartCycle{Type: review, EffectiveAt: ptr(local(9, 0))}
		}},
		{want: command.ReasonCycleEventBeforeStart, run: func(h *harness) command.Command {
			c := h.start(local(9, 0), deepWork)
			h.clock.Set(local(9, 10))
			return command.PauseCycle{CycleID: c, EffectiveAt: ptr(local(8, 55))}
		}},
		{want: command.ReasonResolutionBeforeMaterialization, run: func(*harness) command.Command {
			return command.CompleteTask{TaskID: planDay, EffectiveAt: ptr(local(8, 40))}
		}},
		{want: command.ReasonPeriodOutOfOrder, run: func(*harness) command.Command {
			roll := rolloverCmd("", 8)
			roll.EffectiveAt = ptr(local(8, 40)) // before the bootstrap
			return roll
		}},
		{want: command.ReasonStoreUnavailable, run: func(h *harness) command.Command {
			h.fs.Inject(storefault.Rule{Op: storefault.OpWrite, Name: logName}) // rolled back
			return command.CompleteTask{TaskID: planDay}
		}},
	}
}

func TestRejectionAppendsNothingAndCountsReason(t *testing.T) {
	covered := map[command.Reason]bool{}
	for _, tc := range reasonCases(t) {
		covered[tc.want] = true
		t.Run(string(tc.want), func(t *testing.T) {
			var cfg *config.Config
			if tc.edit != nil {
				cfg = loadConfig(t, tc.edit)
			}
			h := newHarness(t, cfg)
			h.bootstrap()
			c := tc.run(h)
			if n := h.obs.rejections(); n != 0 {
				t.Fatalf("the setup was refused %d times: %v", n, h.obs.rejected)
			}
			version, appended := h.e.Version(), h.obs.appendedCount()
			h.reject(c, tc.want)
			if v := h.e.Version(); v != version {
				t.Errorf("Version %+v after a refusal, want %+v", v, version)
			}
			if h.obs.appendedCount() != appended {
				t.Error("Observer.Appended was called for a refusal")
			}
			if got := h.obs.rejected[tc.want]; got != 1 || h.obs.rejections() != 1 {
				t.Errorf("Observer.Rejected counts %v, want %s once", h.obs.rejected, tc.want)
			}
		})
	}
	for _, r := range command.Reasons() {
		if r != command.ReasonNotReady && !covered[r] {
			t.Errorf("reason %s is not exercised through Do", r)
		}
	}
}

func TestReadOnlyModeRejectsEveryMutationAndKeepsReads(t *testing.T) {
	h := newHarness(t, nil)
	boot := h.bootstrap()
	c := h.start(local(9, 0), deepWork)
	failedAt := local(9, 10)
	h.clock.Set(failedAt)
	h.injectOnLog(storefault.OpSync)
	_, err := h.try(command.PauseCycle{CycleID: c})
	rejectionOf(t, err, command.ReasonStoreUnavailable)
	h.requireNoPending()

	h.clock.Set(local(9, 20))
	version := h.e.Version()
	mutations := []command.Command{
		command.CompleteTask{TaskID: taskOf(7, "plan-day")},
		command.SkipTask{TaskID: taskOf(7, "post-plan"), Reason: "not today"},
		command.StartCycle{Type: review},
		command.PauseCycle{CycleID: c},
		command.ResumeCycle{ID: clientID(), CycleID: c},
		command.BoostCycle{CycleID: c, Minutes: 5},
		command.StopCycle{CycleID: c},
		command.SwitchCycle{To: c},
		command.AnnotateCycle{CycleID: c, Note: "a note"},
		command.BackfillBreak{CycleID: c, From: local(9, 1), To: local(9, 2)},
		rolloverCmd(clientID(), 8),
		command.ChangeProfile{Profile: "on-call"},
		command.Correct{Target: boot.EventIDs[0], Fields: map[string]json.RawMessage{"effective_at": rawInstant(t, local(8, 51))}},
		command.Retract{TargetBatch: boot.BatchID},
	}
	want := readOnlySentence(store.ReasonAppendSync)
	for _, m := range mutations {
		if r := h.reject(m, command.ReasonStoreUnavailable); r.Message != want {
			t.Errorf("%T: message %q, want %q", m, r.Message, want)
		}
	}
	if v := h.e.Version(); v != version {
		t.Errorf("Version %+v, want %+v", v, version)
	}

	// Reads keep working, and carry the mode.
	wantHealth := engine.Health{ReadOnly: true, Reason: store.ReasonAppendSync, Since: failedAt}
	if got := h.e.Health(); got != wantHealth {
		t.Errorf("Health() = %+v, want %+v", got, wantHealth)
	}
	st := h.e.State(local(9, 20))
	if st.Store != (view.StoreHealth{ReadOnly: true, Reason: string(store.ReasonAppendSync), Since: failedAt}) {
		t.Errorf("State.Store = %+v", st.Store)
	}
	if st.Focus == nil || st.Focus.Cycle.ID != c || !st.Initialized {
		t.Errorf("State while read-only: focus %+v, initialized %v", st.Focus, st.Initialized)
	}
	if snap := h.e.Snapshot(); snap.Version != version || snap.Model.Lines() != version.LogLines {
		t.Errorf("Snapshot version %+v with %d lines, want %+v", snap.Version, snap.Model.Lines(), version)
	}
}

// readOnlyFault makes the next append fail so that the store ends read-only
// with the given reason; the adoption failure goes through AdoptFault.
type readOnlyFault struct {
	reason store.ReadOnlyReason
	stage  string
	inject func(h *harness)
}

func readOnlyFaults() []readOnlyFault {
	return []readOnlyFault{
		{store.ReasonWriteRollbackTruncate, "write", func(h *harness) { h.injectOnLog(storefault.OpWrite, storefault.OpTruncate) }},
		{store.ReasonWriteRollbackSync, "write", func(h *harness) { h.injectOnLog(storefault.OpWrite, storefault.OpSync) }},
		{store.ReasonAppendSync, "fsync", func(h *harness) { h.injectOnLog(storefault.OpSync) }},
		{store.ReasonAdopt, "project", func(h *harness) { h.adopt = func() error { return errors.New("injected adoption failure") } }},
	}
}

func TestHealthReportsReadOnlyReasonAndSince(t *testing.T) {
	for _, f := range readOnlyFaults() {
		t.Run(string(f.reason), func(t *testing.T) {
			h := newHarness(t, nil)
			h.bootstrap()
			if got, st := h.e.Health(), h.e.State(local(9, 0)).Store; got != (engine.Health{}) || st != (view.StoreHealth{}) {
				t.Fatalf("a healthy engine reports %+v and %+v, want the zero values", got, st)
			}
			failedAt := local(9, 5)
			h.clock.Set(failedAt)
			f.inject(h)
			_, err := h.try(command.CompleteTask{ID: clientID(), TaskID: taskOf(7, "plan-day")})
			rejectionOf(t, err, command.ReasonStoreUnavailable)
			h.requireNoPending()

			want := engine.Health{ReadOnly: true, Reason: f.reason, Since: failedAt}
			h.clock.Set(local(9, 30))
			if got := h.e.Health(); got != want {
				t.Errorf("Health() = %+v, want %+v", got, want)
			}
			if st := h.e.State(local(9, 30)).Store; st != (view.StoreHealth{ReadOnly: true, Reason: string(f.reason), Since: failedAt}) {
				t.Errorf("State.Store = %+v, want it to agree with Health %+v", st, want)
			}
			if !slices.Equal(h.obs.appendFailed, []string{f.stage}) {
				t.Errorf("AppendFailed calls %v, want [%s]", h.obs.appendFailed, f.stage)
			}

			h.adopt = nil
			h.reopen()
			if got, st := h.e.Health(), h.e.State(local(9, 30)).Store; got != (engine.Health{}) || st != (view.StoreHealth{}) {
				t.Errorf("after reopening: %+v and %+v, want the zero values", got, st)
			}
		})
	}
}

func TestStateVersionAdvancesOnCommitAndOnReload(t *testing.T) {
	h := newHarness(t, nil)
	if v := h.e.Version(); v != (engine.Version{LogLines: 0, ConfigGeneration: h.gen}) {
		t.Fatalf("a fresh engine is at %+v", v)
	}
	var seen []engine.Version
	h.e.OnCommit(func(v engine.Version) { seen = append(seen, v) })

	r := h.bootstrap()
	committed := h.e.Version()
	if committed.LogLines != h.lines() || committed.ConfigGeneration != h.gen || r.Version != committed {
		t.Errorf("after a commit: Version %+v, Result.Version %+v, file %d lines", committed, r.Version, h.lines())
	}
	if err := h.e.SetConfig(loadConfig(t, nil), h.gen+1); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	reloaded := h.e.Version()
	if reloaded != (engine.Version{LogLines: committed.LogLines, ConfigGeneration: h.gen + 1}) {
		t.Errorf("after a reload: Version %+v", reloaded)
	}
	if snap := h.e.Snapshot(); snap.Version != reloaded {
		t.Errorf("Snapshot().Version %+v, want %+v", snap.Version, reloaded)
	}
	if !slices.Equal(seen, []engine.Version{committed, reloaded}) {
		t.Errorf("OnCommit saw %+v, want the commit and the reload", seen)
	}
	if committed == reloaded {
		t.Error("a reload did not change the version")
	}
}

func TestSetConfigRejectsRemovedActiveProfile(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	calls := 0
	h.e.OnCommit(func(engine.Version) { calls++ })
	version := h.e.Version()

	withoutNormal := loadConfig(t, func(c map[string]any) {
		delete(c["profiles"].(map[string]any), "normal")
		c["defaults"].(map[string]any)["profile"] = "on-call"
	})
	err := h.e.SetConfig(withoutNormal, h.gen+1)
	var verr *config.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("SetConfig = %v, want a *config.ValidationError", err)
	}
	if snap := h.e.Snapshot(); snap.Config != h.cfg || snap.Version != version || calls != 0 {
		t.Errorf("a refused reload swapped the config (%v), moved the version (%+v) or ran OnCommit (%d)", snap.Config != h.cfg, snap.Version, calls)
	}

	withoutOnCall := loadConfig(t, func(c map[string]any) { delete(c["profiles"].(map[string]any), "on-call") })
	if err := h.e.SetConfig(withoutOnCall, h.gen+2); err != nil {
		t.Fatalf("removing a profile that is not active: %v", err)
	}
	if snap := h.e.Snapshot(); snap.Config != withoutOnCall || calls != 1 {
		t.Errorf("an accepted reload: config swapped %v, OnCommit calls %d", snap.Config == withoutOnCall, calls)
	}
}

func TestOnCommitCallbacksRunAfterAdopt(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	planDay := taskOf(7, "plan-day")
	type seen struct {
		version engine.Version
		status  projection.TaskStatus
		lines   int
	}
	var first, second []seen
	h.e.OnCommit(func(v engine.Version) { first = append(first, seen{v, h.task(planDay).Status, h.lines()}) })
	h.e.OnCommit(func(v engine.Version) { second = append(second, seen{v, h.task(planDay).Status, h.lines()}) })

	h.at(local(9, 5), command.CompleteTask{TaskID: planDay})
	want := []seen{{h.e.Version(), projection.Completed, h.lines()}}
	if !slices.Equal(first, want) || !slices.Equal(second, want) {
		t.Errorf("OnCommit saw %+v and %+v, want %+v", first, second, want)
	}

	// Neither a refusal, a no-op nor a dry run is a commit.
	h.reject(command.CompleteTask{TaskID: planDay}, command.ReasonTaskAlreadyResolved)
	c := h.start(local(9, 10), deepWork)
	first, second = nil, nil
	h.at(local(9, 15), command.ResumeCycle{CycleID: c})
	dry := rolloverCmd(clientID(), 8)
	dry.DryRun = true
	h.do(dry)
	if len(first) != 0 || len(second) != 0 {
		t.Errorf("OnCommit ran for a no-op or a dry run: %+v", first)
	}
}

func TestDryRunNeverAppends(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	c := h.start(local(9, 0), deepWork)
	before, version := h.logBytes(), h.e.Version()
	appended := h.obs.appendedCount()

	h.clock.Set(local(9, 10))
	roll := rolloverCmd(clientID(), 8)
	roll.DryRun = true
	r := h.do(roll)
	if r.Preview == nil || r.Changed || len(r.EventIDs) != 0 || r.BatchID != "" || r.Replayed {
		t.Fatalf("dry run Result %+v, want only a preview", r)
	}
	if len(r.Preview.BlockingCycles) != 1 || r.Preview.BlockingCycles[0].ID != c {
		t.Errorf("BlockingCycles %+v, want the running cycle %s", r.Preview.BlockingCycles, c)
	}
	if r.Preview.Version != version || r.Version != version {
		t.Errorf("preview version %+v, result version %+v, want %+v", r.Preview.Version, r.Version, version)
	}
	var leaving []event.TaskID
	for _, ref := range r.Preview.Leaving {
		leaving = append(leaving, ref.ID)
	}
	if !slices.Contains(leaving, taskOf(7, "post-plan")) {
		t.Errorf("Leaving %v, want the open post-plan", leaving)
	}

	profile := h.do(command.ChangeProfile{ID: clientID(), Profile: "on-call", DryRun: true})
	if profile.Preview == nil || profile.Changed {
		t.Errorf("profile dry run Result %+v, want a preview", profile)
	}
	if !slices.Equal(before, h.logBytes()) || h.e.Version() != version || h.obs.appendedCount() != appended {
		t.Error("a dry run appended to the log or moved the version")
	}
}

// engineFiles parses the non-test Go files of the module, by directory
// relative to the module root.
func engineFiles(t *testing.T) map[string][]*ast.File {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]*ast.File{}
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = append(out[filepath.ToSlash(rel)], f)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out["internal/engine"]) == 0 {
		t.Fatal("found no source files of internal/engine")
	}
	return out
}

// methodCalls lists the names of the methods called in n among names.
func methodCalls(n ast.Node, names ...string) []string {
	var out []string
	ast.Inspect(n, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && slices.Contains(names, sel.Sel.Name) {
				out = append(out, sel.Sel.Name)
			}
		}
		return true
	})
	return out
}

// assignsField reports whether n assigns to a field called name.
func assignsField(n ast.Node, name string) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok {
			for _, lhs := range as.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
					found = true
				}
			}
		}
		return true
	})
	return found
}

// readsField reports whether n reads a field or method called name.
func readsField(n ast.Node, name string) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
			found = true
		}
		return true
	})
	return found
}

func TestOnlyTheEngineWritesTheStoreAndTheModel(t *testing.T) {
	t.Run("architecture", func(t *testing.T) {
		files := engineFiles(t)
		for dir, fs := range files {
			if dir == "internal/engine" || dir == "internal/store" {
				continue
			}
			for _, f := range fs {
				if calls := methodCalls(f, "Append", "MarkReadOnly"); len(calls) > 0 {
					t.Errorf("package %s calls %v: only the engine writes the store", dir, calls)
				}
			}
		}

		var writers, adopters []string
		for _, f := range files["internal/engine"] {
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok {
					continue
				}
				if len(methodCalls(fn, "Append")) > 0 {
					writers = append(writers, fn.Name.Name)
					if !readsField(fn, "Candidate") {
						t.Errorf("%s appends without adopting the plan's candidate", fn.Name.Name)
					}
				}
				if assignsField(fn, "model") {
					adopters = append(adopters, fn.Name.Name)
				}
			}
		}
		if len(writers) != 1 {
			t.Fatalf("functions that append %v, want exactly one", writers)
		}
		if !slices.Equal(adopters, writers) {
			t.Errorf("functions that adopt a model %v, want only the writer %v", adopters, writers)
		}

		storeType := reflect.TypeFor[*store.Store]()
		opts := reflect.TypeFor[engine.Options]()
		for i := range opts.NumField() {
			if f := opts.Field(i); f.Type == storeType || strings.Contains(f.Type.String(), "store.Store") {
				t.Errorf("engine.Options.%s (%s) hands in an open store", f.Name, f.Type)
			}
		}
	})

	t.Run("behavior", func(t *testing.T) {
		h := newHarness(t, nil)
		h.bootstrap()
		before := h.e.Snapshot()
		h.injectOnLog(storefault.OpWrite)
		h.clock.Set(local(9, 5))
		_, err := h.try(command.CompleteTask{TaskID: taskOf(7, "plan-day")})
		rejectionOf(t, err, command.ReasonStoreUnavailable)
		after := h.e.Snapshot()
		if !reflect.DeepEqual(after.Model.Domain(), before.Model.Domain()) || h.e.Version() != before.Version {
			t.Error("a failed append moved the model or the version")
		}
		// The rolled-back append left the store writable: the same request
		// commits, and a refusal moves nothing.
		for _, tc := range []struct {
			c    command.Command
			want command.Reason // empty: the request commits
		}{
			{command.CompleteTask{TaskID: taskOf(7, "plan-day")}, ""},
			{command.StartCycle{Type: deepWork}, ""},
			{command.SwitchCycle{To: "unknown"}, command.ReasonUnknownCycle},
		} {
			c := tc.c
			r, err := h.try(c)
			switch {
			case tc.want != "":
				rejectionOf(t, err, tc.want)
			case err != nil:
				t.Errorf("%T: %v, want it to commit", c, err)
			case !r.Changed || r.Version != h.e.Version():
				t.Errorf("%T: Result %+v, want a commit at the present version %+v", c, r, h.e.Version())
			}
			if h.lines() != h.e.Version().LogLines {
				t.Errorf("after %T the file has %d lines and Version().LogLines is %d", c, h.lines(), h.e.Version().LogLines)
			}
		}
	})
}

func TestReadOnlyRefusesANewIdLessNoOp(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	c := h.start(local(9, 0), deepWork)
	h.at(local(9, 10), command.StopCycle{CycleID: c})
	h.injectOnLog(storefault.OpSync)
	h.clock.Set(local(9, 20))
	_, err := h.try(command.AnnotateCycle{CycleID: c, Note: "done"})
	rejectionOf(t, err, command.ReasonStoreUnavailable)

	// A stop of the stopped cycle would be a no-op, but the gate comes first.
	r := h.reject(command.StopCycle{CycleID: c}, command.ReasonStoreUnavailable)
	if r.Message != readOnlySentence(store.ReasonAppendSync) {
		t.Errorf("message %q, want the READ-ONLY sentence", r.Message)
	}
}

func TestAppendFsyncFailedRequestGetsRetryGuidance(t *testing.T) {
	for _, withID := range []bool{true, false} {
		h := newHarness(t, nil)
		h.bootstrap()
		h.injectOnLog(storefault.OpSync)
		h.clock.Set(local(9, 5))
		c := command.CompleteTask{TaskID: taskOf(7, "plan-day")}
		if withID {
			c.ID = clientID()
		}
		_, err := h.try(c)
		r := rejectionOf(t, err, command.ReasonStoreUnavailable)
		want := readOnlySentence(store.ReasonAppendSync) + ". " + unknownReadOnly
		if r.Message != want {
			t.Errorf("with an id %v: message %q, want %q", withID, r.Message, want)
		}
		if !h.e.Health().ReadOnly || !slices.Equal(h.obs.appendFailed, []string{"fsync"}) {
			t.Errorf("Health %+v, AppendFailed %v", h.e.Health(), h.obs.appendFailed)
		}
	}
}

func TestNoOpAppendsNothingAndKeepsTheVersion(t *testing.T) {
	h := newHarness(t, nil)
	h.bootstrap()
	a := h.start(local(9, 0), deepWork)
	h.at(local(9, 5), command.StopCycle{CycleID: a})
	b := h.start(local(9, 10), review)
	c := h.start(local(9, 15), notifications) // interrupts b
	skip := h.at(local(9, 16), command.SkipTask{TaskID: taskOf(7, "post-plan"), Reason: "not today"})
	correction := h.at(local(9, 17), command.Correct{Target: skip.EventIDs[0], Fields: map[string]json.RawMessage{"reason": json.RawMessage(`"later"`)}})
	h.at(local(9, 18), command.Retract{Target: correction.EventIDs[0]})
	h.clock.Set(local(9, 20))

	commits := 0
	h.e.OnCommit(func(engine.Version) { commits++ })
	before, version, domain := h.logBytes(), h.e.Version(), h.e.Snapshot().Model.Domain()
	appended := h.obs.appendedCount()

	repeats := map[string]func(id event.ID) command.Command{
		"pause of a paused cycle": func(id event.ID) command.Command { return command.PauseCycle{ID: id, CycleID: b} },
		"resume of a running one": func(id event.ID) command.Command { return command.ResumeCycle{ID: id, CycleID: c} },
		"stop of a stopped one":   func(id event.ID) command.Command { return command.StopCycle{ID: id, CycleID: a} },
		"switch to the focus":     func(id event.ID) command.Command { return command.SwitchCycle{ID: id, To: c} },
		"retraction of a retracted correction": func(id event.ID) command.Command {
			return command.Retract{ID: id, Target: correction.EventIDs[0]}
		},
	}
	for name, mk := range repeats {
		for _, id := range []event.ID{"", clientID()} {
			r := h.do(mk(id))
			if r.Changed || r.Note == "" || len(r.EventIDs) != 0 || r.BatchID != "" || r.Replayed || r.Version != version {
				t.Errorf("%s (id %q): Result %+v, want a no-op with a note", name, id, r)
			}
		}
	}
	if !slices.Equal(before, h.logBytes()) || h.e.Version() != version {
		t.Error("a no-op changed the log or the version")
	}
	if !reflect.DeepEqual(h.e.Snapshot().Model.Domain(), domain) {
		t.Error("a no-op changed the model")
	}
	if commits != 0 || h.obs.appendedCount() != appended || h.obs.rejections() != 0 {
		t.Errorf("OnCommit %d, Appended %d more, Rejected %v: want none", commits, h.obs.appendedCount()-appended, h.obs.rejected)
	}
}

func TestEnteringReadOnlyFiresHealthChange(t *testing.T) {
	for _, f := range readOnlyFaults() {
		t.Run(string(f.reason), func(t *testing.T) {
			h := newHarness(t, nil)
			var seen []engine.Health
			h.e.OnHealthChange(func(hl engine.Health) { seen = append(seen, hl) })
			h.bootstrap()
			h.at(local(9, 0), command.CompleteTask{TaskID: taskOf(7, "plan-day")})
			if len(seen) != 0 {
				t.Fatalf("a healthy commit fired OnHealthChange: %+v", seen)
			}
			h.clock.Set(local(9, 5))
			f.inject(h)
			_, err := h.try(command.SkipTask{TaskID: taskOf(7, "post-plan"), Reason: "not today"})
			rejectionOf(t, err, command.ReasonStoreUnavailable)
			want := engine.Health{ReadOnly: true, Reason: f.reason, Since: local(9, 5)}
			if !slices.Equal(seen, []engine.Health{want}) {
				t.Errorf("OnHealthChange saw %+v, want once %+v", seen, want)
			}
			h.reject(command.StartCycle{Type: deepWork}, command.ReasonStoreUnavailable)
			if len(seen) != 1 {
				t.Errorf("a refusal while read-only fired OnHealthChange again: %+v", seen)
			}
		})
	}
}

func TestDoErrorContract(t *testing.T) {
	planDay := taskOf(7, "plan-day")
	t.Run("a build rejection", func(t *testing.T) {
		h := newHarness(t, nil)
		h.bootstrap()
		h.reject(command.CompleteTask{TaskID: taskOf(7, "water-plants")}, command.ReasonUnknownTask)
	})
	t.Run("id_conflict", func(t *testing.T) {
		h := newHarness(t, nil)
		h.bootstrap()
		id := clientID()
		h.at(local(9, 5), command.CompleteTask{ID: id, TaskID: planDay})
		r := h.reject(command.SkipTask{ID: id, TaskID: taskOf(7, "post-plan"), Reason: "x"}, command.ReasonIDConflict)
		if !slices.Contains(r.Events, id) || !strings.Contains(r.Message, string(id)) {
			t.Errorf("id_conflict %+v, want it to name the stored event %s", r, id)
		}
	})
	t.Run("a rolled-back append", func(t *testing.T) {
		h := newHarness(t, nil)
		h.bootstrap()
		h.fs.Inject(storefault.Rule{Op: storefault.OpWrite, Name: logName, Partial: 3})
		c := command.CompleteTask{ID: clientID(), TaskID: planDay}
		h.clock.Set(local(9, 5))
		r := h.reject(c, command.ReasonStoreUnavailable)
		if r.Message != unknownOutcome || h.e.Health().ReadOnly {
			t.Errorf("message %q, health %+v, want %q and a writable store", r.Message, h.e.Health(), unknownOutcome)
		}
		if got := h.do(c); !got.Changed || got.Replayed {
			t.Errorf("the retry %+v, want it to commit", got)
		}
	})
	t.Run("read-only", func(t *testing.T) {
		h := newHarness(t, nil)
		h.bootstrap()
		h.injectOnLog(storefault.OpSync)
		_, err := h.try(command.CompleteTask{TaskID: planDay})
		rejectionOf(t, err, command.ReasonStoreUnavailable)
		r := h.reject(command.StartCycle{Type: deepWork}, command.ReasonStoreUnavailable)
		if r.Message != readOnlySentence(store.ReasonAppendSync) {
			t.Errorf("message %q", r.Message)
		}
	})
	t.Run("an adoption failure", func(t *testing.T) {
		h := newHarness(t, nil)
		h.bootstrap()
		h.adopt = func() error { return errors.New("injected adoption failure") }
		id := clientID()
		c := command.CompleteTask{ID: id, TaskID: planDay}
		h.clock.Set(local(9, 5))
		_, err := h.try(c)
		r := rejectionOf(t, err, command.ReasonStoreUnavailable)
		if want := readOnlySentence(store.ReasonAdopt) + ". " + storedReadOnly; r.Message != want {
			t.Errorf("message %q, want %q", r.Message, want)
		}
		if !slices.Equal(h.obs.appendFailed, []string{"project"}) {
			t.Errorf("AppendFailed %v, want [project]", h.obs.appendFailed)
		}
		if got := h.task(planDay).Status; got != projection.Open {
			t.Errorf("the model adopted the change: task %s", got)
		}
		if h.lines() != h.e.Version().LogLines+1 {
			t.Errorf("file %d lines, version %+v: the change is stored and the model is behind", h.lines(), h.e.Version())
		}
		retry := h.do(c)
		if !retry.Replayed || !slices.Equal(retry.EventIDs, []event.ID{id}) {
			t.Errorf("the retry in the same process %+v, want the original result", retry)
		}

		h.adopt = nil
		h.reopen()
		if got := h.task(planDay).Status; got != projection.Completed {
			t.Errorf("after a restart the task is %s, want completed", got)
		}
		if retry := h.do(c); !retry.Replayed || !slices.Equal(retry.EventIDs, []event.ID{id}) {
			t.Errorf("the retry after the restart %+v, want the original result", retry)
		}
	})
}

func TestScriptedDayThroughEngine(t *testing.T) {
	h := newHarness(t, nil)
	var boundaries []int
	commit := func(at time.Time, c command.Command) engine.Result {
		t.Helper()
		r := h.at(at, c)
		if !r.Changed {
			t.Fatalf("%T at %s changed nothing: %s", c, at, r.Note)
		}
		boundaries = append(boundaries, h.e.Version().LogLines)
		return r
	}
	running := func() event.CycleID {
		t.Helper()
		focus, ok := h.e.Snapshot().Model.Running()
		if !ok {
			t.Fatal("no cycle is running")
		}
		return focus.ID
	}

	first := bootstrapCmd(clientID())
	firstResult := commit(local(8, 50), first)
	commit(local(9, 5), command.CompleteTask{ID: clientID(), TaskID: taskOf(7, "plan-day")})
	commit(local(9, 10), command.StartCycle{ID: clientID(), Type: deepWork})
	deep := running()
	commit(local(9, 40), command.StartCycle{ID: clientID(), Type: notifications})
	notif := running()

	st := h.e.State(local(9, 45))
	if st.Focus == nil || st.Focus.Cycle.ID != notif {
		t.Fatalf("at 09:45 the focus is %+v, want notifications", st.Focus)
	}
	if len(st.Dimmed) != 1 || st.Dimmed[0].Cycle.ID != deep {
		t.Fatalf("at 09:45 Dimmed is %+v, want [deep-work]", st.Dimmed)
	}

	commit(local(9, 50), command.SwitchCycle{ID: clientID(), To: deep})
	commit(local(9, 55), command.SwitchCycle{ID: clientID(), To: notif})
	commit(local(9, 58), command.StopCycle{ID: clientID(), CycleID: notif})
	if offer := h.e.State(local(9, 58)).ResumeOffer; offer == nil || offer.Cycle.Cycle.ID != deep {
		t.Fatalf("after notifications stopped the resume offer is %+v, want deep-work", offer)
	}
	commit(local(9, 58), command.ResumeCycle{ID: clientID(), CycleID: deep})
	commit(local(13, 5), command.BackfillBreak{ID: clientID(), CycleID: deep, From: local(12, 0), To: local(13, 0)})
	commit(local(13, 30), command.StopCycle{ID: clientID(), CycleID: deep})

	lines := h.e.Version().LogLines
	again := h.at(local(13, 35), command.StopCycle{ID: clientID(), CycleID: deep})
	if again.Changed || again.Note == "" || h.e.Version().LogLines != lines {
		t.Fatalf("the second stop %+v moved LogLines to %d, want a no-op", again, h.e.Version().LogLines)
	}

	commit(local(13, 40), command.AnnotateCycle{ID: clientID(), CycleID: deep, Note: "Finished the parser", KV: []event.KV{
		{Key: "pr", Value: "https://example.test/pr/1"}, {Key: "pr", Value: "https://example.test/pr/2"},
	}})
	postPlan := taskOf(7, "post-plan")
	skip := commit(local(13, 45), command.SkipTask{ID: clientID(), TaskID: postPlan, Reason: "Posted in the standup instead"})
	correction := commit(local(13, 50), command.Correct{ID: clientID(), Target: skip.EventIDs[0], Fields: map[string]json.RawMessage{
		"reason": json.RawMessage(`"Shared in the standup instead"`),
	}})
	if got := h.task(postPlan).Reason; got != "Shared in the standup instead" {
		t.Errorf("the corrected reason is %q", got)
	}
	commit(local(13, 55), command.Retract{ID: clientID(), Target: correction.EventIDs[0]})
	if got := h.task(postPlan).Reason; got != "Posted in the standup instead" {
		t.Errorf("after the correction is retracted the reason is %q", got)
	}

	summary := taskOf(7, "end-of-day-summary")
	commit(localOn(8, 9, 0), rolloverCmd(clientID(), 8))
	if got := h.task(summary).Status; got != projection.Missed {
		t.Fatalf("after the rollover end-of-day-summary is %s, want missed", got)
	}
	commit(localOn(8, 9, 5), command.CompleteTask{ID: clientID(), TaskID: summary, EffectiveAt: ptr(local(16, 0))})

	now := localOn(8, 9, 10)
	if got := h.cycle(deep).Elapsed(now); got != 187*time.Minute {
		t.Errorf("deep-work elapsed %v, want 187m", got)
	}
	if got := h.cycle(notif).Elapsed(now); got != 13*time.Minute {
		t.Errorf("notifications elapsed %v, want 13m", got)
	}
	task := h.task(summary)
	if task.Status != projection.Completed || task.ResolvedAt == nil || !task.ResolvedAt.Equal(local(16, 0)) {
		t.Errorf("end-of-day-summary %s resolved at %v, want completed at 16:00 the day before", task.Status, task.ResolvedAt)
	}
	if got := h.e.Version().LogLines; got != h.lines() {
		t.Errorf("Version().LogLines %d, file %d lines", got, h.lines())
	}
	if got := h.obs.appendedCount(); got != h.lines() {
		t.Errorf("Observer.Appended was called %d times for %d lines", got, h.lines())
	}
	if !slices.Equal(h.obs.corrected, []string{"correct", "retract"}) || h.obs.rejections() != 0 {
		t.Errorf("Observer.Corrected %v, rejections %v; want a correction and a retraction, no refusal", h.obs.corrected, h.obs.rejected)
	}

	state := h.e.State(now)
	h.reopen()
	if got := h.e.State(now); !reflect.DeepEqual(got, state) {
		t.Errorf("after a restart the state differs:\n got %+v\nwant %+v", got, state)
	}
	retry := h.do(first)
	if !retry.Replayed || retry.BatchID != first.ID || !slices.Equal(retry.EventIDs, firstResult.EventIDs) {
		t.Errorf("a retry of the first request %+v, want its original result %+v", retry, firstResult)
	}
	events := h.fileEvents()
	if boundaries[len(boundaries)-1] != len(events) {
		t.Errorf("the last commit ended at line %d, the file has %d", boundaries[len(boundaries)-1], len(events))
	}
	for _, n := range boundaries {
		if _, err := projection.Replay(events[:n]); err != nil {
			t.Errorf("the prefix of %d lines does not replay: %v", n, err)
		}
	}
}

func TestWriteRollbackFailureMessageIsExact(t *testing.T) {
	for _, f := range readOnlyFaults() {
		if f.stage != "write" {
			continue
		}
		t.Run(string(f.reason), func(t *testing.T) {
			h := newHarness(t, nil)
			h.bootstrap()
			h.clock.Set(local(9, 5))
			f.inject(h)
			_, err := h.try(command.CompleteTask{ID: clientID(), TaskID: taskOf(7, "plan-day")})
			r := rejectionOf(t, err, command.ReasonStoreUnavailable)
			h.requireNoPending()
			if want := readOnlySentence(f.reason) + ". " + unknownReadOnly; r.Message != want {
				t.Errorf("message %q, want %q", r.Message, want)
			}
		})
	}
}
