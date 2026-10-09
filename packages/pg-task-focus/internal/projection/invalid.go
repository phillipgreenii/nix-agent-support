// Package projection replays the event log into the state the clients show:
// first the overlay that applies corrections and retractions, then the
// periods, tasks and cycles the live events describe.
package projection

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// Code names one condition a replay can find in a log. The set is closed; each
// stage of the projection declares the codes it can find.
type Code string

// The codes the overlay finds.
const (
	codeUnknownEvent      Code = "unknown_event"
	codeInvalidCorrection Code = "invalid_correction"
)

// Codes lists every code declared so far. The caller owns the slice.
func Codes() []Code {
	return []Code{codeUnknownEvent, codeInvalidCorrection}
}

// Invalid is the finding of a replay: the log, or the log plus the events a
// request would add, describes an impossible timeline. Message is one plain
// sentence naming the entity, the instants and the ids of stored events; the
// event a request would add is called "the new event" there and is never
// listed in Events, and a cycle it would create is never listed in Cycles.
// Instants always includes the new event's effective_at. Entity is a stored
// task or cycle id or a period kind, empty when the offender is the new cycle
// itself.
type Invalid struct {
	Code     Code
	Message  string
	Entity   string
	Events   []event.ID
	Cycles   []event.CycleID
	Instants []time.Time
}

// Error implements error.
func (i *Invalid) Error() string { return i.Message }
