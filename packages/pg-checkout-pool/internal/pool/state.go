// Package pool is the slot store of pg-checkout-pool: per-slot exclusive
// locks, atomic lease records and the slot state machine. It performs no git
// operations; a provisioner supplied by a later change drives the resets.
package pool

import (
	"fmt"
	"time"
)

// State is the lifecycle state of one slot.
type State string

// The slot states. A slot is created WarmFree, leased through Resetting (the
// reset to the requested commit happens before hand-out) to Leased, and goes
// back to WarmFree through Resetting on release. Quarantined takes a slot out
// of rotation with its directory intact; Retired is terminal.
const (
	WarmFree    State = "warm-free"
	Leased      State = "leased"
	Resetting   State = "resetting"
	Quarantined State = "quarantined"
	Retired     State = "retired"
)

// transitions is the complete set of legal state changes.
var transitions = map[State][]State{
	WarmFree:    {Resetting, Quarantined, Retired},
	Resetting:   {Leased, WarmFree, Quarantined},
	Leased:      {Resetting, Quarantined},
	Quarantined: {Resetting, Retired},
	Retired:     {},
}

// CanTransition reports whether from -> to is a legal state change.
func CanTransition(from, to State) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Valid reports whether s is one of the five defined states.
func (s State) Valid() bool {
	_, ok := transitions[s]
	return ok
}

// TransitionError is returned for an illegal state change.
type TransitionError struct {
	Slot     string
	From, To State
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("slot %s: illegal transition %s -> %s", e.Slot, e.From, e.To)
}

// Clock is the time source; tests inject a fake.
type Clock interface{ Now() time.Time }

// SystemClock is the real clock.
type SystemClock struct{}

// Now returns the wall-clock time.
func (SystemClock) Now() time.Time { return time.Now() }
