package command_test

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/testutil"
)

// taskStatus is the status of a task in a model.
func taskStatus(t *testing.T, m *projection.Model, id event.TaskID) projection.TaskStatus {
	t.Helper()
	task, ok := m.Task(id)
	if !ok {
		t.Fatalf("task %s not in the model", id)
	}
	return task.Status
}

func TestCompleteTaskDefaults(t *testing.T) {
	now := at(10).Add(1234567 * time.Nanosecond) // finer than a millisecond
	stamped := event.At(now).Time()

	t.Run("effective now, a fresh id, no req_hash", func(t *testing.T) {
		env := envOf(t, bootstrapped(t), now)
		p := mustPlan(t, env, command.CompleteTask{TaskID: postPlan})
		e := only(t, p)
		if e.Payload.EventType() != event.TypeTaskCompleted || e.Type != event.TypeTaskCompleted {
			t.Errorf("type = %s / %s, want task.completed", e.Type, e.Payload.EventType())
		}
		if got := e.Payload.(event.TaskCompleted).TaskID; got != postPlan {
			t.Errorf("task_id = %s, want %s", got, postPlan)
		}
		if !e.At.Time().Equal(stamped) || !e.EffectiveAt.Time().Equal(stamped) {
			t.Errorf("at %v, effective_at %v, want both %v", e.At.Time(), e.EffectiveAt.Time(), stamped)
		}
		if e.ReqHash != "" {
			t.Errorf("req_hash = %q without an id", e.ReqHash)
		}
		if e.ID != idOf('N', 1) {
			t.Errorf("id = %s, want the first fresh id %s", e.ID, idOf('N', 1))
		}
		if len(e.Data) == 0 {
			t.Error("the event has no data: it is not as the codec reads it")
		}
		if p.NoOp || p.BatchID != "" || p.Candidate == nil {
			t.Errorf("NoOp %v, BatchID %q, Candidate %v", p.NoOp, p.BatchID, p.Candidate)
		}
		if got := taskStatus(t, p.Candidate, postPlan); got != projection.Completed {
			t.Errorf("candidate task status = %s, want completed", got)
		}
		if got := taskStatus(t, env.Model, postPlan); got != projection.Open {
			t.Errorf("Build changed the model it was given: status %s", got)
		}
		if p.Candidate.Lines() != env.Model.Lines()+1 {
			t.Errorf("candidate has %d lines, want %d", p.Candidate.Lines(), env.Model.Lines()+1)
		}
	})

	t.Run("the client id is the event id and req_hash is set", func(t *testing.T) {
		env := envOf(t, bootstrapped(t), now)
		c := command.CompleteTask{ID: clientID, TaskID: postPlan}
		e := only(t, mustPlan(t, env, c))
		if e.ID != clientID {
			t.Errorf("id = %s, want %s", e.ID, clientID)
		}
		if want := hashOf(t, c); e.ReqHash != want {
			t.Errorf("req_hash = %q, want %q", e.ReqHash, want)
		}
	})

	t.Run("a supplied effective_at is kept, truncated to the millisecond", func(t *testing.T) {
		env := envOf(t, bootstrapped(t), now)
		eff := at(5).Add(999 * time.Microsecond)
		e := only(t, mustPlan(t, env, command.CompleteTask{TaskID: postPlan, EffectiveAt: &eff}))
		if !e.EffectiveAt.Time().Equal(at(5)) || !e.At.Time().Equal(stamped) {
			t.Errorf("at %v, effective_at %v, want %v and %v", e.At.Time(), e.EffectiveAt.Time(), stamped, at(5))
		}
	})
}

func TestCompleteResolvedTaskIsTaskAlreadyResolved(t *testing.T) {
	tests := []struct {
		name     string
		resolve  event.Payload
		cmd      command.Command
		resolved string
	}{
		{"a completion of a completed task", event.TaskCompleted{TaskID: postPlan}, command.CompleteTask{TaskID: postPlan}, "completed"},
		{"a skip of a completed task", event.TaskCompleted{TaskID: postPlan}, command.SkipTask{TaskID: postPlan, Reason: "late"}, "completed"},
		{"a completion of a skipped task", event.TaskSkipped{TaskID: postPlan, Reason: "away"}, command.CompleteTask{TaskID: postPlan}, "skipped"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := bootstrapped(t)
			stored := b.add(10, tc.resolve)
			r := mustReject(t, envOf(t, b, at(20)), tc.cmd, command.ReasonTaskAlreadyResolved)
			if r.Reason.Status() != 409 || r.Entity != string(postPlan) {
				t.Errorf("status %d, entity %q", r.Reason.Status(), r.Entity)
			}
			if !sameIDs(r.Events, stored) {
				t.Errorf("Events = %v, want the stored resolution %s alone", r.Events, stored)
			}
			for _, want := range []string{string(stored), "the new event", tc.resolved, string(postPlan), "2026-10-07T12:10:00.000Z (08:10 America/New_York)"} {
				if !strings.Contains(r.Message, want) {
					t.Errorf("Message %q does not mention %q", r.Message, want)
				}
			}
		})
	}
}

func TestSkipRequiresNonBlankReason(t *testing.T) {
	for _, reason := range []string{"", "  ", "\t\n", " ", "　", "  　\t"} {
		mustReject(t, envOf(t, bootstrapped(t), at(10)), command.SkipTask{TaskID: postPlan, Reason: reason}, command.ReasonInvalidRequest)
	}
	e := only(t, mustPlan(t, envOf(t, bootstrapped(t), at(10)), command.SkipTask{TaskID: postPlan, Reason: " late "}))
	if got := e.Payload.(event.TaskSkipped).Reason; got != "late" {
		t.Errorf("reason = %q, want %q", got, "late")
	}
}

func TestCompleteMissedTaskAllowed(t *testing.T) {
	b := bootstrapped(t)
	b.batch(
		1440,
		func(bt event.ID) event.Payload {
			return event.PeriodChanged{Kind: "day", Start: day2, TZ: newYork, Batch: bt}
		},
		func(bt event.ID) event.Payload { return event.TaskMissed{TaskID: postPlan, Batch: bt} },
	)
	for name, eff := range map[string]*time.Time{"now": nil, "late on the day it was due": testutil.Ptr(at(600))} {
		t.Run(name, func(t *testing.T) {
			env := envOf(t, b, at(1500))
			if got := taskStatus(t, env.Model, postPlan); got != projection.Missed {
				t.Fatalf("setup: status %s, want missed", got)
			}
			p := mustPlan(t, env, command.CompleteTask{TaskID: postPlan, EffectiveAt: eff})
			if got := taskStatus(t, p.Candidate, postPlan); got != projection.Completed {
				t.Errorf("status = %s, want completed", got)
			}
		})
	}
}

func TestCompleteWithdrawnTask(t *testing.T) {
	withdraw := func(b *logb, n int) event.ID {
		b.batch(
			n,
			func(bt event.ID) event.Payload { return event.ProfileChanged{Profile: "on-call", Batch: bt} },
			func(bt event.ID) event.Payload { return event.TaskWithdrawn{TaskID: postPlan, Batch: bt} },
		)
		return b.events[len(b.events)-2].ID // the withdrawal, before the commit marker
	}
	reinstate := func(b *logb, n int) {
		b.batch(
			n,
			func(bt event.ID) event.Payload { return event.ProfileChanged{Profile: "normal", Batch: bt} },
			func(bt event.ID) event.Payload { return event.TaskReinstated{TaskID: postPlan, Batch: bt} },
		)
	}

	t.Run("a completion after the withdrawal is task_withdrawn", func(t *testing.T) {
		b := bootstrapped(t)
		w := withdraw(b, 10)
		for _, cmd := range []command.Command{command.CompleteTask{TaskID: postPlan}, command.SkipTask{TaskID: postPlan, Reason: "late"}} {
			r := mustReject(t, envOf(t, b, at(20)), cmd, command.ReasonTaskWithdrawn)
			if r.Reason.Status() != 409 || !sameIDs(r.Events, w) || r.Entity != string(postPlan) {
				t.Errorf("status %d, events %v, entity %q; want 409, [%s], %s", r.Reason.Status(), r.Events, r.Entity, w, postPlan)
			}
			if !strings.Contains(r.Message, string(w)) || !strings.Contains(r.Message, "the new event") {
				t.Errorf("Message %q does not name the withdrawal and the new event", r.Message)
			}
		}
	})

	t.Run("a completion effective before the withdrawal is accepted", func(t *testing.T) {
		b := bootstrapped(t)
		withdraw(b, 10)
		p := mustPlan(t, envOf(t, b, at(20)), command.CompleteTask{TaskID: postPlan, EffectiveAt: testutil.Ptr(at(5))})
		if got := taskStatus(t, p.Candidate, postPlan); got != projection.Completed {
			t.Errorf("status = %s, want completed", got)
		}
	})

	t.Run("a reinstated task can be completed", func(t *testing.T) {
		b := bootstrapped(t)
		withdraw(b, 10)
		reinstate(b, 15)
		mustPlan(t, envOf(t, b, at(20)), command.CompleteTask{TaskID: postPlan})
	})

	t.Run("a completion inside an earlier withdrawal is found by replay", func(t *testing.T) {
		b := bootstrapped(t)
		first := withdraw(b, 10)
		reinstate(b, 15)
		withdraw(b, 25)
		r := mustReject(t, envOf(t, b, at(30)), command.CompleteTask{TaskID: postPlan, EffectiveAt: testutil.Ptr(at(12))}, command.ReasonTaskWithdrawn)
		if !sameIDs(r.Events, first) {
			t.Errorf("Events = %v, want the first withdrawal %s", r.Events, first)
		}
	})
}

func TestCompleteBeforeMaterialization(t *testing.T) {
	r := mustReject(t, envOf(t, bootstrapped(t), at(10)), command.CompleteTask{TaskID: postPlan, EffectiveAt: testutil.Ptr(at(-90))}, command.ReasonResolutionBeforeMaterialization)
	if r.Reason.Status() != 422 {
		t.Errorf("status %d, want 422", r.Reason.Status())
	}
}

func TestUnknownTask(t *testing.T) {
	const ghost = event.TaskID("day:2026-10-07:nothing")
	for _, cmd := range []command.Command{command.CompleteTask{TaskID: ghost}, command.SkipTask{TaskID: ghost, Reason: "late"}} {
		r := mustReject(t, envOf(t, bootstrapped(t), at(10)), cmd, command.ReasonUnknownTask)
		if r.Reason.Status() != 404 || !strings.Contains(r.Message, string(ghost)) {
			t.Errorf("status %d, message %q", r.Reason.Status(), r.Message)
		}
	}
	mustReject(t, envOf(t, bootstrapped(t), at(10)), command.CompleteTask{}, command.ReasonInvalidRequest)
}

func TestTaskCommandWithClockBehindTheTaskIsClockBehindLog(t *testing.T) {
	b := bootstrapped(t)
	r := mustReject(t, envOf(t, b, at(-70)), command.CompleteTask{TaskID: postPlan}, command.ReasonClockBehindLog)
	for _, want := range []string{"2026-10-07T10:50:00.000Z", "2026-10-07T11:00:00.000Z", "effective_at"} {
		if !strings.Contains(r.Message, want) {
			t.Errorf("Message %q does not mention %q", r.Message, want)
		}
	}
	mustReject(t, envOf(t, b, at(-70)), command.CompleteTask{TaskID: postPlan, EffectiveAt: testutil.Ptr(at(-70))}, command.ReasonResolutionBeforeMaterialization)
}
