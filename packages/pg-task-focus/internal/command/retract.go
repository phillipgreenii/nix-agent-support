package command

import (
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
)

// Retract takes back, effective now, one stored event (Target) or one whole
// batch (TargetBatch); exactly one is named. It appends one event.retracted,
// which carries no batch, so it can itself be retracted alone, and retracting
// it restores what it took back. Any event but a batch.committed can be the
// Target, a correction or a retraction included, except that an event that is
// a member of a batch, and every task.materialized and period.changed, is
// taken back only through its batch (invalid_correction). A target or batch
// that is already retracted is a no-op. A batch is not retracted while a later
// live event references a task it created (batch_has_dependents), and a
// cycle.started is not retracted while later live events need its cycle
// (cycle_has_dependents); each refusal lists those events. Reason, optional,
// says why.
type Retract struct {
	ID          event.ID
	Target      event.ID
	TargetBatch event.ID
	Reason      string
}

// Name implements Command.
func (Retract) Name() string { return "events/retract" }

// ClientID implements Command.
func (c Retract) ClientID() event.ID { return c.ID }

// IsDryRun implements Command: a retraction has no dry run.
func (Retract) IsDryRun() bool { return false }

// ReqHash implements Command.
func (c Retract) ReqHash() (string, error) {
	return event.ReqHash(c.Name(), struct {
		Target      event.ID `json:"target,omitempty"`
		TargetBatch event.ID `json:"target_batch,omitempty"`
		Reason      string   `json:"reason,omitempty"`
	}{c.Target, c.TargetBatch, c.Reason})
}

// plan judges a retraction in this order: (1) the request's own validity
// (invalid_request), exactly one of target and target_batch among it; (2) the
// target or batch (unknown_event); (3) the retraction rules
// (invalid_correction), so a member of a batch retracted alone is refused even
// when its batch is already retracted; (4) a target or batch already
// retracted, which is a no-op; (5) the dependents of a cycle.started or a
// batch (cycle_has_dependents, batch_has_dependents), then the candidate replay
// of the log with the retraction, whose finding keeps its own code.
//
// The new event takes effect when it is recorded, so every refusal, a
// malformed request's included, names that instant.
func (c Retract) plan(b *builder) (Plan, error) {
	switch {
	case c.Target == "" && c.TargetBatch == "":
		return Plan{}, stamped(b.invalid("A retraction names exactly one of target, an event id, and target_batch, a batch id, and it names neither."), b.at)
	case c.Target != "" && c.TargetBatch != "":
		return Plan{}, stamped(b.invalid("A retraction names exactly one of target, an event id, and target_batch, a batch id, and it names both."), b.at)
	}
	if err := b.validText(string(c.Target), string(c.TargetBatch), c.Reason); err != nil {
		return Plan{}, stamped(err, b.at)
	}
	p := event.EventRetracted{Target: c.Target, TargetBatch: c.TargetBatch, Reason: c.Reason}
	if err := b.encodable(b.at, p); err != nil {
		return Plan{}, stamped(err, b.at)
	}
	if c.TargetBatch != "" {
		return b.retractBatch(c.TargetBatch, p)
	}
	return b.retractEvent(c.Target, p)
}

// retractBatch plans steps 2 to 5 of the retraction p of batch id.
func (b *builder) retractBatch(id event.ID, p event.EventRetracted) (Plan, error) {
	members := b.env.Model.BatchEvents(id)
	if len(members) == 0 {
		msg := fmt.Sprintf("No batch %s exists in the log, so there is nothing to retract.", id)
		if _, ok := b.logged(id); ok {
			msg = fmt.Sprintf("No batch %s exists in the log: it is an event, which is retracted by naming it as target.", id)
		}
		return Plan{}, &Rejection{Reason: ReasonUnknownEvent, Instants: []time.Time{b.at}, Message: msg}
	}
	// Every member is retracted with its batch, and never alone.
	if done, ok := b.alreadyRetracted(members[0], "Batch "+string(id)); ok {
		return done, nil
	}
	if deps := b.env.Model.Dependents(id); len(deps) > 0 {
		return Plan{}, &Rejection{
			Reason: ReasonBatchHasDependents, Events: deps, Instants: []time.Time{b.at},
			Message: fmt.Sprintf(
				"Batch %s cannot be retracted while later live events reference the tasks it created: %s; retract those first, then the batch.",
				id, b.described(deps),
			),
		}
	}
	return b.finish([]event.Event{b.lone(b.at, p)}, "")
}

// retractEvent plans steps 2 to 5 of the retraction p of event id.
func (b *builder) retractEvent(id event.ID, p event.EventRetracted) (Plan, error) {
	target, ok := b.logged(id)
	if !ok {
		return Plan{}, b.unknownEvent(id, "retract")
	}
	refuse := func(why string) error {
		return &Rejection{
			Reason: ReasonInvalidCorrection, Entity: projection.EntityOf(target.Payload),
			Events: []event.ID{id}, Instants: []time.Time{b.at},
			Message: fmt.Sprintf("The new event retracts %s, %s", projection.Describe(target), why),
		}
	}
	typ, batch := target.Payload.EventType(), target.Payload.BatchID()
	switch {
	case typ == event.TypeBatchCommitted:
		return Plan{}, refuse(fmt.Sprintf("the marker that commits batch %s, and a retraction never targets a batch marker; to take the batch back, name %s as target_batch.", batch, batch))
	case typ == event.TypeTaskMaterialized || typ == event.TypePeriodChanged:
		// The schema requires a batch of both, so a stored one always has one.
		return Plan{}, refuse(fmt.Sprintf("and a %s is never retracted alone, which would leave a period and its tasks out of step; it is retracted only through its batch, by naming %s as target_batch.", typ, batch))
	case batch != "":
		return Plan{}, refuse(fmt.Sprintf("which is a member of batch %s and can be retracted only through its batch, by naming %s as target_batch.", batch, batch))
	}
	if done, ok := b.alreadyRetracted(id, "Event "+string(id)); ok {
		return done, nil
	}
	if start, ok := target.Payload.(event.CycleStarted); ok {
		if deps := b.env.Model.CycleDependents(id); len(deps) > 0 {
			// The start takes effect at its live instant, as corrected.
			startedAt := target.EffectiveAt.Time()
			if v, ok := b.env.Model.Event(id); ok {
				startedAt = v.Corrected.EffectiveAt.Time()
			}
			return Plan{}, &Rejection{
				Reason: ReasonCycleHasDependents, Entity: string(start.CycleID), Events: deps,
				Instants: instantsInOrder(startedAt, b.at),
				Message: fmt.Sprintf(
					"Event %s, the start of cycle %s, cannot be retracted while later live events need the cycle: %s; retract those first, then the start.",
					id, start.CycleID, b.described(deps),
				),
			}
		}
	}
	return b.finish([]event.Event{b.lone(b.at, p)}, "")
}

// alreadyRetracted is the no-op of a retraction whose target, the event id or
// the member of a batch, a live retraction already takes back; what names the
// target at the start of the note.
func (b *builder) alreadyRetracted(id event.ID, what string) (Plan, bool) {
	v, ok := b.env.Model.Event(id)
	if !ok || !v.Retracted {
		return Plan{}, false
	}
	return Plan{NoOp: true, Note: fmt.Sprintf(
		"%s is already retracted by event %s, so there is nothing to retract; retract event %s to restore it.",
		what, v.RetractedBy, v.RetractedBy,
	)}, true
}

// described lists stored events by id, type and entity, in a sentence.
func (b *builder) described(ids []event.ID) string {
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = "event " + string(id)
		if v, ok := b.env.Model.Event(id); ok {
			names[i] = projection.Describe(v.Original)
		}
	}
	return list(names)
}
