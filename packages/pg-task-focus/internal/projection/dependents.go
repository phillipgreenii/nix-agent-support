package projection

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// Dependents lists, in log order, the live events that come after batch b's
// first member in the log, are not members of b, and reference an entity b
// created: a task one of its task.materialized events begins. Retracting b
// would leave them about a task that no longer exists, so a retraction of b
// is refused while any is live. A batch that marks a task missed did not
// create it, so a late completion or skip of that task is no dependent, and a
// switch or a break creates nothing. An id that names no batch has none. The
// caller owns the result.
func (m *Model) Dependents(b event.ID) []event.ID {
	created := map[string]bool{}
	first := -1
	for i, e := range m.log {
		if e.Payload.BatchID() != b || e.Payload.EventType() == event.TypeBatchCommitted {
			continue
		}
		if first < 0 {
			first = i
		}
		if p, ok := e.Payload.(event.TaskMaterialized); ok {
			created[string(p.TaskID)] = true
		}
	}
	var out []event.ID
	for _, e := range m.live {
		if e.pos > first && e.Payload.BatchID() != b && created[entityOf(e.Payload)] {
			out = append(out, e.ID)
		}
	}
	return out
}

// CycleDependents lists, in log order, the live events that come after the
// live cycle.started start in the log and need the cycle it begins: every
// event of that cycle, and every cycle.started that names it in interrupts.
// Retracting the start would leave them about a cycle that never started, so
// a retraction of it is refused while any is live. An id that is not a live
// cycle.started has none. The caller owns the result.
func (m *Model) CycleDependents(start event.ID) []event.ID {
	at := -1
	var id event.CycleID
	for _, e := range m.live {
		if p, ok := e.Payload.(event.CycleStarted); ok && e.ID == start {
			at, id = e.pos, p.CycleID
			break
		}
	}
	if at < 0 {
		return nil
	}
	var out []event.ID
	for _, e := range m.live {
		if e.pos <= at {
			continue
		}
		p, interrupts := e.Payload.(event.CycleStarted)
		if entityOf(e.Payload) == string(id) || (interrupts && p.Interrupts == id) {
			out = append(out, e.ID)
		}
	}
	return out
}

// EntityOf is the stored entity an event is about, as a finding names it: a
// task or cycle id, or the kind of a period.changed; empty for an event about
// no entity (a profile.changed, a correction, a retraction, a batch marker).
// A cycle.started that names a cycle in interrupts is about the cycle it
// starts.
func EntityOf(p event.Payload) string { return entityOf(p) }
