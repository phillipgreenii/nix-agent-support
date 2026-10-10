package command

import (
	"errors"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/zone"
)

// PeriodChange is one period a change begins: its kind, its first civil date,
// its last for a week or a sprint (End is required for those and absent for a
// day), the zone in force and an optional label.
type PeriodChange struct {
	Kind  projection.Kind
	Start civil.Date
	End   *civil.Date
	TZ    string
	Label string
}

// Override skips one open task of a period being left, for a reason, instead
// of marking it missed. The reason MUST NOT be blank and is stored without its
// surrounding whitespace.
type Override struct {
	TaskID event.TaskID
	Reason string
}

// ChangePeriods begins one or more periods, at most one of each kind, in one
// batch whose id is the request id. Each kind changes independently. The
// open tasks of each period being left are marked missed, or skipped: by
// their Override, else, when SkipAllReason is set, with that shared reason.
// Profile, when set, becomes the active profile of the periods that remain
// current and of the new ones. EffectiveAt may backdate the change by any
// amount; only the future-skew rule and the timeline bound it. A change is
// refused while any cycle is running or paused now, whatever its effective
// instant; a DryRun reports those cycles in its preview instead. There is no
// carry-over: no task of a period being left reaches the new one.
type ChangePeriods struct {
	ID              event.ID
	Changes         []PeriodChange
	EffectiveAt     *time.Time
	Profile         string
	Overrides       []Override
	SkipAllReason   *string
	ExpectedVersion *Version
	DryRun          bool
}

// Name implements Command.
func (ChangePeriods) Name() string { return "periods/change" }

// ClientID implements Command: the request id is the batch id.
func (c ChangePeriods) ClientID() event.ID { return c.ID }

// IsDryRun implements Command.
func (c ChangePeriods) IsDryRun() bool { return c.DryRun }

// ReqHash implements Command. The dry-run flag and the expected version are
// not what the request asks for and never enter it.
func (c ChangePeriods) ReqHash() (string, error) {
	type change struct {
		Kind  projection.Kind `json:"kind"`
		Start civil.Date      `json:"start"`
		End   *civil.Date     `json:"end,omitempty"`
		TZ    string          `json:"tz"`
		Label string          `json:"label,omitempty"`
	}
	type override struct {
		TaskID event.TaskID `json:"task_id"`
		Reason string       `json:"reason"`
	}
	changes := make([]change, len(c.Changes))
	for i, ch := range c.Changes {
		changes[i] = change(ch)
	}
	overrides := make([]override, len(c.Overrides))
	for i, o := range c.Overrides {
		overrides[i] = override(o)
	}
	return event.ReqHash(c.Name(), struct {
		Changes       []change       `json:"changes"`
		EffectiveAt   *event.Instant `json:"effective_at,omitempty"`
		Profile       string         `json:"profile,omitempty"`
		Overrides     []override     `json:"overrides,omitempty"`
		SkipAllReason *string        `json:"skip_all_reason,omitempty"`
	}{changes, instantOf(c.EffectiveAt), c.Profile, overrides, c.SkipAllReason})
}

// kinds are the kinds of period, in the order a batch takes them.
var kinds = []projection.Kind{projection.Day, projection.Week, projection.Sprint}

// cadenceOf is the cadence of the tasks of a kind of period.
func cadenceOf(k projection.Kind) due.Cadence {
	switch k {
	case projection.Week:
		return due.Weekly
	case projection.Sprint:
		return due.Sprint
	}
	return due.Daily
}

// placeholderZone stands in for a change's zone when the size of its event is
// checked and the zone is not one that loads: the codec refuses an unknown zone,
// which the zone check judges later as invalid_zone. A zone that loads is sized
// as it is.
const placeholderZone = "UTC"

// plan judges a period change in this order, the first answer stopping it:
// (1) the request's own validity (invalid_request), then future_effective_at;
// (2) each zone (invalid_zone); (3) the profile the new periods take their
// tasks from (unknown_profile); (4) the override targets (unknown_task,
// task_already_resolved, task_withdrawn, and invalid_request for a task that
// is not open in a period being left); (5) the expected version
// (stale_preview); (6) each new start against the present start of its kind
// (period_unchanged); (7) the cycles running or paused now (cycle_active,
// except for a dry run, which lists them); (8) the candidate replay of the
// batch.
func (c ChangePeriods) plan(b *builder) (Plan, error) {
	eff := b.effective(c.EffectiveAt)
	overrides, skipAll, err := c.validate(b)
	if err != nil {
		return Plan{}, stamped(err, eff)
	}
	if err := b.encodable(eff, c.sizedPayloads(b, overrides, skipAll)...); err != nil {
		return Plan{}, stamped(err, eff)
	}
	if err := b.notFuture(eff, c.EffectiveAt); err != nil {
		return Plan{}, err
	}
	for _, ch := range c.Changes {
		if _, err := zone.Load(ch.TZ); err != nil {
			return Plan{}, invalidZone(ch, err, eff)
		}
	}

	bootstrap := b.env.Model.Profile() == ""
	var first string // the profile a bootstrap batch begins with
	active := b.env.Model.Profile()
	if bootstrap {
		first = b.env.Config.Defaults().Profile
		active = first
	}
	if c.Profile != "" {
		active = c.Profile
	}
	profile, err := b.profile(active, c.Profile != "", eff)
	if err != nil {
		return Plan{}, err
	}

	changing := map[projection.Kind]bool{}
	leaving := map[projection.Kind]bool{}
	for _, ch := range c.Changes {
		changing[ch.Kind] = true
		if _, ok := b.env.Model.Period(ch.Kind); ok {
			leaving[ch.Kind] = true
		}
	}
	for _, o := range c.Overrides {
		if err := b.overridable(o.TaskID, leaving, eff); err != nil {
			return Plan{}, err
		}
	}
	if err := b.stale(c.ExpectedVersion, eff); err != nil {
		return Plan{}, err
	}
	for _, ch := range c.Changes {
		if err := b.moves(ch, eff); err != nil {
			return Plan{}, err
		}
	}
	blocking := b.activeCycles()
	if len(blocking) > 0 && !c.DryRun {
		return Plan{}, b.cycleActive(blocking, eff)
	}

	bt := b.batch(eff)
	pv := &Preview{BlockingCycles: blocking, Version: b.env.Version}
	if bootstrap {
		bt.add(event.ProfileChanged{Profile: first, Batch: bt.id})
	}
	// (1) The rollover of the open tasks of each period being left.
	tasks := b.env.Model.Tasks()
	for _, ch := range c.Changes {
		if !leaving[ch.Kind] {
			continue
		}
		for _, task := range tasks {
			if task.Status != projection.Open || task.Cadence != cadenceOf(ch.Kind) {
				continue
			}
			pv.Leaving = append(pv.Leaving, b.taskRef(task.ID, task.Title, task.Due))
			reason, skipped := overrides[task.ID]
			if !skipped && c.SkipAllReason != nil {
				reason, skipped = skipAll, true
			}
			if skipped {
				bt.add(event.TaskSkipped{TaskID: task.ID, Reason: reason, Batch: bt.id})
			} else {
				bt.add(event.TaskMissed{TaskID: task.ID, Batch: bt.id})
			}
		}
	}
	// (2) The requested profile, for the periods that remain current.
	if c.Profile != "" && c.Profile != first {
		bt.add(event.ProfileChanged{Profile: c.Profile, Batch: bt.id})
		for _, k := range kinds {
			if period, ok := b.env.Model.Period(k); ok && !changing[k] {
				if err := b.reconcile(bt, period, profile, pv); err != nil {
					return Plan{}, err
				}
			}
		}
	}
	// (3) Each new period and its tasks, from the profile now active.
	for _, ch := range c.Changes {
		bt.add(event.PeriodChanged{Kind: string(ch.Kind), Start: ch.Start, End: ch.End, TZ: ch.TZ, Label: ch.Label, Batch: bt.id})
		end := ch.Start
		if ch.End != nil {
			end = *ch.End
		}
		cad := cadenceOf(ch.Kind)
		if err := b.materialize(bt, cad, ch.Start, end, listed(profile, cad), &pv.Materialize, pv); err != nil {
			return Plan{}, err
		}
	}
	return bt.finish(c.DryRun, pv)
}

// validate is step 1 of a period change, before its instant is known: the
// changes, the text sizes and the reasons. It returns the override reasons by
// task and the shared skip reason, trimmed.
func (c ChangePeriods) validate(b *builder) (map[event.TaskID]string, string, error) {
	if len(c.Changes) == 0 {
		return nil, "", b.invalid("A period change needs at least one change of the day, the week or the sprint.")
	}
	strs := []string{c.Profile}
	seen := map[projection.Kind]bool{}
	for i, ch := range c.Changes {
		if err := b.periodShape(i, ch); err != nil {
			return nil, "", err
		}
		if seen[ch.Kind] {
			return nil, "", b.invalid("changes[%d] changes the %s a second time; a request changes each kind at most once.", i, ch.Kind)
		}
		seen[ch.Kind] = true
		strs = append(strs, ch.TZ, ch.Label)
	}
	for _, o := range c.Overrides {
		strs = append(strs, string(o.TaskID))
	}
	if err := b.validText(strs...); err != nil {
		return nil, "", err
	}
	overrides := map[event.TaskID]string{}
	for i, o := range c.Overrides {
		if o.TaskID == "" {
			return nil, "", b.invalid("overrides[%d] needs the task_id of the task to skip.", i)
		}
		if _, twice := overrides[o.TaskID]; twice {
			return nil, "", b.invalid("Task %s has more than one override; give each task one.", o.TaskID)
		}
		reason, err := b.reason("The override of task "+string(o.TaskID), o.Reason)
		if err != nil {
			return nil, "", err
		}
		overrides[o.TaskID] = reason
	}
	var skipAll string
	if c.SkipAllReason != nil {
		reason, err := b.reason("The shared skip_all_reason", *c.SkipAllReason)
		if err != nil {
			return nil, "", err
		}
		skipAll = reason
	}
	return overrides, skipAll, nil
}

// sizedPayloads are the events of the request whose size depends on what the
// client wrote, for the size check: each period change (with its zone when it
// loads, else a placeholder: an unknown zone is judged later), the profile
// change, a skip with each override reason, and a skip with the shared reason
// of each open task it may skip (the open tasks of the cadences that change,
// but not a task with an override, which takes the override's reason), each
// with its real id, so the check cannot pass a skip the real id would make too
// long. With no such task the shared reason is sized beside a placeholder task
// id.
func (c ChangePeriods) sizedPayloads(b *builder, overrides map[event.TaskID]string, skipAll string) []event.Payload {
	var out []event.Payload
	for _, ch := range c.Changes {
		tz := placeholderZone
		if _, err := zone.Load(ch.TZ); err == nil {
			tz = ch.TZ
		}
		out = append(out, event.PeriodChanged{Kind: string(ch.Kind), Start: ch.Start, End: ch.End, TZ: tz, Label: ch.Label, Batch: placeholderID})
	}
	if c.Profile != "" {
		out = append(out, event.ProfileChanged{Profile: c.Profile, Batch: placeholderID})
	}
	for id, reason := range overrides {
		out = append(out, event.TaskSkipped{TaskID: id, Reason: reason, Batch: placeholderID})
	}
	if c.SkipAllReason == nil {
		return out
	}
	cadences := map[due.Cadence]bool{}
	for _, ch := range c.Changes {
		cadences[cadenceOf(ch.Kind)] = true
	}
	sized := false
	for _, task := range b.env.Model.Tasks() {
		if _, overridden := overrides[task.ID]; overridden {
			continue // its skip carries the override's reason, sized above
		}
		if task.Status == projection.Open && cadences[task.Cadence] {
			out = append(out, event.TaskSkipped{TaskID: task.ID, Reason: skipAll, Batch: placeholderID})
			sized = true
		}
	}
	if !sized {
		out = append(out, event.TaskSkipped{TaskID: event.NewTaskID(due.Sprint, civil.Date{Year: 2000, Month: time.January, Day: 1}, "placeholder"), Reason: skipAll, Batch: placeholderID})
	}
	return out
}

// periodShape refuses a change that is not a well-formed period: an unknown
// kind, a missing or extra end, a start or end that is not a civil date, or
// an end before the start.
func (b *builder) periodShape(i int, ch PeriodChange) error {
	switch ch.Kind {
	case projection.Day:
		if ch.End != nil {
			return b.invalid("changes[%d] is a day, which takes no end: a day is the one civil date of its start.", i)
		}
	case projection.Week, projection.Sprint:
		if ch.End == nil {
			return b.invalid("changes[%d] is a %s, which requires end, its last civil date.", i, ch.Kind)
		}
	default:
		return b.invalid("changes[%d] has the kind %q, which is not day, week or sprint.", i, ch.Kind)
	}
	if ch.Start == (civil.Date{}) {
		return b.invalid("changes[%d] needs a start, the period's first civil date.", i)
	}
	if !isDate(ch.Start) {
		return b.invalid("changes[%d] has the start %s, which is not a civil date.", i, ch.Start)
	}
	if ch.End != nil {
		if !isDate(*ch.End) {
			return b.invalid("changes[%d] has the end %s, which is not a civil date.", i, ch.End)
		}
		if ch.End.Compare(ch.Start) < 0 {
			return b.invalid("changes[%d] has the end %s before its start %s.", i, ch.End, ch.Start)
		}
	}
	return nil
}

// isDate reports whether d names a civil date that exists.
func isDate(d civil.Date) bool {
	parsed, err := civil.ParseDate(d.String())
	return err == nil && parsed == d
}

// invalidZone is the invalid_zone of a change's zone.
func invalidZone(ch PeriodChange, err error, eff time.Time) *Rejection {
	why := err.Error()
	var zerr *zone.Error
	if errors.As(err, &zerr) {
		why = zerr.Message
	}
	return &Rejection{
		Reason: ReasonInvalidZone, Entity: string(ch.Kind), Instants: []time.Time{eff},
		Message: fmt.Sprintf("The %s change names a zone that cannot be used: %s.", ch.Kind, why),
	}
}

// overridable refuses an override whose task is unknown, already resolved, or
// withdrawn, as a skip of it would be, and one whose task is not open in a
// period the change leaves, which the rollover would not touch.
func (b *builder) overridable(id event.TaskID, leaving map[projection.Kind]bool, eff time.Time) error {
	task, ok := b.env.Model.Task(id)
	if !ok {
		return &Rejection{
			Reason: ReasonUnknownTask, Instants: []time.Time{eff},
			Message: fmt.Sprintf("No task %s exists in the log, so its override has nothing to skip.", id),
		}
	}
	if err := b.resolvable(task, eff, "skip"); err != nil {
		return err
	}
	for _, k := range kinds {
		if leaving[k] && task.Cadence == cadenceOf(k) && task.Status == projection.Open {
			return nil
		}
	}
	return &Rejection{
		Reason: ReasonInvalidRequest, Entity: string(id), Instants: []time.Time{eff},
		Message: fmt.Sprintf("Task %s is %s and not an open task of a period this change leaves, so the rollover does not touch it and there is nothing to override.", id, task.Status),
	}
}

// moves refuses a change whose start is not later than the present start of
// its kind. The facts are those replay gives for the same pair of events.
func (b *builder) moves(ch PeriodChange, eff time.Time) error {
	cur, ok := b.env.Model.Period(ch.Kind)
	if !ok || ch.Start.Compare(cur.Start) > 0 {
		return nil
	}
	opened := eff
	if v, ok := b.env.Model.Event(cur.OpenedBy); ok {
		opened = v.Corrected.EffectiveAt.Time()
	}
	return &Rejection{
		Reason: ReasonPeriodUnchanged, Entity: string(ch.Kind),
		Events:   []event.ID{cur.OpenedBy},
		Instants: instantsInOrder(opened, eff),
		Message: fmt.Sprintf(
			"The %s period start %s set by the new event effective at %s is not later than the start %s set by event %s effective at %s, so the %s period does not move forward; retract that change to go back.",
			ch.Kind, ch.Start, b.instant(eff), cur.Start, cur.OpenedBy, b.instant(opened), ch.Kind,
		),
	}
}

// activeCycles names every cycle that is running or paused in the present
// state, whatever instant the change takes effect at.
func (b *builder) activeCycles() []CycleRef {
	var out []CycleRef
	for _, c := range b.env.Model.Cycles() {
		if c.Status == projection.Running || c.Status == projection.Paused {
			out = append(out, b.cycleRef(c.ID))
		}
	}
	return out
}

// cycleActive is the refusal of a period change while the cycles refs are
// active. A period change never stops a cycle: the operator stops each, at the
// time the work ended for one left running, and repeats the change.
func (b *builder) cycleActive(refs []CycleRef, eff time.Time) *Rejection {
	names := make([]string, len(refs))
	instants := []time.Time{eff}
	for i, r := range refs {
		names[i] = fmt.Sprintf("cycle %s (%s, %s, started at %s)", r.ID, r.Title, r.Status, b.instant(r.StartedAt))
		instants = append(instants, r.StartedAt)
	}
	verb := "is"
	if len(refs) > 1 {
		verb = "are"
	}
	return &Rejection{
		Reason: ReasonCycleActive, Cycles: refs, Instants: instantsInOrder(instants...),
		Message: fmt.Sprintf(
			"A period change is refused while any cycle is running or paused, whatever its effective_at, and %s %s not stopped; stop each first, at the time the work ended for one left running, then repeat the change.",
			list(names), verb,
		),
	}
}
