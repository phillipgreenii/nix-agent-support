package command

import (
	"errors"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

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

// materialize adds to bt a task.materialized for each definition of cadence c
// in the period start to end, with the snapshot of its title, group, link,
// due rule and the due instant the rule resolves to, and lists each in refs. A
// definition whose rule matches no civil date of the period is not
// materialized and is listed in the preview with the reason.
func (b *builder) materialize(bt *batchBuilder, c due.Cadence, start, end civil.Date, names []string, refs *[]TaskRef, pv *Preview) error {
	for _, name := range names {
		def, ok := b.env.Config.Task(name)
		if !ok {
			pv.NotMaterialized = append(pv.NotMaterialized, notMaterialized{name, "the definition is not in the configuration"})
			continue
		}
		res, err := due.Resolve(c, def.Due, start, end)
		var none *due.NoMatch
		switch {
		case errors.As(err, &none):
			pv.NotMaterialized = append(pv.NotMaterialized, notMaterialized{
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
