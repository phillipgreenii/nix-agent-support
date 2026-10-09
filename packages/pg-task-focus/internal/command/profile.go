package command

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
)

// ChangeProfile makes Profile the active profile, effective now, in one batch
// whose id is the request id. In each current period it materializes the
// tasks the profile lists that the period does not have, withdraws the open
// tasks it does not list (a task whose definition has left the configuration
// included), and reinstates the withdrawn tasks it lists again, never
// materializing a task a second time. Completed, skipped and missed tasks are
// left alone. Changing to the active profile applies it again, which is how a
// task whose definition vanished is withdrawn.
type ChangeProfile struct {
	ID              event.ID
	Profile         string
	ExpectedVersion *Version
	DryRun          bool
}

// Name implements Command.
func (ChangeProfile) Name() string { return "profile/change" }

// ClientID implements Command: the request id is the batch id.
func (c ChangeProfile) ClientID() event.ID { return c.ID }

// IsDryRun implements Command.
func (c ChangeProfile) IsDryRun() bool { return c.DryRun }

// ReqHash implements Command. The dry-run flag and the expected version are
// not what the request asks for and never enter it.
func (c ChangeProfile) ReqHash() (string, error) {
	return event.ReqHash(c.Name(), struct {
		Profile string `json:"profile"`
	}{c.Profile})
}

// plan judges a profile change in this order: the request's own validity
// (invalid_request), the profile (unknown_profile), the expected version
// (stale_preview), then the candidate replay of the batch.
func (c ChangeProfile) plan(b *builder) (Plan, error) {
	if c.Profile == "" {
		return Plan{}, b.invalid("A profile change needs the profile to make active.")
	}
	if err := b.validText(c.Profile); err != nil {
		return Plan{}, err
	}
	eff := b.effective(nil)
	if err := b.encodable(eff, event.ProfileChanged{Profile: c.Profile, Batch: placeholderID}); err != nil {
		return Plan{}, err
	}
	profile, err := b.profile(c.Profile, true, eff)
	if err != nil {
		return Plan{}, err
	}
	if err := b.stale(c.ExpectedVersion, eff); err != nil {
		return Plan{}, err
	}
	bt := b.batch(eff)
	pv := &Preview{Version: b.env.Version}
	bt.add(event.ProfileChanged{Profile: c.Profile, Batch: bt.id})
	for _, k := range kinds {
		if period, ok := b.env.Model.Period(k); ok {
			if err := b.reconcile(bt, period, profile, pv); err != nil {
				return Plan{}, err
			}
		}
	}
	return bt.finish(c.DryRun, pv)
}

// profile is the configured profile name, which the request named (requested)
// or which is the active profile the new periods take their tasks from.
func (b *builder) profile(name string, requested bool, eff time.Time) (config.Profile, error) {
	if p, ok := b.env.Config.Profile(name); ok {
		return p, nil
	}
	msg := fmt.Sprintf("The profile %q is not defined in the configuration.", name)
	if !requested {
		msg = fmt.Sprintf("The active profile %q is no longer defined in the configuration, so the new periods have no profile to take their tasks from; name a defined profile in the request.", name)
	}
	return config.Profile{}, &Rejection{Reason: ReasonUnknownProfile, Instants: []time.Time{eff}, Message: msg}
}

// stale refuses a request whose expected version differs from the present one
// in either member, so what the operator previewed is what happens.
func (b *builder) stale(want *Version, eff time.Time) error {
	if want == nil || *want == b.env.Version {
		return nil
	}
	return &Rejection{
		Reason: ReasonStalePreview, Instants: []time.Time{eff},
		Message: fmt.Sprintf(
			"The request expects the state version (log_lines %d, config_generation %d), but the state is at (log_lines %d, config_generation %d), so what was previewed is not what would happen; preview the change again.",
			want.LogLines, want.ConfigGeneration, b.env.Version.LogLines, b.env.Version.ConfigGeneration,
		),
	}
}

// listed is the definitions profile p lists for a cadence.
func listed(p config.Profile, c due.Cadence) []string {
	switch c {
	case due.Weekly:
		return p.Weekly
	case due.Sprint:
		return p.Sprint
	}
	return p.Daily
}

// reconcile adds to bt what applying profile p to a current period changes:
// a withdrawal of each open task it does not list, a reinstatement of each
// withdrawn task it lists, then a materialization of each definition it lists
// that the period has no task of. Resolved and missed tasks are left alone.
func (b *builder) reconcile(bt *batchBuilder, period projection.Period, p config.Profile, pv *Preview) error {
	cad := cadenceOf(period.Kind)
	names := listed(p, cad)
	has := map[string]bool{}
	for _, task := range b.env.Model.Tasks() {
		if task.Cadence != cad || task.PeriodStart != period.Start {
			continue
		}
		has[task.Definition] = true
		in := slices.Contains(names, task.Definition)
		switch {
		case task.Status == projection.Open && !in:
			bt.add(event.TaskWithdrawn{TaskID: task.ID, Batch: bt.id})
			pv.ProfileWithdraw = append(pv.ProfileWithdraw, b.taskRef(task.ID, task.Title, task.Due))
		case task.Status == projection.Withdrawn && in:
			bt.add(event.TaskReinstated{TaskID: task.ID, Batch: bt.id})
			pv.ProfileReinstate = append(pv.ProfileReinstate, b.taskRef(task.ID, task.Title, task.Due))
		}
	}
	var missing []string
	for _, name := range names {
		if !has[name] {
			missing = append(missing, name)
		}
	}
	return b.materialize(bt, cad, period.Start, period.End, missing, &pv.ProfileAdd, pv)
}

// materialize adds to bt a task.materialized for each definition of cadence c
// in the period start to end, with the snapshot of its title, group, link,
// due rule and the due instant the rule resolves to, and lists each in refs. A
// definition whose rule matches no civil date of the period is not
// materialized and is listed in the preview with the reason.
func (b *builder) materialize(bt *batchBuilder, c due.Cadence, start, end civil.Date, names []string, refs *[]TaskRef, pv *Preview) error {
	for _, name := range names {
		def, ok := b.env.Config.Task(name)
		if !ok {
			pv.NotMaterialized = append(pv.NotMaterialized, struct{ Definition, Reason string }{name, "the definition is not in the configuration"})
			continue
		}
		res, err := due.Resolve(c, def.Due, start, end)
		var none *due.NoMatch
		switch {
		case errors.As(err, &none):
			pv.NotMaterialized = append(pv.NotMaterialized, struct{ Definition, Reason string }{
				name, fmt.Sprintf("its due rule matches no date of the period %s to %s: %s", start, end, none.Reason),
			})
			continue
		case err != nil:
			return fmt.Errorf("command: the due rule of %s does not resolve in the period %s to %s: %w", name, start, end, err)
		}
		id := event.NewTaskID(c, start, name)
		bt.add(event.TaskMaterialized{
			TaskID: id, Definition: name, Cadence: c, Period: start,
			Title: def.Title, Group: def.Group, Link: def.Link,
			Due: event.At(res.Instant), DueRule: def.Due, Batch: bt.id,
		})
		*refs = append(*refs, b.taskRef(id, def.Title, res.Instant))
	}
	return nil
}

// taskRef names a task in a preview, flagged overdue when its due instant has
// passed by the clock the request is recorded at.
func (b *builder) taskRef(id event.TaskID, title string, dueAt time.Time) TaskRef {
	return TaskRef{ID: id, Title: title, Overdue: dueAt.Before(b.at)}
}

// batchBuilder collects the events of one batch, each with a fresh id, the
// request's effective instant and the batch's id.
type batchBuilder struct {
	b      *builder
	id     event.ID
	eff    time.Time
	events []event.Event
}

// batch starts a batch effective at eff whose id is the request id, else a
// fresh one.
func (b *builder) batch(eff time.Time) *batchBuilder {
	id := b.cmd.ClientID()
	if id == "" {
		id = b.env.NewID()
	}
	return &batchBuilder{b: b, id: id, eff: eff}
}

func (bt *batchBuilder) add(p event.Payload) {
	bt.events = append(bt.events, bt.b.newEvent(bt.b.env.NewID(), bt.eff, p))
}

// finish closes the batch with its batch.committed and runs the candidate
// replay; a dry run's plan carries the preview.
func (bt *batchBuilder) finish(dry bool, pv *Preview) (Plan, error) {
	bt.add(event.BatchCommitted{Batch: bt.id})
	p, err := bt.b.finish(bt.events, bt.id)
	if err != nil {
		return Plan{}, err
	}
	if dry {
		p.Preview = pv
	}
	return p, nil
}
