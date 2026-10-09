package projection

import (
	"slices"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// Candidate replays the log base followed by the events add, which a request
// would append. It is the validation every mutation, correction, retraction
// and client effective_at goes through before anything is written: a nil error
// means the whole timeline is possible, and the model returned is the one to
// adopt once the events are stored, so no second replay is needed. The error is
// the *Invalid of the replay, unchanged.
//
// The events are replayed in full; there is no incremental path. Neither base
// nor add is written to: the candidate log is a fresh slice, and the added
// events take the log positions that follow the base, Line len(base)+i+1.
// A finding calls the added events "the new event" and a cycle they start "the
// new cycle", and never lists them.
func Candidate(base, add []event.Event) (*Model, error) {
	events := make([]event.Event, 0, len(base)+len(add))
	events = append(events, base...)
	events = append(events, add...)
	for i := len(base); i < len(events); i++ {
		events[i].Line = i + 1
	}
	return replay(events, len(base))
}

// Log is the raw log the model was built from, in order, with the log
// position of each event. The caller owns the slice, but each event's Data and
// the slices and maps of its payload are shared with the model and MUST NOT be
// modified.
func (m *Model) Log() []event.Event { return slices.Clone(m.log) }
