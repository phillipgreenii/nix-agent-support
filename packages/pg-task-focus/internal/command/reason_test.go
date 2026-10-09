package command_test

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
)

func TestRejectionStatusTable(t *testing.T) {
	// The one error table: every code, its status, and nothing else.
	table := []struct {
		reason command.Reason
		code   string
		status int
	}{
		{command.ReasonInvalidRequest, "invalid_request", 400},
		{command.ReasonInvalidZone, "invalid_zone", 400},
		{command.ReasonUnknownProfile, "unknown_profile", 400},
		{command.ReasonUnknownCycleType, "unknown_cycle_type", 400},
		{command.ReasonReservedKey, "reserved_key", 400},
		{command.ReasonCycleAmbiguous, "cycle_ambiguous", 400},
		{command.ReasonUnknownTask, "unknown_task", 404},
		{command.ReasonUnknownCycle, "unknown_cycle", 404},
		{command.ReasonUnknownEvent, "unknown_event", 404},
		{command.ReasonNoRunningCycle, "no_running_cycle", 409},
		{command.ReasonCycleStopped, "cycle_stopped", 409},
		{command.ReasonAnotherCycleRunning, "another_cycle_running", 409},
		{command.ReasonInterruptedCycleNotRunning, "interrupted_cycle_not_running", 409},
		{command.ReasonCycleActive, "cycle_active", 409},
		{command.ReasonCycleSegmentsOverlap, "cycle_segments_overlap", 409},
		{command.ReasonBreakEndsAtStop, "break_ends_at_stop", 409},
		{command.ReasonClockBehindLog, "clock_behind_log", 409},
		{command.ReasonTaskAlreadyResolved, "task_already_resolved", 409},
		{command.ReasonTaskWithdrawn, "task_withdrawn", 409},
		{command.ReasonTaskNotWithdrawn, "task_not_withdrawn", 409},
		{command.ReasonTaskMaterializedTwice, "task_materialized_twice", 409},
		{command.ReasonTaskWithoutPeriod, "task_without_period", 409},
		{command.ReasonTaskBeforeProfile, "task_before_profile", 409},
		{command.ReasonPeriodUnchanged, "period_unchanged", 409},
		{command.ReasonIDConflict, "id_conflict", 409},
		{command.ReasonStalePreview, "stale_preview", 409},
		{command.ReasonBatchHasDependents, "batch_has_dependents", 409},
		{command.ReasonCycleHasDependents, "cycle_has_dependents", 409},
		{command.ReasonInvalidCorrection, "invalid_correction", 422},
		{command.ReasonFutureEffectiveAt, "future_effective_at", 422},
		{command.ReasonStopNotAfterStart, "stop_not_after_start", 422},
		{command.ReasonEmptyRunningSegment, "empty_running_segment", 422},
		{command.ReasonCycleEventBeforeStart, "cycle_event_before_start", 422},
		{command.ReasonResolutionBeforeMaterialization, "resolution_before_materialization", 422},
		{command.ReasonPeriodOutOfOrder, "period_out_of_order", 422},
		{command.ReasonStoreUnavailable, "store_unavailable", 503},
		{command.ReasonNotReady, "not_ready", 503},
	}
	var want []command.Reason
	for _, row := range table {
		if string(row.reason) != row.code {
			t.Errorf("constant for %s = %q", row.code, row.reason)
		}
		if got := row.reason.Status(); got != row.status {
			t.Errorf("%s.Status() = %d, want %d", row.code, got, row.status)
		}
		want = append(want, row.reason)
	}
	got := command.Reasons()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("Reasons() = %v, want exactly the table's %v", got, want)
	}
	if s := command.Reason("invalid_timeline").Status(); s != 0 {
		t.Errorf("a code outside the set has status %d, want 0", s)
	}
}

func TestEveryReplayCodeIsAReason(t *testing.T) {
	reasons := command.Reasons()
	for _, code := range projection.Codes() {
		r := command.Reason(code)
		if !slices.Contains(reasons, r) {
			t.Errorf("replay code %q is not a Reason", code)
		}
		if r.Status() == 0 {
			t.Errorf("replay code %q has no status", code)
		}
	}
}

func TestRejectionIsAnError(t *testing.T) {
	var err error = &command.Rejection{Reason: command.ReasonUnknownTask, Message: "No task x exists."}
	if err.Error() != "No task x exists." {
		t.Errorf("Error() = %q, want the message", err.Error())
	}
	var r *command.Rejection
	if !errors.As(err, &r) {
		t.Error("errors.As cannot reach the rejection")
	}
}

// byHand is the event a command would add, built without the command layer.
func byHand(p event.Payload, effective, now time.Time) event.Event {
	return event.Event{
		Envelope: event.Envelope{V: event.SchemaVersion, ID: idOf('H', 1), At: event.At(now), EffectiveAt: event.At(effective), Type: p.EventType()},
		Payload:  p,
	}
}

func TestPresentStateAndReplayAgreeOnCodes(t *testing.T) {
	// For each condition, the command's rejection and the finding of a
	// candidate replay of the same events built by hand agree on the code,
	// the entity, the stored events, the cycles and the instants.
	type agreement struct {
		name  string
		build func(t *testing.T) *logb
		now   time.Time
		cmd   command.Command
		event event.Payload // what the command adds
		at    time.Time     // its effective_at
		want  command.Reason
	}
	completedAt10 := func(t *testing.T) *logb {
		b := bootstrapped(t)
		b.add(10, event.TaskCompleted{TaskID: postPlan})
		return b
	}
	withdrawnAt10 := func(t *testing.T) *logb {
		b := bootstrapped(t)
		b.batch(
			10,
			func(bt event.ID) event.Payload { return event.ProfileChanged{Profile: "on-call", Batch: bt} },
			func(bt event.ID) event.Payload { return event.TaskWithdrawn{TaskID: postPlan, Batch: bt} },
		)
		return b
	}
	cases := []agreement{
		{
			name: "a double completion", build: completedAt10, now: at(20),
			cmd:   command.CompleteTask{TaskID: postPlan},
			event: event.TaskCompleted{TaskID: postPlan}, at: at(20),
			want: command.ReasonTaskAlreadyResolved,
		},
		{
			name: "a backdated double completion lists the instants in event order", build: completedAt10, now: at(20),
			cmd:   command.CompleteTask{TaskID: postPlan, EffectiveAt: ptr(at(5))},
			event: event.TaskCompleted{TaskID: postPlan}, at: at(5),
			want: command.ReasonTaskAlreadyResolved,
		},
		{
			name: "a double resolution at the same instant", build: completedAt10, now: at(20),
			cmd:   command.CompleteTask{TaskID: postPlan, EffectiveAt: ptr(at(10))},
			event: event.TaskCompleted{TaskID: postPlan}, at: at(10),
			want: command.ReasonTaskAlreadyResolved,
		},
		{
			name: "a skip of a completed task", build: completedAt10, now: at(20),
			cmd:   command.SkipTask{TaskID: postPlan, Reason: "late"},
			event: event.TaskSkipped{TaskID: postPlan, Reason: "late"}, at: at(20),
			want: command.ReasonTaskAlreadyResolved,
		},
		{
			name: "a completion of a withdrawn task", build: withdrawnAt10, now: at(20),
			cmd:   command.CompleteTask{TaskID: postPlan},
			event: event.TaskCompleted{TaskID: postPlan}, at: at(20),
			want: command.ReasonTaskWithdrawn,
		},
		{
			name: "a completion at the instant of the withdrawal", build: withdrawnAt10, now: at(20),
			cmd:   command.CompleteTask{TaskID: postPlan, EffectiveAt: ptr(at(10))},
			event: event.TaskCompleted{TaskID: postPlan}, at: at(10),
			want: command.ReasonTaskWithdrawn,
		},
		{
			name: "a pause of a stopped cycle",
			build: func(t *testing.T) *logb {
				b := bootstrapped(t)
				b.add(0, startOf(cycleA, deepWork))
				b.add(10, stopOf(cycleA))
				return b
			},
			now: at(20), cmd: command.PauseCycle{CycleID: cycleA},
			event: pauseOf(cycleA), at: at(20),
			want: command.ReasonCycleStopped,
		},
		{
			name: "a resume while another cycle runs",
			build: func(t *testing.T) *logb {
				b := bootstrapped(t)
				b.add(0, startOf(cycleA, deepWork))
				b.add(10, pauseOf(cycleA))
				b.add(15, startOf(cycleB, review))
				return b
			},
			now: at(20), cmd: command.ResumeCycle{CycleID: cycleA},
			event: resumeOf(cycleA), at: at(20),
			want: command.ReasonAnotherCycleRunning,
		},
		{
			// bootstrapped's events are L1 (the batch id) to L6; the skip is L7.
			name: "a correction of an identity field",
			build: func(t *testing.T) *logb {
				b := bootstrapped(t)
				b.add(10, event.TaskSkipped{TaskID: postPlan, Reason: "late"})
				return b
			},
			now:   at(20),
			cmd:   command.Correct{Target: idOf('L', 7), Fields: map[string]json.RawMessage{"task_id": json.RawMessage(`"x"`)}},
			event: event.EventCorrected{Target: idOf('L', 7), Fields: map[string]json.RawMessage{"task_id": json.RawMessage(`"x"`)}}, at: at(20),
			want: command.ReasonInvalidCorrection,
		},
		{
			name: "a retraction of a batch member alone", build: bootstrapped, now: at(20),
			cmd:   command.Retract{Target: idOf('L', 3)},
			event: event.EventRetracted{Target: idOf('L', 3)}, at: at(20),
			want: command.ReasonInvalidCorrection,
		},
		{
			name: "a retraction of an unknown event", build: bootstrapped, now: at(20),
			cmd:   command.Retract{Target: idOf('G', 1)},
			event: event.EventRetracted{Target: idOf('G', 1)}, at: at(20),
			want: command.ReasonUnknownEvent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.build(t)
			env := envOf(t, b, tc.now)
			r := mustReject(t, env, tc.cmd, tc.want)

			_, err := projection.Candidate(env.Model.Log(), []event.Event{byHand(tc.event, tc.at, tc.now)})
			var inv *projection.Invalid
			if !errors.As(err, &inv) {
				t.Fatalf("Candidate: %v, want an *Invalid", err)
			}
			if string(inv.Code) != string(r.Reason) {
				t.Errorf("code: replay %q, command %q", inv.Code, r.Reason)
			}
			if inv.Entity != r.Entity {
				t.Errorf("entity: replay %q, command %q", inv.Entity, r.Entity)
			}
			if !slices.Equal(inv.Events, r.Events) {
				t.Errorf("events: replay %v, command %v", inv.Events, r.Events)
			}
			if !slices.Equal(inv.Cycles, cycleIDsOf(r)) {
				t.Errorf("cycles: replay %v, command %v", inv.Cycles, cycleIDsOf(r))
			}
			if !slices.EqualFunc(inv.Instants, r.Instants, time.Time.Equal) {
				t.Errorf("instants: replay %v, command %v", inv.Instants, r.Instants)
			}
			if !slices.ContainsFunc(r.Instants, tc.at.Equal) {
				t.Errorf("instants %v do not include the new event's effective_at %v", r.Instants, tc.at)
			}
			for _, c := range r.Cycles {
				if title, _ := env.Model.CycleTitle(c.ID); c.Title != title || title == "" {
					t.Errorf("cycle %s title = %q, want the model's %q", c.ID, c.Title, title)
				}
			}
		})
	}
}
