package focus

import (
	"fmt"
	"strings"
	"time"
)

// The bead column: a closed vocabulary (D-F20) that names, for each row of
// a focus table, what its linked bead is doing, so "pending" never hides a
// reason. BeadStates is the ONE Go table of the spec's state-by-outcome table
// (section 8.3): `focus show` reads its Shown column, the select echo reads
// the strike and select columns, and the decider's parity test compares the
// skip-reason column. The same table is written into
// docs/behavior/pg-desk/focus.md, and a test keeps the two equal.
//
// Telemetry (D24): this file emits no OpenTelemetry or Prometheus signal and
// logs nothing; it is a pure table and a pure classifier.

// BeadState names one row of the state-by-outcome table.
type BeadState string

// The rows of the table, in table order.
const (
	StatePending          BeadState = "pending"
	StatePendingHidden    BeadState = "pending_hidden"
	StatePendingSuppress  BeadState = "pending_suppressed"
	StateSourceDoneNoBead BeadState = "source_done_no_bead"
	StateNotApplicable    BeadState = "not_applicable"
	StateBeadHidden       BeadState = "bead_hidden"
	StateBeadSuppress     BeadState = "bead_suppressed"
	StateOpen             BeadState = "open"
	StateOpenChildren     BeadState = "open_has_children"
	StateOpenMarkerStale  BeadState = "open_marker_stale"
	StateClaimed          BeadState = "claimed"
	StateHeld             BeadState = "held"
	StateDeferredOther    BeadState = "deferred_by_someone_else"
	StateStatusWord       BeadState = "status_word"
	StateClosed           BeadState = "closed"
	StateSourceDone       BeadState = "source_done"
	StateHoldStale        BeadState = "hold_stale"
	StateEscalated        BeadState = "escalated"
	StateUnwatched        BeadState = "unwatched"
)

// holdMarkerStruck and holdMarkerReleased are the values of the decider's
// hold marker (the focus_hold metadata of a minted bead) as pg-desk reads
// them. pg-desk never writes the marker.
const (
	holdMarkerStruck   = "struck"
	holdMarkerReleased = "released"
)

// Skip reasons of the decider's closed vocabulary, plus the apply outcome
// skipped-stale. ClassifyBead's table carries them; they are words, not logic.
const (
	skipNotMatched     = "not matched"
	skipHidden         = "hidden"
	skipSuppressed     = "suppressed"
	skipAlreadyHandled = "already handled"
	skipSkippedStale   = "skipped-stale"
	skipNA             = "n/a"
)

// LinkedBead is the bead the store links to a source by relation source,
// read from the store with no tracker call. Present is false when the source
// has no minted bead yet (PendingAge is then how long the selection has
// waited, which the "pending <age>" word shows). FocusHold is the value of
// the bead's hold marker ("" when absent).
type LinkedBead struct {
	Present      bool
	Status       string
	Assignee     string
	FocusHold    string
	OpenChildren bool
	PendingAge   time.Duration
}

// SourceFlags are the facts about the SOURCE entity (the row of the table)
// the classification needs. Hidden is the source's hidden annotation;
// SuppressedFocusKind is the suppress.<kind> annotation of the configured
// work kind (focus.suppressed_kind); Terminal is a merged or closed PR or a
// done issue; IsBeadItself is a source whose id already is a bead id (an
// epic or a bd task); Unwatched is a source outside every watch query;
// HoldStale and Escalated are the two states the decider reports through
// facts pg-desk does not read this phase and so leave false.
type SourceFlags struct {
	Hidden, SuppressedFocusKind, Terminal, IsBeadItself, Unwatched, HoldStale, Escalated bool
}

// BeadStateRow is one row of the state-by-outcome table.
//
//   - Shown is the closed vocabulary of the bead column ("Shown as").
//   - SkipReason is the decider's skip reason for the row.
//   - StrikeEffect and SelectEffect are the effect on a strike and on a select
//     (mint, hold, release or none) and StrikeBecause and SelectBecause the
//     clause that says why; the echo words are StrikeEcho and SelectEcho,
//     verbatim from the spec table, which is what the select echo prints.
//   - Description is the "Bead state" cell.
type BeadStateRow struct {
	State                               BeadState
	Shown, SkipReason                   string
	StrikeEffect, StrikeBecause         string
	SelectEffect, SelectBecause         string
	StrikeEcho, SelectEcho, Description string
}

// BeadStates is the state-by-outcome table, one row per row of spec section
// 8.3, in table order.
var BeadStates = []BeadStateRow{
	{StatePending, "pending", skipNotMatched, "none", "", "mint", "", "nothing to do", "will mint", "No bead yet, decider not run"},
	{StatePendingHidden, "pending (hidden)", skipHidden, "none", "", "none", "source hidden", "nothing to do", "no change (source hidden)", "No bead yet, source hidden"},
	{StatePendingSuppress, "pending (suppressed)", skipSuppressed, "none", "", "none", "suppressed", "nothing to do", "no change (suppressed)", "No bead yet, kind suppressed"},
	{StateSourceDoneNoBead, "source done (no bead)", skipNotMatched, "none", "", "none", "source finished", "nothing to do", "no change (source finished)", "No bead yet, source terminal (finished)"},
	{StateNotApplicable, "n/a (epic or bead)", skipNA, "none", "no bead", "none", "no bead", "no bead", "no bead", "Source is an epic or a bd task (its id is a bead id)"},
	{StateBeadHidden, "the bead word (source hidden)", skipHidden, "none", "source hidden", "none", "source hidden", "no change (source hidden)", "no change (source hidden)", "A bead exists (any bead state), source hidden"},
	{StateBeadSuppress, "the bead word (source suppressed)", skipSuppressed, "none", "source suppressed", "none", "source suppressed", "no change (source suppressed)", "no change (source suppressed)", "A bead exists (any bead state), kind suppressed"},
	{StateOpen, "open", skipAlreadyHandled, "hold", "", "none", "bead open", "will hold", "bead open, no change", "open, unclaimed, no marker or marker released"},
	{StateOpenChildren, "open (has children)", skipAlreadyHandled, "none", "has open children", "none", "bead open", "no change (has open children)", "bead open, no change", "open, unclaimed, no marker or marker released, with open children"},
	{StateOpenMarkerStale, "open (marker stale)", skipAlreadyHandled, "hold", "", "none", "will rewrite the marker", "will hold", "will rewrite the marker", "open, unclaimed, marker struck (undeferred by hand)"},
	{StateClaimed, "claimed", skipAlreadyHandled, "none", "claimed, left running", "none", "claimed", "claimed by <actor>, left running", "claimed by <actor>, no change", "in_progress or assigned"},
	{StateHeld, "held", skipAlreadyHandled, "none", "already held", "release", "", "no change (already held)", "held, will release", "deferred, marker struck, unassigned (held)"},
	{StateDeferredOther, "deferred by someone else", skipAlreadyHandled, "none", "", "none", "deferred by someone else", "no change", "deferred by someone else, no change", "deferred, no marker (parked by someone else)"},
	{StateStatusWord, "the status word", skipAlreadyHandled, "none", "", "none", "", "no change", "no change", "blocked, pinned or hooked"},
	{StateClosed, "closed", skipAlreadyHandled, "none", "", "none", "worker-closed", "no change", "worker-closed, no change", "closed"},
	{StateSourceDone, "source done", skipAlreadyHandled, "hold", "", "hold", "source done", "will hold", "will hold (source done)", "Source terminal (merged or closed PR, done issue), bead unclaimed"},
	{StateHoldStale, "hold stale", skipSkippedStale, "none", "changed since read", "none", "changed since read", "no change (changed since read)", "no change (changed since read)", "A hold failed or was abandoned after the live re-read"},
	{StateEscalated, "escalated", skipNA, "none", "", "none", "", "unchanged", "unchanged", "An escalated human sibling exists (INV-DECIDER-18)"},
	{StateUnwatched, "unwatched (added to above)", skipNA, "none", "", "none", "", "unchanged", "unchanged", "Source outside every watch query"},
}

// RowOf returns the table row of state; the zero row for an unknown state.
func RowOf(state BeadState) BeadStateRow {
	for _, r := range BeadStates {
		if r.State == state {
			return r
		}
	}
	return BeadStateRow{}
}

// unwatchedWord is appended to the shown word of any row whose source is
// outside every watch query ("unwatched (added to above)").
const unwatchedWord = "unwatched"

// ClassifyBead returns the table row a source and its linked bead fall in.
// The checks run in the order that makes the rows disjoint: a source that is
// itself a bead is not applicable; an escalation and a stale hold are the
// decider's own reports and win over the bead's status; a source with no
// bead yet is pending (or hidden, suppressed or finished); a closed bead is
// closed; a claimed bead (state in_progress or a non-empty assignee) is
// claimed whatever else is true; a deferred bead is held (marker struck and
// unassigned) or deferred by someone else; blocked, pinned and hooked read as
// their status word; an open unclaimed bead is source done when the source
// is terminal, else has-children, marker-stale or plain open. A hidden or
// kind-suppressed source with a bead is its own row (the decider skips every
// rule for it and leaves the bead as it was), whatever the bead's state, so
// the check follows the escalation and precedes the bead's status. Unwatched is
// not a state of its own here: ShownAs appends it to whichever row applies.
func ClassifyBead(link LinkedBead, src SourceFlags) BeadState {
	if src.IsBeadItself {
		return StateNotApplicable
	}
	if src.Escalated {
		return StateEscalated
	}
	if !link.Present {
		switch {
		case src.Hidden:
			return StatePendingHidden
		case src.SuppressedFocusKind:
			return StatePendingSuppress
		case src.Terminal:
			return StateSourceDoneNoBead
		}
		return StatePending
	}
	if src.Hidden {
		return StateBeadHidden
	}
	if src.SuppressedFocusKind {
		return StateBeadSuppress
	}
	if src.HoldStale {
		return StateHoldStale
	}
	return classifyByStatus(link, src)
}

// classifyByStatus classifies a PRESENT bead by its own status, marker and
// the source's terminal state: the rows that do not depend on the source
// being hidden or suppressed.
func classifyByStatus(link LinkedBead, src SourceFlags) BeadState {
	status := strings.ToLower(strings.TrimSpace(link.Status))
	claimed := status == statusInProg || strings.TrimSpace(link.Assignee) != ""
	switch {
	case status == "closed":
		return StateClosed
	case claimed:
		return StateClaimed
	case status == statusDeferred:
		if strings.TrimSpace(link.FocusHold) == holdMarkerStruck {
			return StateHeld
		}
		return StateDeferredOther
	case status == statusBlocked || status == "pinned" || status == "hooked":
		return StateStatusWord
	}
	// open (or an unrecognised status, read as open)
	switch {
	case src.Terminal:
		return StateSourceDone
	case strings.TrimSpace(link.FocusHold) == holdMarkerStruck:
		return StateOpenMarkerStale
	case link.OpenChildren:
		return StateOpenChildren
	}
	return StateOpen
}

// beadWord is the bead's own status word, as the bead column names it when
// the source is hidden or suppressed: "open (source hidden)", "claimed
// (source hidden)", "held (source hidden)". It is the word of the row the
// bead would be in were the source visible, without the qualifiers (children,
// stale marker, source done, the claimant), because the marker already says
// why nothing will change.
func beadWord(link LinkedBead, src SourceFlags) string {
	switch st := classifyByStatus(link, src); st {
	case StateStatusWord:
		return strings.ToLower(strings.TrimSpace(link.Status))
	case StateClosed, StateClaimed, StateHeld, StateDeferredOther:
		return RowOf(st).Shown
	}
	return "open"
}

// ShownAs is the text of the bead column for one row: the "Shown as" word of
// state, with the pending age ("pending 12m") and the claimant of a claimed
// bead filled in, the status word of a blocked, pinned or hooked bead, and
// "unwatched" appended when the source is outside every watch query.
func ShownAs(state BeadState, link LinkedBead, src SourceFlags) string {
	word := RowOf(state).Shown
	switch state {
	case StatePending:
		if link.PendingAge >= time.Minute {
			word = "pending " + shortAge(link.PendingAge)
		}
	case StateBeadHidden:
		word = beadWord(link, src) + " (source hidden)"
	case StateBeadSuppress:
		word = beadWord(link, src) + " (source suppressed)"
	case StateStatusWord:
		word = strings.ToLower(strings.TrimSpace(link.Status))
	case StateClaimed:
		if a := strings.TrimSpace(link.Assignee); a != "" {
			word = "claimed by " + a
		}
	case StateUnwatched:
		return unwatchedWord
	}
	if src.Unwatched && state != StateNotApplicable {
		word += " " + unwatchedWord
	}
	return word
}

// shortAge renders a duration as the coarsest whole unit: 12m, 3h, 2d.
func shortAge(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%ds", int(d/time.Second))
}
