package roles

import "fmt"

// Completion selects the bead-done semantics for a ccpool role. Go owns each value's
// implementation (incl. the seenClaimed startup-race guard for close-or-handback).
type Completion string

const (
	CloseOnly       Completion = "close-only"
	CloseOrHandback Completion = "close-or-handback"
	// CloseOrTriage is for a role that works a bead it does NOT claim (the
	// escalation triager, pg2-2grpj): done on close, on a hand-back, on
	// de-escalation (the `escalated` label removed), or on a new comment
	// since dispatch (a Triage outcome that leaves the bead escalated).
	CloseOrTriage Completion = "close-or-triage"
	// CloseOrSplitTriage is for the split-triage role (pg2-47rsh): it works a
	// needs-split-review bead it does NOT claim. Done on close, or when the
	// `needs-split-review` label is gone (split -> was-split, or not
	// splittable -> human; both remove it). Unlike close-or-triage there is no
	// comment-growth signal and no hand-back reading: the label is the only
	// outcome marker.
	CloseOrSplitTriage Completion = "close-or-split-triage"
	// CloseOrRelease is for a role whose session claims its bead and MAY give it
	// back unfinished (a drain worker: park, defer, convert, refuse a stamp;
	// INV-CCH-28). Done on close, or when the session has ENDED (not merely
	// idle) and the bead is open or deferred and unassigned. Unlike
	// close-or-handback its claim latch is assignee-aware (a peer's claim never
	// sets it), and a session that ended without claiming earns a two-strike
	// unclaimed end instead of counting as a hand-back.
	CloseOrRelease Completion = "close-or-release"
)

func (c *Completion) UnmarshalText(b []byte) error {
	switch Completion(b) {
	case CloseOnly, CloseOrHandback, CloseOrTriage, CloseOrSplitTriage, CloseOrRelease:
		*c = Completion(b)
		return nil
	}
	return fmt.Errorf("invalid completion %q (valid: close-only, close-or-handback, close-or-triage, close-or-split-triage, close-or-release)", b)
}

// Precheck selects the zero-model-cost check a ccpool role runs at dispatch,
// before anything that costs a session (INV-CCH-22). The zero value is "no
// precheck configured": the role named "review" then keeps its historical
// review precheck and every other role is never prechecked.
type Precheck string

const (
	// PrecheckNone (the zero value) configures nothing.
	PrecheckNone Precheck = ""
	// PrecheckReview is the review-pr precheck: bead closed, PR merged, pending
	// review already covers the head.
	PrecheckReview Precheck = "review"
	// PrecheckReady is the precheck for a role that works a bead it claims
	// itself: the bead is still open, unclaimed, groomed, not for a human, not
	// deferred and not blocked (an own-actor in_progress bead always proceeds).
	PrecheckReady Precheck = "ready"
)

func (p *Precheck) UnmarshalText(b []byte) error {
	switch Precheck(b) {
	case PrecheckNone, PrecheckReview, PrecheckReady:
		*p = Precheck(b)
		return nil
	}
	return fmt.Errorf("invalid precheck %q (valid: review, ready, or empty for none)", b)
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
