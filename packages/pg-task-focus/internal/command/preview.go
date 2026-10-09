package command

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// TaskRef names a task in a preview: its id, its title and whether its due
// instant has already passed when the preview is built, so it would be overdue
// as soon as the change is made.
type TaskRef struct {
	ID      event.TaskID
	Title   string
	Overdue bool
}

// Preview is what the dry run of a period or profile change reports, built
// from the same plan the real request would append. Leaving lists the open
// tasks of each period being left; Materialize the tasks the new periods
// would materialize, and NotMaterialized the definitions they could not, each
// with the reason; ProfileAdd, ProfileWithdraw and ProfileReinstate the tasks a
// requested profile change would add, withdraw and reinstate in the periods
// that remain current. BlockingCycles lists every cycle that is running or
// paused now, which makes the real period change fail as cycle_active; there
// is no stop time to fill in. Version is the state version the preview was
// built at, for the client to carry back as the expected version.
type Preview struct {
	Leaving          []TaskRef
	Materialize      []TaskRef
	ProfileAdd       []TaskRef
	ProfileWithdraw  []TaskRef
	ProfileReinstate []TaskRef
	NotMaterialized  []struct{ Definition, Reason string }
	BlockingCycles   []CycleRef
	Version          Version
}

// notMaterialized is an entry of Preview.NotMaterialized: a definition the
// new periods could not materialize, with the reason.
type notMaterialized = struct{ Definition, Reason string }

// taskRef names a task in a preview, flagged overdue when its due instant has
// passed by the clock the request is recorded at.
func (b *builder) taskRef(id event.TaskID, title string, dueAt time.Time) TaskRef {
	return TaskRef{ID: id, Title: title, Overdue: dueAt.Before(b.at)}
}
