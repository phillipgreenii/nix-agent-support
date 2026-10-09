package projection

import (
	"slices"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// Model is a replayed log. It starts with the overlay's views of the events;
// the later stages of the projection add the state the live events describe.
type Model struct {
	log     []event.Event
	live    []liveEvent
	views   map[event.ID]EventView
	order   []event.ID // the ids that have a view, in log order
	batches map[event.ID][]event.ID
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

// Events lists the views of the logged events matching q, in log order. Both
// views include retracted events, flagged; neither lists batch.committed.
func (m *Model) Events(q EventQuery) []EventView {
	out := make([]EventView, 0, len(m.order))
	for _, id := range m.order {
		if v := m.views[id]; q.matches(v) {
			out = append(out, v)
		}
	}
	return out
}

// Event returns the view of one logged event.
func (m *Model) Event(id event.ID) (EventView, bool) {
	v, ok := m.views[id]
	return v, ok
}

// BatchEvents lists, in log order, the events that are members of batch b,
// read through each payload's BatchID. The commit marker and the retraction
// that names the batch are not members.
func (m *Model) BatchEvents(b event.ID) []event.ID {
	return slices.Clone(m.batches[b])
}
