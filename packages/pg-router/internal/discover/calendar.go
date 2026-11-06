package discover

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/phillipgreenii/pg-router/internal/calstate"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/query"
)

// SlotRecord is one calendar query's remembered slot (see internal/calstate).
type SlotRecord = calstate.Record

// maxSupersededScan bounds the slot walk that counts passed-over slots.
const maxSupersededScan = 100000

// CalendarReport is what one pass decided for one calendar source. The caller
// (the visibility layer) turns it into log lines; this package only computes and
// exposes it.
type CalendarReport struct {
	// Slot is P = Prev(now), the one slot evaluated this pass (zero when the
	// trigger has none).
	Slot time.Time
	// FirstStart is true when the source had no record: last_slot was set to
	// Slot and nothing fired.
	FirstStart bool
	// Due is true when Slot was later than the remembered slot.
	Due bool
	// Missed is true when the router was not running, or the host asleep, at
	// Slot; MissedReason is then the stored reason string.
	Missed       bool
	MissedReason string
	// Fired is true when a tick was enqueued; Outcome is the recorded outcome
	// (empty when nothing was recorded this pass).
	Fired   bool
	Outcome string
	// Superseded counts the slots passed over because several were due at one
	// evaluation (earlier than Slot, later than the remembered slot, never
	// fired), with the first and last of them. A slot with a failed enqueue is
	// excluded.
	Superseded      int
	FirstSuperseded time.Time
	LastSuperseded  time.Time
	// Late is how long after Slot an on-time fire happened (set whenever it
	// fired on time, whatever its size).
	Late time.Duration
	// Clamped is true when a remembered slot later than now (a clock jump then
	// correction) was clamped to Slot; ClampedFrom is the stored value.
	Clamped     bool
	ClampedFrom time.Time
	// EnqueueFailed is true when this pass tried to fire Slot and the enqueue
	// failed (the slot stays unhandled and is retried). FailedSlotSkipped is
	// set when an older slot whose enqueue had failed was recorded skipped
	// because the following slot arrived first.
	EnqueueFailed     bool
	FailedSlotSkipped time.Time
}

// calendarPass evaluates one calendar-triggered source for this pass: it decides
// whether slot P = Prev(now) is due, whether it was missed, applies the source's
// on_missed policy, and records the slot ONLY after a successful enqueue.
func calendarPass(ctx context.Context, env query.Env, s query.Source, ct query.CalendarTrigger, q *eventqueue.Queue, declared core.Bindings, sleep sleepFunc, obs SourceFailureObserver, activityObs SourceActivityObserver, rpt *ProduceReport, now func() time.Time, nowT time.Time, cad Cadence) error {
	name := s.Name
	p := ct.Prev(nowT)
	cr := CalendarReport{Slot: p}
	defer func() { rpt.Calendar[name] = cr }()
	if p.IsZero() {
		return nil
	}
	save := func() {
		if cad.Save == nil {
			return
		}
		if err := cad.Save(cad.Slots); err != nil {
			slog.Warn("calendar state write failed", "query", name, "err", err)
		}
	}
	rec, has := cad.Slots[name]
	if !has {
		// First start: nothing counts as missed and nothing fires.
		cad.Slots[name] = SlotRecord{LastSlot: p, Outcome: calstate.OutcomeSkipped, Reason: calstate.ReasonFirstStart, DecidedAt: nowT}
		cr.FirstStart = true
		save()
		return nil
	}
	if rec.LastSlot.After(nowT) {
		// The clock jumped and was corrected: the stored slot is in the future.
		cr.Clamped, cr.ClampedFrom = true, rec.LastSlot
		rec = SlotRecord{LastSlot: p, Outcome: rec.Outcome, Reason: rec.Reason, DecidedAt: nowT}
		cad.Slots[name] = rec
		delete(cad.FailedSlots, name)
		save()
	}
	if fs, ok := cad.FailedSlots[name]; ok && fs.Before(p) {
		// The following slot arrived before the failed one could be enqueued.
		rec = SlotRecord{LastSlot: fs, Outcome: calstate.OutcomeSkipped, Reason: calstate.ReasonEnqueueFailed, DecidedAt: nowT}
		cad.Slots[name] = rec
		delete(cad.FailedSlots, name)
		cr.FailedSlotSkipped = fs
		slog.Warn("calendar slot skipped: enqueue failed", "query", name, "slot", fs)
		save()
	}
	if !p.After(rec.LastSlot) {
		return nil
	}
	cr.Due = true
	// Slots between the remembered one and P are passed over, never fired.
	for sl, n := ct.Next(rec.LastSlot), 0; !sl.IsZero() && sl.Before(p) && n < maxSupersededScan; sl, n = ct.Next(sl), n+1 {
		if cr.Superseded == 0 {
			cr.FirstSuperseded = sl
		}
		cr.Superseded++
		cr.LastSuperseded = sl
	}
	reason := ""
	switch {
	case !cad.ProcessStart.IsZero() && cad.ProcessStart.After(p):
		reason = calstate.ReasonRouterNotRunning
	case cad.Asleep != nil && cad.Asleep(p):
		reason = calstate.ReasonHostAsleep
	}
	cr.Missed, cr.MissedReason = reason != "", reason
	if cr.Missed && ct.OnMissed != "run-now" {
		// next-slot: record skipped, enqueue nothing, advance the success clock so
		// liveness is not hurt.
		cad.Slots[name] = SlotRecord{LastSlot: p, Outcome: calstate.OutcomeSkipped, Reason: reason, DecidedAt: nowT}
		cr.Outcome = calstate.OutcomeSkipped
		rpt.LastTick[name] = nowT
		if obs != nil {
			obs.OnSourceSucceeded(name)
		}
		save()
		return nil
	}
	slot := query.Slot{Name: name, Time: p, OnMissed: cr.Missed, Now: nowT}
	err := runAndEnqueue(ctx, env, s, q, declared, sleep, obs, activityObs, rpt, now, &slot)
	if _, failed := rpt.SourceErrors[name]; failed || err != nil {
		cad.FailedSlots[name] = p
		cr.EnqueueFailed = true
		return err
	}
	delete(cad.FailedSlots, name)
	out := SlotRecord{LastSlot: p, DecidedAt: nowT}
	if cr.Missed {
		out.Outcome, out.Reason = calstate.OutcomeCatchUp, reason
	} else {
		out.Outcome = calstate.OutcomeFired
		cr.Late = nowT.Sub(p)
		if cr.Late > cad.PollInterval {
			out.Reason = fmt.Sprintf("late by %s", cr.Late.Round(time.Second))
		}
	}
	cad.Slots[name] = out
	cr.Fired, cr.Outcome = true, out.Outcome
	save()
	return nil
}
