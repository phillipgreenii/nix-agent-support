package view

import (
	"cmp"
	"slices"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
)

// kinds lists the period kinds in the order a checklist shows them.
var kinds = []projection.Kind{projection.Day, projection.Week, projection.Sprint}

// cadenceOf is the cadence of the tasks of a kind of period.
var cadenceOf = map[projection.Kind]due.Cadence{
	projection.Day:    due.Daily,
	projection.Week:   due.Weekly,
	projection.Sprint: due.Sprint,
}

// TaskView is one task of a current period as the clients show it. GroupRank
// is the rank the configuration gives the task's group now, so reordering
// group_order reorders a checklist that already exists. Overdue is set for an
// open task whose due time has passed; how it is worded is the client's.
type TaskView struct {
	Kind      projection.Kind
	Task      projection.Task
	GroupRank int
	Overdue   bool
}

// tasksOf lists the tasks of the current period of every kind, ordered by
// kind, then group rank (a group the configuration does not list ranks last),
// then due time, then the order the log materialized them in. The tasks of a
// period that has been rolled over belong to history and are not listed.
func tasksOf(m *projection.Model, cfg *config.Config, now time.Time, periods []PeriodState) []TaskView {
	all := m.Tasks()
	var out []TaskView
	for _, p := range periods {
		for _, t := range all {
			if t.Cadence != cadenceOf[p.Kind] || t.PeriodStart.Compare(p.Start) != 0 {
				continue
			}
			out = append(out, TaskView{
				Kind: p.Kind, Task: t, GroupRank: cfg.GroupRank(t.Group),
				Overdue: t.Status == projection.Open && t.Due.Before(now),
			})
		}
	}
	// The sort is stable, so tasks that tie keep the order of materialization.
	slices.SortStableFunc(out, func(a, b TaskView) int {
		return cmp.Or(
			cmp.Compare(slices.Index(kinds, a.Kind), slices.Index(kinds, b.Kind)),
			cmp.Compare(a.GroupRank, b.GroupRank),
			a.Task.Due.Compare(b.Task.Due),
		)
	})
	return out
}

// nextOf is the open task, across every kind, with the earliest due time; a
// tie goes to the lower group rank, and a tie after that to the task listed
// first. A completed, skipped, missed or withdrawn task is never next.
func nextOf(tasks []TaskView) *TaskView {
	var best *TaskView
	for i := range tasks {
		t := &tasks[i]
		if t.Task.Status != projection.Open {
			continue
		}
		if best == nil || cmp.Or(t.Task.Due.Compare(best.Task.Due), cmp.Compare(t.GroupRank, best.GroupRank)) < 0 {
			best = t
		}
	}
	if best == nil {
		return nil
	}
	next := *best
	return &next
}
