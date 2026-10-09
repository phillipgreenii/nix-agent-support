package command

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// BackfillBreak adds a break the operator forgot to pause for: one batch,
// whose id is the request id, that pauses cycle CycleID at From, resumes it at
// To and commits. From MUST be before To, and neither may be in the future.
// The whole break lies strictly before the cycle's stop when it has one: a
// break that begins or ends at or after the stop is break_ends_at_stop, whose
// message points at "end at" (a stop with an effective_at), and a break that
// overlaps a pause the cycle already has is cycle_segments_overlap. The
// candidate replay finds both, as it finds every impossible timeline, so the
// code is the one a stored log with the same events would get.
type BackfillBreak struct {
	ID       event.ID
	CycleID  event.CycleID
	From, To time.Time
}

// Name implements Command.
func (BackfillBreak) Name() string { return "cycles/break" }

// ClientID implements Command: the request id is the batch id.
func (c BackfillBreak) ClientID() event.ID { return c.ID }

// IsDryRun implements Command: a break has no dry run.
func (BackfillBreak) IsDryRun() bool { return false }

// ReqHash implements Command. The instants enter it as they are stored, to
// the millisecond.
func (c BackfillBreak) ReqHash() (string, error) {
	return event.ReqHash(c.Name(), struct {
		CycleID event.CycleID `json:"cycle_id"`
		From    event.Instant `json:"from"`
		To      event.Instant `json:"to"`
	}{c.CycleID, event.At(c.From), event.At(c.To)})
}

// plan judges a break in this order: (1) the request's own validity
// (invalid_request), then future_effective_at for its end, which is after its
// start; (2) the cycle (unknown_cycle); (3) the candidate replay of the batch.
// The batch.committed takes effect when it is recorded.
//
// A malformed request names the instants of the break it gives, the
// effective instants of its pause and resume.
func (c BackfillBreak) plan(b *builder) (Plan, error) {
	if err := c.validate(b); err != nil {
		var given []time.Time
		for _, t := range []time.Time{c.From, c.To} {
			if !t.IsZero() {
				given = append(given, event.At(t).Time())
			}
		}
		return Plan{}, stamped(err, given...)
	}
	from, to := event.At(c.From).Time(), event.At(c.To).Time()
	pause := func(batch event.ID) event.Payload { return event.CyclePaused{CycleID: c.CycleID, Batch: batch} }
	resume := func(batch event.ID) event.Payload { return event.CycleResumed{CycleID: c.CycleID, Batch: batch} }
	if err := b.encodable(to, pause(placeholderID), resume(placeholderID), event.BatchCommitted{Batch: placeholderID}); err != nil {
		return Plan{}, stamped(err, from, to)
	}
	if err := b.notFuture(to, &to); err != nil {
		return Plan{}, err
	}
	if _, err := b.named(c.CycleID, to); err != nil {
		return Plan{}, err
	}
	batch := c.ID
	if batch == "" {
		batch = b.env.NewID()
	}
	return b.finish([]event.Event{
		b.newEvent(b.env.NewID(), from, pause(batch)),
		b.newEvent(b.env.NewID(), to, resume(batch)),
		b.newEvent(b.env.NewID(), b.at, event.BatchCommitted{Batch: batch}),
	}, batch)
}

// validate is the shape of a break: a cycle, from and to, from before to.
func (c BackfillBreak) validate(b *builder) error {
	if c.CycleID == "" {
		return b.invalid("A break needs the cycle_id of the cycle it pauses.")
	}
	if err := b.validText(string(c.CycleID)); err != nil {
		return err
	}
	if c.From.IsZero() || c.To.IsZero() {
		return b.invalid("A break needs from and to, the instants it begins and ends.")
	}
	from, to := event.At(c.From).Time(), event.At(c.To).Time()
	if !from.Before(to) {
		return b.invalid("The break's from, %s, is not before its to, %s, to the millisecond, so the break has no length.", b.instant(from), b.instant(to))
	}
	return nil
}
