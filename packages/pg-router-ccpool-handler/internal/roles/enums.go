package roles

import "fmt"

// Completion selects the bead-done semantics for a ccpool role. Go owns each value's
// implementation (incl. the seenClaimed startup-race guard for close-or-handback).
type Completion string

const (
	CloseOnly       Completion = "close-only"
	CloseOrHandback Completion = "close-or-handback"
)

func (c *Completion) UnmarshalText(b []byte) error {
	switch Completion(b) {
	case CloseOnly, CloseOrHandback:
		*c = Completion(b)
		return nil
	}
	return fmt.Errorf("invalid completion %q (valid: close-only, close-or-handback)", b)
}

// FailureAction is what to do to the bead when a dispatch is flagged.
type FailureAction string

const (
	Unclaim  FailureAction = "unclaim"
	AddHuman FailureAction = "add-human"
)

func (a *FailureAction) UnmarshalText(b []byte) error {
	switch FailureAction(b) {
	case Unclaim, AddHuman:
		*a = FailureAction(b)
		return nil
	}
	return fmt.Errorf("invalid on_failure %q (valid: unclaim, add-human)", b)
}

// DispatchFailAction is what to do when the nudge could not be SENT.
type DispatchFailAction string

const (
	DispatchUnclaim DispatchFailAction = "unclaim"
	DispatchLeave   DispatchFailAction = "leave"
)

func (d *DispatchFailAction) UnmarshalText(b []byte) error {
	switch DispatchFailAction(b) {
	case DispatchUnclaim, DispatchLeave:
		*d = DispatchFailAction(b)
		return nil
	}
	return fmt.Errorf("invalid on_dispatch_fail %q (valid: unclaim, leave)", b)
}
