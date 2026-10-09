package projection

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// The codes the task state can find.
const (
	codeTaskAlreadyResolved             Code = "task_already_resolved"
	codeTaskWithdrawn                   Code = "task_withdrawn"
	codeTaskNotWithdrawn                Code = "task_not_withdrawn"
	codeTaskMaterializedTwice           Code = "task_materialized_twice"
	codeTaskWithoutPeriod               Code = "task_without_period"
	codeTaskBeforeProfile               Code = "task_before_profile"
	codeResolutionBeforeMaterialization Code = "resolution_before_materialization"
)

// TaskStatus is where a task stands. Completed and Skipped are the operator's
// resolutions; Missed and Withdrawn are markers of the system and are not.
type TaskStatus string

// The statuses of a task.
const (
	Open      TaskStatus = "open"
	Completed TaskStatus = "completed"
	Skipped   TaskStatus = "skipped"
	Missed    TaskStatus = "missed"
	Withdrawn TaskStatus = "withdrawn"
)

// Task is one task of one period, as its materialization snapshot says and as
// the live events since have left it. ResolvedAt and Reason are set only by a
// resolution: the instant the task was completed or skipped, and the reason
// for a skip.
type Task struct {
	ID          event.TaskID
	Definition  string
	Cadence     due.Cadence
	PeriodStart civil.Date
	Title       string
	Group       string
	Link        string
	Due         time.Time
	DueRule     due.Rule
	Status      TaskStatus
	ResolvedAt  *time.Time
	Reason      string
}

// Task returns the task with the given id.
func (m *Model) Task(id event.TaskID) (Task, bool) {
	i, ok := m.taskIndex[id]
	if !ok {
		return Task{}, false
	}
	return m.tasks[i], true
}

// Tasks lists every task, in the order the log materialized them. The caller
// owns the slice.
func (m *Model) Tasks() []Task { return slices.Clone(m.tasks) }

// taskEvents are the live events of one task, each list in event order.
type taskEvents struct {
	id          event.TaskID
	materialize []liveEvent
	resolutions []liveEvent // task.completed and task.skipped
	missed      []liveEvent
	chain       []liveEvent // task.withdrawn and task.reinstated
}

func groupTasks(live []liveEvent) []*taskEvents {
	var out []*taskEvents
	byID := map[event.TaskID]*taskEvents{}
	get := func(id event.TaskID) *taskEvents {
		if t, ok := byID[id]; ok {
			return t
		}
		t := &taskEvents{id: id}
		byID[id] = t
		out = append(out, t)
		return t
	}
	for _, e := range live {
		switch p := e.Payload.(type) {
		case event.TaskMaterialized:
			t := get(p.TaskID)
			t.materialize = append(t.materialize, e)
		case event.TaskCompleted:
			t := get(p.TaskID)
			t.resolutions = append(t.resolutions, e)
		case event.TaskSkipped:
			t := get(p.TaskID)
			t.resolutions = append(t.resolutions, e)
		case event.TaskMissed:
			t := get(p.TaskID)
			t.missed = append(t.missed, e)
		case event.TaskWithdrawn:
			t := get(p.TaskID)
			t.chain = append(t.chain, e)
		case event.TaskReinstated:
			t := get(p.TaskID)
			t.chain = append(t.chain, e)
		}
	}
	for _, t := range out {
		t.materialize = sorted(t.materialize)
		t.resolutions = sorted(t.resolutions)
		t.chain = sorted(t.chain)
	}
	return out
}

// kindOf is the kind of period a cadence's tasks belong to.
func kindOf(c due.Cadence) Kind {
	switch c {
	case due.Daily:
		return Day
	case due.Weekly:
		return Week
	case due.Sprint:
		return Sprint
	}
	return ""
}

// resolved says how a resolution reads in a sentence.
func resolved(e liveEvent) string {
	if e.Payload.EventType() == event.TypeTaskSkipped {
		return "skipped"
	}
	return "completed"
}

// projectTasks checks the live task events against the periods and the
// profiles and returns the tasks in materialization order.
//
// Rule: a task is resolved by its live completion or skip, which wins over
// everything; otherwise the end of its withdraw and reinstate chain decides
// withdrawn; otherwise a live missed gives missed; otherwise it is open. A
// missed or withdrawn marker is exempt from every ordering check, which is why
// a late completion of a missed task is accepted whatever its effective_at.
func (r *run) projectTasks(live []liveEvent, periods []liveEvent, profiles []liveEvent) ([]Task, error) {
	starts := map[Kind][]civil.Date{}
	for _, e := range periods {
		p := e.Payload.(event.PeriodChanged)
		starts[Kind(p.Kind)] = append(starts[Kind(p.Kind)], p.Start)
	}

	groups := groupTasks(live)
	var materialized []*taskEvents
	for _, t := range groups {
		if inv := r.checkTask(t, starts, profiles); inv != nil {
			return nil, inv
		}
		if len(t.materialize) > 0 { // markers of a task the log never materialized describe no task
			materialized = append(materialized, t)
		}
	}
	slices.SortStableFunc(materialized, func(a, b *taskEvents) int {
		return cmp.Compare(a.materialize[0].pos, b.materialize[0].pos)
	})
	tasks := make([]Task, len(materialized))
	for i, t := range materialized {
		tasks[i] = r.taskOf(t)
	}
	return tasks, nil
}

// taskOf is the task a checked set of events describes; it has exactly one
// materialization.
func (r *run) taskOf(t *taskEvents) Task {
	m := t.materialize[0].Payload.(event.TaskMaterialized)
	task := Task{
		ID: m.TaskID, Definition: m.Definition, Cadence: m.Cadence, PeriodStart: m.Period,
		Title: m.Title, Group: m.Group, Link: m.Link, Due: m.Due.Time(), DueRule: m.DueRule,
		Status: Open,
	}
	switch {
	case len(t.resolutions) > 0:
		z := t.resolutions[0]
		at := z.EffectiveAt.Time()
		task.ResolvedAt = &at
		task.Status = Completed
		if s, ok := z.Payload.(event.TaskSkipped); ok {
			task.Status, task.Reason = Skipped, s.Reason
		}
	case r.withdrawnAtEnd(t.chain) != nil:
		task.Status = Withdrawn
	case len(t.missed) > 0:
		task.Status = Missed
	}
	return task
}

// withdrawnAtEnd returns the withdrawal that leaves the task withdrawn at the
// end of the chain, or nil when the chain ends reinstated or is empty. A second
// withdrawal of a withdrawn task changes nothing.
func (r *run) withdrawnAtEnd(chain []liveEvent) *liveEvent {
	return r.withdrawnBefore(chain, nil)
}

// withdrawnBefore is withdrawnAtEnd for the part of the chain that sorts before
// z; nil z means all of it.
func (r *run) withdrawnBefore(chain []liveEvent, z *liveEvent) *liveEvent {
	var by *liveEvent
	for i := range chain {
		if z != nil && order(chain[i], *z) >= 0 {
			break
		}
		switch chain[i].Payload.(type) {
		case event.TaskWithdrawn:
			if by == nil {
				by = &chain[i]
			}
		case event.TaskReinstated:
			by = nil
		}
	}
	return by
}

// checkTask reports the first impossible thing the live events of one task
// say. The materialization is checked first, then the chain, then the
// resolutions.
func (r *run) checkTask(t *taskEvents, starts map[Kind][]civil.Date, profiles []liveEvent) *Invalid {
	id := string(t.id)
	if len(t.materialize) > 1 {
		return r.finding(codeTaskMaterializedTwice, id, fmt.Sprintf(
			"Task %s is materialized by %s and again by %s, and a task id has one live materialization",
			id, r.ref(t.materialize[0]), r.ref(t.materialize[1]),
		), t.materialize[0], t.materialize[1])
	}
	if len(t.materialize) == 1 {
		if inv := r.checkMaterialization(t.materialize[0], starts, profiles); inv != nil {
			return inv
		}
	}

	var withdrawn bool
	for _, e := range t.chain {
		switch e.Payload.(type) {
		case event.TaskWithdrawn:
			withdrawn = true
		case event.TaskReinstated:
			if !withdrawn {
				return r.finding(codeTaskNotWithdrawn, id, fmt.Sprintf(
					"Task %s is reinstated by %s although it is not withdrawn at that instant", id, r.ref(e),
				), e)
			}
			withdrawn = false
		}
	}

	if len(t.resolutions) > 1 {
		a, b := t.resolutions[0], t.resolutions[1]
		return r.finding(codeTaskAlreadyResolved, id, fmt.Sprintf(
			"Task %s is %s by %s and %s by %s, and a task is completed or skipped only once",
			id, resolved(a), r.ref(a), resolved(b), r.ref(b),
		), a, b)
	}
	for i := range t.resolutions {
		z := t.resolutions[i]
		switch {
		case len(t.materialize) == 0:
			return r.finding(codeResolutionBeforeMaterialization, id, fmt.Sprintf(
				"Task %s is %s by %s, but no live task.materialized creates it", id, resolved(z), r.ref(z),
			), z)
		case order(z, t.materialize[0]) < 0:
			return r.finding(codeResolutionBeforeMaterialization, id, fmt.Sprintf(
				"Task %s is %s by %s, which takes effect before %s materialized it",
				id, resolved(z), r.ref(z), r.ref(t.materialize[0]),
			), z, t.materialize[0])
		}
		if w := r.withdrawnBefore(t.chain, &z); w != nil {
			return r.finding(codeTaskWithdrawn, id, fmt.Sprintf(
				"Task %s is %s by %s after %s withdrew it, with no reinstatement in between",
				id, resolved(z), r.ref(z), r.ref(*w),
			), *w, z)
		}
	}
	return nil
}

// checkMaterialization checks that a materialization has a profile before it
// and a period of its own.
func (r *run) checkMaterialization(m liveEvent, starts map[Kind][]civil.Date, profiles []liveEvent) *Invalid {
	p := m.Payload.(event.TaskMaterialized)
	id := string(p.TaskID)
	switch {
	case len(profiles) == 0:
		return r.finding(codeTaskBeforeProfile, id, fmt.Sprintf(
			"Task %s is materialized by %s, but no live profile.changed makes a profile active", id, r.ref(m),
		), m)
	case order(m, profiles[0]) < 0:
		return r.finding(codeTaskBeforeProfile, id, fmt.Sprintf(
			"Task %s is materialized by %s, before %s, the first profile change", id, r.ref(m), r.ref(profiles[0]),
		), m, profiles[0])
	}
	if k := kindOf(p.Cadence); !slices.Contains(starts[k], p.Period) {
		return r.finding(codeTaskWithoutPeriod, id, fmt.Sprintf(
			"Task %s is materialized by %s for the %s period starting %s, but no live period.changed starts that period",
			id, r.ref(m), k, p.Period,
		), m)
	}
	return nil
}
