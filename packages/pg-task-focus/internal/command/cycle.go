package command

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
)

// maxMinutes bounds a start's minutes override and a boost, which keeps
// duration arithmetic far from overflow.
const maxMinutes = 525600

// reservedKey is the annotation key the connector owns.
const reservedKey = "cycle_type"

// keyPattern is the form of an annotation key.
var keyPattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

// placeholderCycle stands in for a cycle id not resolved yet when the size of
// a planned event is checked.
const placeholderCycle = event.CycleID(placeholderID)

// StartCycle starts a cycle of a configured type, with the type's title and
// planned minutes (Minutes overrides the minutes). A cycle running at the
// start's instant is interrupted by it: the new cycle.started names it in
// interrupts, which the system sets and the client never does. A type
// outside the active profile is allowed.
type StartCycle struct {
	ID          event.ID
	Type        string
	Minutes     *int
	EffectiveAt *time.Time
}

// PauseCycle pauses a cycle. With no CycleID it is the cycle running at the
// request's instant, else the only cycle that is not stopped.
type PauseCycle struct {
	ID          event.ID
	CycleID     event.CycleID
	EffectiveAt *time.Time
}

// ResumeCycle resumes a paused cycle. With no CycleID it is the only cycle that
// is not stopped.
type ResumeCycle struct {
	ID          event.ID
	CycleID     event.CycleID
	EffectiveAt *time.Time
}

// BoostCycle adds minutes to a running or paused cycle. With no CycleID it is
// the cycle running at the request's instant, else the only cycle that is not
// stopped.
type BoostCycle struct {
	ID          event.ID
	CycleID     event.CycleID
	Minutes     int
	EffectiveAt *time.Time
}

// StopCycle stops a cycle. With no CycleID it is the cycle running at the
// request's instant, else the only cycle that is not stopped.
type StopCycle struct {
	ID          event.ID
	CycleID     event.CycleID
	EffectiveAt *time.Time
}

// SwitchCycle makes the paused cycle To the focus: one batch pauses the cycle
// running at the request's instant and resumes To at that same instant. There
// is no switched event; a switch is that pause and that resume.
type SwitchCycle struct {
	ID          event.ID
	To          event.CycleID
	EffectiveAt *time.Time
}

// AnnotateCycle replaces a cycle's note and key/value list, in any state,
// stopped included. With no CycleID it is the only cycle that is not stopped,
// so a stopped cycle is annotated only by naming it. It takes effect now.
type AnnotateCycle struct {
	ID      event.ID
	CycleID event.CycleID
	Note    string
	KV      []event.KV
}

// Name implements Command.
func (StartCycle) Name() string { return "cycles/start" }

// Name implements Command.
func (PauseCycle) Name() string { return "cycles/pause" }

// Name implements Command.
func (ResumeCycle) Name() string { return "cycles/resume" }

// Name implements Command.
func (BoostCycle) Name() string { return "cycles/boost" }

// Name implements Command.
func (StopCycle) Name() string { return "cycles/stop" }

// Name implements Command.
func (SwitchCycle) Name() string { return "cycles/switch" }

// Name implements Command.
func (AnnotateCycle) Name() string { return "cycles/annotate" }

// ClientID implements Command.
func (c StartCycle) ClientID() event.ID { return c.ID }

// ClientID implements Command.
func (c PauseCycle) ClientID() event.ID { return c.ID }

// ClientID implements Command.
func (c ResumeCycle) ClientID() event.ID { return c.ID }

// ClientID implements Command.
func (c BoostCycle) ClientID() event.ID { return c.ID }

// ClientID implements Command.
func (c StopCycle) ClientID() event.ID { return c.ID }

// ClientID implements Command.
func (c SwitchCycle) ClientID() event.ID { return c.ID }

// ClientID implements Command.
func (c AnnotateCycle) ClientID() event.ID { return c.ID }

// IsDryRun implements Command: no cycle verb has a dry run.
func (StartCycle) IsDryRun() bool { return false }

// IsDryRun implements Command.
func (PauseCycle) IsDryRun() bool { return false }

// IsDryRun implements Command.
func (ResumeCycle) IsDryRun() bool { return false }

// IsDryRun implements Command.
func (BoostCycle) IsDryRun() bool { return false }

// IsDryRun implements Command.
func (StopCycle) IsDryRun() bool { return false }

// IsDryRun implements Command.
func (SwitchCycle) IsDryRun() bool { return false }

// IsDryRun implements Command.
func (AnnotateCycle) IsDryRun() bool { return false }

// cycleFields are the client fields of pause, resume and stop.
type cycleFields struct {
	CycleID     event.CycleID  `json:"cycle_id,omitempty"`
	EffectiveAt *event.Instant `json:"effective_at,omitempty"`
}

// ReqHash implements Command.
func (c StartCycle) ReqHash() (string, error) {
	return event.ReqHash(c.Name(), struct {
		Type        string         `json:"type"`
		Minutes     *int           `json:"minutes,omitempty"`
		EffectiveAt *event.Instant `json:"effective_at,omitempty"`
	}{c.Type, c.Minutes, instantOf(c.EffectiveAt)})
}

// ReqHash implements Command.
func (c PauseCycle) ReqHash() (string, error) {
	return event.ReqHash(c.Name(), cycleFields{c.CycleID, instantOf(c.EffectiveAt)})
}

// ReqHash implements Command.
func (c ResumeCycle) ReqHash() (string, error) {
	return event.ReqHash(c.Name(), cycleFields{c.CycleID, instantOf(c.EffectiveAt)})
}

// ReqHash implements Command.
func (c StopCycle) ReqHash() (string, error) {
	return event.ReqHash(c.Name(), cycleFields{c.CycleID, instantOf(c.EffectiveAt)})
}

// ReqHash implements Command.
func (c BoostCycle) ReqHash() (string, error) {
	return event.ReqHash(c.Name(), struct {
		CycleID     event.CycleID  `json:"cycle_id,omitempty"`
		Minutes     int            `json:"minutes"`
		EffectiveAt *event.Instant `json:"effective_at,omitempty"`
	}{c.CycleID, c.Minutes, instantOf(c.EffectiveAt)})
}

// ReqHash implements Command.
func (c SwitchCycle) ReqHash() (string, error) {
	return event.ReqHash(c.Name(), struct {
		To          event.CycleID  `json:"to"`
		EffectiveAt *event.Instant `json:"effective_at,omitempty"`
	}{c.To, instantOf(c.EffectiveAt)})
}

// ReqHash implements Command.
func (c AnnotateCycle) ReqHash() (string, error) {
	return event.ReqHash(c.Name(), struct {
		CycleID event.CycleID `json:"cycle_id,omitempty"`
		Note    string        `json:"note,omitempty"`
		KV      []event.KV    `json:"kv,omitempty"`
	}{c.CycleID, c.Note, c.KV})
}

func (c StartCycle) plan(b *builder) (Plan, error) {
	eff := b.effective(c.EffectiveAt)
	if c.Type == "" {
		return Plan{}, stamped(b.invalid("A cycle start needs the cycle type."), eff)
	}
	if err := b.validText(c.Type); err != nil {
		return Plan{}, stamped(err, eff)
	}
	if c.Minutes != nil {
		if err := b.minutes("minutes", *c.Minutes); err != nil {
			return Plan{}, stamped(err, eff)
		}
	}
	def, known := b.env.Config.CycleType(c.Type)
	p := event.CycleStarted{
		CycleID: placeholderCycle, Type: c.Type, Title: def.Title,
		PlannedMinutes: b.env.Config.CycleMinutes(c.Type, c.Minutes),
	}
	if !known {
		p.Title = c.Type // the size check only; an unknown type is refused below
	}
	// The cycle running at the start's own instant is the one it interrupts.
	if running, ok := b.env.Model.RunningAt(eff); ok {
		p.Interrupts = running.ID
	}
	if err := b.encodable(eff, p); err != nil {
		return Plan{}, stamped(err, eff)
	}
	if err := b.notFuture(eff, c.EffectiveAt); err != nil {
		return Plan{}, err
	}
	if !known {
		return Plan{}, &Rejection{
			Reason: ReasonUnknownCycleType, Instants: []time.Time{eff},
			Message: fmt.Sprintf("The cycle type %q is not defined in the configuration.", c.Type),
		}
	}
	// With no effective_at the start acts on the cycle it interrupts and on
	// the present focus: a clock behind either would stamp the start before
	// events the operator has already recorded. A start that interrupts
	// nothing and finds no focus acts on no stored cycle.
	var acted []string
	if p.Interrupts != "" {
		acted = append(acted, string(p.Interrupts))
	}
	if focus, ok := b.env.Model.Running(); ok && focus.ID != p.Interrupts {
		acted = append(acted, string(focus.ID))
	}
	if err := b.clockBehind(c.EffectiveAt, eff, "cycle", acted...); err != nil {
		return Plan{}, err
	}
	p.CycleID = event.CycleID(b.env.NewID())
	return b.finish([]event.Event{b.lone(eff, p)}, "")
}

func (c PauseCycle) plan(b *builder) (Plan, error) {
	return b.cycleVerb(c.CycleID, c.EffectiveAt, verb{
		name: "pause", repeat: projection.Paused, runningFirst: true,
		payload: func(id event.CycleID) event.Payload { return event.CyclePaused{CycleID: id} },
	})
}

func (c ResumeCycle) plan(b *builder) (Plan, error) {
	return b.cycleVerb(c.CycleID, c.EffectiveAt, verb{
		name: "resume", repeat: projection.Running,
		payload: func(id event.CycleID) event.Payload { return event.CycleResumed{CycleID: id} },
	})
}

func (c BoostCycle) plan(b *builder) (Plan, error) {
	if err := b.minutes("a boost's minutes", c.Minutes); err != nil {
		return Plan{}, stamped(err, b.effective(c.EffectiveAt))
	}
	return b.cycleVerb(c.CycleID, c.EffectiveAt, verb{
		name: "boost", runningFirst: true,
		payload: func(id event.CycleID) event.Payload { return event.CycleBoosted{CycleID: id, Minutes: c.Minutes} },
	})
}

func (c StopCycle) plan(b *builder) (Plan, error) {
	p, err := b.cycleVerb(c.CycleID, c.EffectiveAt, verb{
		name: "stop", repeat: projection.Stopped, runningFirst: true,
		payload: func(id event.CycleID) event.Payload { return event.CycleStopped{CycleID: id} },
	})
	// A stop earlier than the cycle's stop finds it already stopped at the
	// later instant: the stop to move is the stored one.
	var r *Rejection
	if errors.As(err, &r) && r.Reason == ReasonCycleStopped {
		stops := b.liveEvents(func(p event.Payload) bool {
			s, ok := p.(event.CycleStopped)
			return ok && string(s.CycleID) == r.Entity
		}, event.TypeCycleStopped)
		if len(stops) > 0 {
			r.Message += fmt.Sprintf(" To stop the cycle earlier, correct the time of its stop, event %s, instead.", stops[0].ID)
		}
	}
	return p, err
}

func (c AnnotateCycle) plan(b *builder) (Plan, error) {
	strs := []string{string(c.CycleID), c.Note}
	for _, kv := range c.KV {
		strs = append(strs, kv.Key, kv.Value)
	}
	eff := b.effective(nil)
	if err := b.validText(strs...); err != nil {
		return Plan{}, stamped(err, eff)
	}
	for i, kv := range c.KV {
		if !keyPattern.MatchString(kv.Key) {
			return Plan{}, stamped(b.invalid("The key %q of kv[%d] does not match [a-z0-9_-]+ (lower-case letters, digits, underscore and hyphen).", kv.Key, i), eff)
		}
	}
	p := event.CycleAnnotated{CycleID: c.CycleID, Note: c.Note, KV: c.KV}
	if p.CycleID == "" {
		p.CycleID = placeholderCycle
	}
	if err := b.encodable(eff, p); err != nil {
		return Plan{}, stamped(err, eff)
	}
	for i, kv := range c.KV {
		if kv.Key == reservedKey {
			return Plan{}, &Rejection{
				Reason:   ReasonReservedKey,
				Instants: []time.Time{eff},
				Message:  fmt.Sprintf("The key %s of kv[%d] is reserved for the connector and cannot be supplied.", reservedKey, i),
			}
		}
	}
	return b.cycleVerb(c.CycleID, nil, verb{
		name: "annotate", checked: true,
		payload: func(id event.CycleID) event.Payload {
			return event.CycleAnnotated{CycleID: id, Note: c.Note, KV: c.KV}
		},
	})
}

func (c SwitchCycle) plan(b *builder) (Plan, error) {
	eff := b.effective(c.EffectiveAt)
	if c.To == "" {
		return Plan{}, stamped(b.invalid("A switch needs to, the paused cycle that becomes the focus."), eff)
	}
	if err := b.validText(string(c.To)); err != nil {
		return Plan{}, stamped(err, eff)
	}
	pause := func(id event.CycleID, batch event.ID) event.Payload {
		return event.CyclePaused{CycleID: id, Batch: batch}
	}
	resume := func(id event.CycleID, batch event.ID) event.Payload {
		return event.CycleResumed{CycleID: id, Batch: batch}
	}
	if err := b.encodable(eff, pause(placeholderCycle, placeholderID), resume(c.To, placeholderID), event.BatchCommitted{Batch: placeholderID}); err != nil {
		return Plan{}, stamped(err, eff)
	}
	if err := b.notFuture(eff, c.EffectiveAt); err != nil {
		return Plan{}, err
	}
	to, err := b.named(c.To, eff)
	if err != nil {
		return Plan{}, err
	}
	// The cycle paused is the one running at the switch's own instant, so a
	// backdated switch after that cycle stopped is still a switch.
	focus, ok := b.env.Model.RunningAt(eff)
	if !ok {
		return Plan{}, &Rejection{
			Reason: ReasonNoRunningCycle, Entity: string(to.ID), Instants: []time.Time{eff},
			Message: fmt.Sprintf("No cycle is running at %s, so there is no focus to switch away from; resume cycle %s instead.", b.instant(eff), to.ID),
		}
	}
	if focus.ID == to.ID {
		return b.noOp(to, eff, projection.Running, ", so it is already the focus")
	}
	if err := b.clockBehind(c.EffectiveAt, eff, "cycle", string(focus.ID), string(to.ID)); err != nil {
		return Plan{}, err
	}
	bt := b.batch(eff)
	bt.add(pause(focus.ID, bt.id))
	bt.add(resume(to.ID, bt.id))
	return bt.finish(false, nil)
}

// minutes refuses a minutes value outside 1 to 525600.
func (b *builder) minutes(what string, n int) error {
	if n < 1 || n > maxMinutes {
		return b.invalid("The %s, %d, is outside 1 to %d.", what, n, maxMinutes)
	}
	return nil
}

// verb is how one cycle verb plans: the state in which a repeat of it is a
// no-op (none for boost and annotate), whether an omitted cycle id is first the
// cycle running at the request's instant, the event it adds, and whether the
// command has already checked its text and the event's size itself.
type verb struct {
	name         string
	repeat       projection.CycleStatus
	runningFirst bool
	checked      bool
	payload      func(event.CycleID) event.Payload
}

// cycleVerb plans a verb that adds one event to one cycle.
func (b *builder) cycleVerb(id event.CycleID, supplied *time.Time, v verb) (Plan, error) {
	eff := b.effective(supplied)
	if !v.checked {
		if err := b.validText(string(id)); err != nil {
			return Plan{}, stamped(err, eff)
		}
		provisional := id
		if provisional == "" {
			provisional = placeholderCycle
		}
		if err := b.encodable(eff, v.payload(provisional)); err != nil {
			return Plan{}, stamped(err, eff)
		}
	}
	if err := b.notFuture(eff, supplied); err != nil {
		return Plan{}, err
	}
	c, err := b.target(id, eff, v)
	if err != nil {
		return Plan{}, err
	}
	// A cycle already in the requested state at the request's instant is a
	// no-op; a cycle not started then never is.
	if v.repeat != "" && c.StatusAt(eff) == v.repeat {
		return b.noOp(c, eff, v.repeat, "")
	}
	if err := b.clockBehind(supplied, eff, "cycle", string(c.ID)); err != nil {
		return Plan{}, err
	}
	return b.finish([]event.Event{b.lone(eff, v.payload(c.ID))}, "")
}

// named is the stored cycle a request names.
func (b *builder) named(id event.CycleID, eff time.Time) (projection.Cycle, error) {
	c, ok := b.env.Model.Cycle(id)
	if !ok {
		return projection.Cycle{}, &Rejection{
			Reason: ReasonUnknownCycle, Instants: []time.Time{eff},
			Message: fmt.Sprintf("No cycle %s exists in the log.", id),
		}
	}
	return c, nil
}

// target is the cycle a verb acts on: the one named; else, for pause, boost
// and stop, the cycle running at the request's instant; else the only cycle
// that is not stopped. There is no "last cycle": with several candidates the
// request is ambiguous, and with none there is no cycle to act on.
func (b *builder) target(id event.CycleID, eff time.Time, v verb) (projection.Cycle, error) {
	if id != "" {
		return b.named(id, eff)
	}
	if v.runningFirst {
		if c, ok := b.env.Model.RunningAt(eff); ok {
			return c, nil
		}
	}
	var open []projection.Cycle
	for _, c := range b.env.Model.Cycles() {
		if c.Status != projection.Stopped {
			open = append(open, c)
		}
	}
	switch len(open) {
	case 1:
		return open[0], nil
	case 0:
		where := "no cycle is open (running or paused)"
		if v.runningFirst {
			where = fmt.Sprintf("no cycle is running at %s and none is paused", b.instant(eff))
		}
		return projection.Cycle{}, &Rejection{
			Reason: ReasonNoRunningCycle, Instants: []time.Time{eff},
			Message: fmt.Sprintf("No cycle_id was given and %s, so there is no cycle to %s.", where, v.name),
		}
	}
	r := &Rejection{Reason: ReasonCycleAmbiguous, Instants: []time.Time{eff}}
	var names []string
	for _, c := range open {
		ref := b.cycleRef(c.ID)
		r.Cycles = append(r.Cycles, ref)
		names = append(names, fmt.Sprintf("cycle %s (%s)", ref.ID, ref.Title))
	}
	lead := "No cycle_id was given"
	if v.runningFirst {
		lead += fmt.Sprintf(", no cycle is running at %s,", b.instant(eff))
	}
	r.Message = fmt.Sprintf("%s and %d cycles are not stopped: %s; name the cycle to %s.", lead, len(open), list(names), v.name)
	return projection.Cycle{}, r
}

// noOp is the answer to a request that finds cycle c already in state at eff:
// nothing to append, and one sentence naming the cycle and since when.
func (b *builder) noOp(c projection.Cycle, eff time.Time, state projection.CycleStatus, tail string) (Plan, error) {
	title, _ := b.env.Model.CycleTitle(c.ID)
	if title == "" {
		title = "Cycle " + string(c.ID)
	}
	return Plan{NoOp: true, Note: fmt.Sprintf("%s has been %s since %s%s.", title, state, b.instant(since(c, eff, state)), tail)}, nil
}

// since is the instant cycle c entered state, the state it is in at eff: the
// start of the running segment holding eff, the end of the last segment
// before eff, or the stop.
func since(c projection.Cycle, eff time.Time, state projection.CycleStatus) time.Time {
	var out time.Time
	switch state {
	case projection.Stopped:
		if c.StoppedAt != nil {
			out = *c.StoppedAt
		}
	case projection.Running:
		for _, s := range c.Segments {
			if !eff.Before(s.Start) {
				out = s.Start
			}
		}
	case projection.Paused:
		for _, s := range c.Segments {
			if s.End != nil && !eff.Before(*s.End) {
				out = *s.End
			}
		}
	}
	return out
}
