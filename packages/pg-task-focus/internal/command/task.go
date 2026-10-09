package command

import (
	"errors"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
)

// instantOf is a supplied instant as the request hash takes it.
func instantOf(t *time.Time) *event.Instant {
	if t == nil {
		return nil
	}
	i := event.At(*t)
	return &i
}

// CompleteTask completes a task, effective now unless EffectiveAt says when.
type CompleteTask struct {
	ID          event.ID
	TaskID      event.TaskID
	EffectiveAt *time.Time
}

// Name implements Command.
func (CompleteTask) Name() string { return "tasks/complete" }

// ClientID implements Command.
func (c CompleteTask) ClientID() event.ID { return c.ID }

// IsDryRun implements Command: a completion has no dry run.
func (CompleteTask) IsDryRun() bool { return false }

// ReqHash implements Command.
func (c CompleteTask) ReqHash() (string, error) {
	return event.ReqHash(c.Name(), struct {
		TaskID      event.TaskID   `json:"task_id"`
		EffectiveAt *event.Instant `json:"effective_at,omitempty"`
	}{c.TaskID, instantOf(c.EffectiveAt)})
}

func (c CompleteTask) plan(b *builder) (Plan, error) {
	return b.resolveTask(c.TaskID, c.EffectiveAt, "complete", event.TaskCompleted{TaskID: c.TaskID})
}

// SkipTask skips a task for a reason, which MUST NOT be blank and is stored
// without its surrounding whitespace.
type SkipTask struct {
	ID          event.ID
	TaskID      event.TaskID
	Reason      string
	EffectiveAt *time.Time
}

// Name implements Command.
func (SkipTask) Name() string { return "tasks/skip" }

// ClientID implements Command.
func (c SkipTask) ClientID() event.ID { return c.ID }

// IsDryRun implements Command: a skip has no dry run.
func (SkipTask) IsDryRun() bool { return false }

// ReqHash implements Command.
func (c SkipTask) ReqHash() (string, error) {
	return event.ReqHash(c.Name(), struct {
		TaskID      event.TaskID   `json:"task_id"`
		Reason      string         `json:"reason"`
		EffectiveAt *event.Instant `json:"effective_at,omitempty"`
	}{c.TaskID, c.Reason, instantOf(c.EffectiveAt)})
}

func (c SkipTask) plan(b *builder) (Plan, error) {
	reason, err := event.ValidReason(c.Reason)
	switch {
	case errors.Is(err, event.ErrInvalidUTF8), errors.Is(err, event.ErrTooLarge):
		return Plan{}, b.badText(err)
	case err != nil:
		return Plan{}, b.invalid("A skip needs a reason that is not blank: empty or only white space is refused.")
	}
	return b.resolveTask(c.TaskID, c.EffectiveAt, "skip", event.TaskSkipped{TaskID: c.TaskID, Reason: reason})
}

// resolveTask plans the completion or skip p of task id. Completing or
// skipping a missed task is allowed: a missed marker is not a resolution.
func (b *builder) resolveTask(id event.TaskID, supplied *time.Time, action string, p event.Payload) (Plan, error) {
	if id == "" {
		return Plan{}, b.invalid("A task_id is required to %s a task.", action)
	}
	if err := b.validText(string(id)); err != nil {
		return Plan{}, err
	}
	eff := b.effective(supplied)
	if err := b.encodable(eff, p); err != nil {
		return Plan{}, err
	}
	if err := b.notFuture(eff, supplied); err != nil {
		return Plan{}, err
	}
	task, ok := b.env.Model.Task(id)
	if !ok {
		return Plan{}, &Rejection{
			Reason: ReasonUnknownTask, Instants: []time.Time{eff},
			Message: fmt.Sprintf("No task %s exists in the log, so there is nothing to %s.", id, action),
		}
	}
	if err := b.resolvable(task, eff, action); err != nil {
		return Plan{}, err
	}
	if err := b.clockBehind(supplied, eff, "task", string(id)); err != nil {
		return Plan{}, err
	}
	return b.finish([]event.Event{b.lone(eff, p)}, "")
}

// taskEvent reports whether p, of one of the given types, is about task id.
func taskEvent(id event.TaskID) func(event.Payload) bool {
	return func(p event.Payload) bool {
		switch p := p.(type) {
		case event.TaskCompleted:
			return p.TaskID == id
		case event.TaskSkipped:
			return p.TaskID == id
		case event.TaskWithdrawn:
			return p.TaskID == id
		case event.TaskReinstated:
			return p.TaskID == id
		}
		return false
	}
}

// resolvable refuses, from the present state, a resolution of a task that is
// already resolved, and of a withdrawn task unless eff precedes the
// withdrawal that leaves it withdrawn. The facts are those the candidate
// replay would give for the same events: the stored event, and the instants
// in event order.
func (b *builder) resolvable(task projection.Task, eff time.Time, action string) error {
	switch task.Status {
	case projection.Completed, projection.Skipped:
		resolutions := b.liveEvents(taskEvent(task.ID), event.TypeTaskCompleted, event.TypeTaskSkipped)
		if len(resolutions) == 0 {
			return nil // the replay finds it
		}
		z := resolutions[0]
		return &Rejection{
			Reason: ReasonTaskAlreadyResolved, Entity: string(task.ID),
			Events:   []event.ID{z.ID},
			Instants: instantsInOrder(z.EffectiveAt.Time(), eff),
			Message: fmt.Sprintf(
				"Task %s is already %s by event %s effective at %s, so the new event effective at %s cannot %s it; a task is completed or skipped only once.",
				task.ID, task.Status, z.ID, b.instant(z.EffectiveAt.Time()), b.instant(eff), action,
			),
		}
	case projection.Withdrawn:
		var by *event.Event
		chain := b.liveEvents(taskEvent(task.ID), event.TypeTaskWithdrawn, event.TypeTaskReinstated)
		for i := range chain {
			switch chain[i].Payload.(type) {
			case event.TaskWithdrawn:
				if by == nil {
					by = &chain[i]
				}
			case event.TaskReinstated:
				by = nil
			}
		}
		if by == nil || eff.Before(by.EffectiveAt.Time()) {
			return nil // an earlier withdrawal, if any, is the replay's to find
		}
		return &Rejection{
			Reason: ReasonTaskWithdrawn, Entity: string(task.ID),
			Events:   []event.ID{by.ID},
			Instants: instantsInOrder(by.EffectiveAt.Time(), eff),
			Message: fmt.Sprintf(
				"Task %s was withdrawn by event %s effective at %s, with no reinstatement since, so the new event effective at %s cannot %s it; reinstate the task first, or give an effective_at before the withdrawal.",
				task.ID, by.ID, b.instant(by.EffectiveAt.Time()), b.instant(eff), action,
			),
		}
	}
	return nil
}
