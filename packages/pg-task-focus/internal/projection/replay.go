package projection

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// Replay projects a whole log: the overlay of its corrections and retractions,
// then the periods, profile and tasks, then the cycles the live events
// describe. It is a pure function of the events and never reads the config.
// The error is an *Invalid when the timeline is impossible.
func Replay(events []event.Event) (*Model, error) {
	return replay(events, len(events))
}

// replay is Replay for a log whose events from firstAdded on are the ones a
// request would add: a finding never lists them and calls them "the new
// event", and a cycle they would create "the new cycle".
func replay(events []event.Event, firstAdded int) (*Model, error) {
	m, err := newModelFrom(events, firstAdded)
	if err != nil {
		return nil, err
	}
	r := newRun(m.log, firstAdded)
	if err := m.projectCalendar(r); err != nil {
		return nil, err
	}
	if err := m.projectCycles(r); err != nil {
		return nil, err
	}
	return m, nil
}

// projectCalendar sets the periods, the profile and the tasks from the live
// events.
func (m *Model) projectCalendar(r *run) error {
	periods, profiles, err := r.projectPeriods(m.live)
	if err != nil {
		return err
	}
	var changes []liveEvent
	for _, e := range m.live {
		if _, ok := e.Payload.(event.PeriodChanged); ok {
			changes = append(changes, e)
		}
	}
	tasks, err := r.projectTasks(m.live, changes, profiles)
	if err != nil {
		return err
	}
	m.periods, m.tasks = periods, tasks
	if len(profiles) > 0 {
		m.profile = profiles[len(profiles)-1].Payload.(event.ProfileChanged).Profile
	}
	m.taskIndex = make(map[event.TaskID]int, len(tasks))
	for i, t := range tasks {
		m.taskIndex[t.ID] = i
	}
	return nil
}

// projectCycles sets the cycles from the live events. It runs after
// projectCalendar, whose day period gives the zone of its messages.
func (m *Model) projectCycles(r *run) error {
	cycles, err := r.projectCycles(m.live)
	if err != nil {
		return err
	}
	m.cycles = cycles
	m.cycleIndex = make(map[event.CycleID]int, len(cycles))
	for i, c := range cycles {
		m.cycleIndex[c.ID] = i
	}
	return nil
}
