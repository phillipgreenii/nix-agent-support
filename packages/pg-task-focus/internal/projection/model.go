package projection

import (
	"maps"
	"slices"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/zone"
)

// Model is a replayed log. It starts with the overlay's views of the events;
// the later stages of the projection add the state the live events describe.
type Model struct {
	log     []event.Event
	live    []liveEvent
	views   map[event.ID]EventView
	order   []event.ID // the ids that have a view, in log order
	batches map[event.ID][]event.ID

	periods    map[Kind]Period
	profile    string
	tasks      []Task
	taskIndex  map[event.TaskID]int
	cycles     []Cycle
	cycleIndex map[event.CycleID]int
	zone       *zone.Zone // the active day period's zone; nil while no day period exists
}

// newModel runs the overlay over the log and indexes the result. The model
// keeps its own copy of the log's slice.
func newModel(events []event.Event) (*Model, error) {
	return newModelFrom(events, len(events))
}

// newModelFrom is newModel for a log whose events from firstAdded on are the
// ones a request would add.
func newModelFrom(events []event.Event, firstAdded int) (*Model, error) {
	live, views, err := overlayFrom(events, firstAdded)
	if err != nil {
		return nil, err
	}
	m := &Model{
		log:     slices.Clone(events),
		live:    live,
		views:   views,
		batches: map[event.ID][]event.ID{},
	}
	for _, e := range events {
		if e.Payload.EventType() == event.TypeBatchCommitted {
			continue // the commit marker is listed nowhere: it is no event of the batch
		}
		m.order = append(m.order, e.ID)
		if b := e.Payload.BatchID(); b != "" {
			m.batches[b] = append(m.batches[b], e.ID)
		}
	}
	return m, nil
}

// Lines is the number of events in the log the model was built from.
func (m *Model) Lines() int { return len(m.log) }

// Events lists the views of the logged events matching q, in log order. Both
// views include retracted events, flagged; neither lists batch.committed. The
// caller owns the result and each CorrectedBy, but the events in a view share
// their Data and the slices and maps of their payloads with the model, which
// MUST NOT be modified.
func (m *Model) Events(q EventQuery) []EventView {
	out := make([]EventView, 0, len(m.order))
	for _, id := range m.order {
		if v := m.views[id]; q.matches(v) {
			out = append(out, v.clone())
		}
	}
	return out
}

// Event returns the view of one logged event, shared as Events says.
func (m *Model) Event(id event.ID) (EventView, bool) {
	v, ok := m.views[id]
	return v.clone(), ok
}

// BatchEvents lists, in log order, the events that are members of batch b,
// read through each payload's BatchID. The commit marker and the retraction
// that names the batch are not members.
func (m *Model) BatchEvents(b event.ID) []event.ID {
	return slices.Clone(m.batches[b])
}

// Domain is the state a log describes, by value: the profile, the current
// periods, the tasks and the cycles, without the log, the log positions, the
// views or the batches. Two logs that reach the same state by different
// histories, one of them through corrections or retractions, have equal
// domains.
type Domain struct {
	Profile string
	Periods map[Kind]Period
	Tasks   []Task
	Cycles  []Cycle
}

// Domain returns the state the model describes, as a copy.
func (m *Model) Domain() Domain {
	return Domain{Profile: m.profile, Periods: maps.Clone(m.periods), Tasks: m.Tasks(), Cycles: m.Cycles()}
}

// NewestEvent returns the live event about entity, a task or cycle id or a
// period kind as an Invalid names it, that sorts last in the entity's order:
// the latest effective_at, ties going to the later log position. The event is
// as corrected. A cycle.started that names a cycle in interrupts is an event
// of that cycle too, since it pauses it. False means no live event is about
// the entity. The event shares its Data and payload with the model, as Log
// says.
func (m *Model) NewestEvent(entity string) (event.Event, bool) {
	var newest *liveEvent
	for i := range m.live {
		e := &m.live[i]
		about := entityOf(e.Payload) == entity
		if p, ok := e.Payload.(event.CycleStarted); ok && string(p.Interrupts) == entity {
			about = true
		}
		if about && (newest == nil || order(*e, *newest) > 0) {
			newest = e
		}
	}
	if newest == nil {
		return event.Event{}, false
	}
	return newest.Event, true
}
