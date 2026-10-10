package rules

import (
	"errors"
	"fmt"
	"strings"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// focus.item is the one rule behind the daily focus bead (daily-focus design
// section 8; docs/behavior/pg-decider/work-items.md "Focus bead"). It covers
// four transitions for the entity types `issue` and `pr`, all from the view
// of the SOURCE entity alone:
//
//   - mint: annotations.focus_selected is a real period key and no focus bead
//     is linked: ONE create of the bead shape (D-F13). No bead is minted for an
//     issue whose id matches bead_id_pattern (an epic or a plain bd task), nor
//     for a terminal source;
//   - hold: the annotation reads none (null and "" read the same) and the
//     linked bead is exactly `open`, unclaimed and not already held, or the
//     source is terminal and the bead is open and unclaimed: ONE update
//     (status deferred, no end date, metadata focus_hold=struck);
//   - release: the annotation is a real period key and the bead is HELD
//     (status deferred, focus_hold=struck, empty assignee) and the source is
//     live: ONE update (status open, deferral cleared, focus_hold=released);
//     an open unassigned bead carrying a stale `struck` marker whose item is
//     selected gets a metadata-only update rewriting it to `released`;
//   - hold_terminal: the hold of a terminal source (a merged or closed PR, a
//     done issue). A reselect of a terminal source holds, never releases.
//
// Every other state is a skip with a reason from the closed vocabulary of the
// bead-state table (section 8.3). The rule never emits close or reopen and
// never reads who closed an item or why (S26). The "no open children"
// condition of a hold and the live re-read are the apply step's, not this
// rule's. An absent annotations.focus_selected member plans nothing here; the
// run fails closed in CheckFocusView, before any rule is evaluated.

const (
	focusRuleID = "focus.item"

	// focusMarkerKey is the bead metadata key the decider owns.
	focusMarkerKey      = "focus_hold"
	focusMarkerStruck   = "struck"
	focusMarkerReleased = "released"

	focusStatusOpen     = "open"
	focusStatusDeferred = "deferred"
	focusStatusProgress = "in_progress"

	// FocusAbsentReason is the counted failure reason of a run whose view
	// lacks annotations.focus_selected.
	FocusAbsentReason = "view-lacks-focus_selected"
)

// The transitions named in facts.transition.
const (
	FocusTransitionMint         = "mint"
	FocusTransitionHold         = "hold"
	FocusTransitionRelease      = "release"
	FocusTransitionHoldTerminal = "hold_terminal"
)

// Why a focus skip is a skip: facts.cause, one value per row of the bead-state
// table (section 8.3) that plans nothing.
const (
	focusCauseNoSelection     = "not-selected"
	focusCauseMalformedKey    = "malformed-key"
	focusCauseSourceIsBead    = "source-is-bead"
	focusCauseSourceTerminal  = "source-terminal"
	focusCauseBeadPatternBad  = "bead-pattern-invalid"
	focusCauseInPlay          = "in-play"
	focusCauseClaimed         = "claimed"
	focusCauseHeld            = "held"
	focusCauseDeferredOther   = "deferred-by-someone-else"
	focusCauseOtherStatus     = "status-not-open"
	focusCauseClosed          = "closed"
	focusCauseHeldSourceFinal = "held-source-terminal"
)

// focusEntityTypes are the entity types focus.item is registered for.
var focusEntityTypes = []string{decide.EntityTypeIssue, decide.EntityTypePR}

// ErrFocusSelectedAbsent is the error CheckFocusView returns: the view lacks
// the annotations.focus_selected member (an older pg-desk, or a rollback), so
// "none" cannot be told from "unknown" and a strike could not be trusted.
var ErrFocusSelectedAbsent = errors.New("the view lacks annotations.focus_selected (" + FocusAbsentReason + ")")

// CheckFocusView fails a run closed when the view of an entity type the focus
// rule serves lacks annotations.focus_selected. A present null is fine: it
// means unset. plan and apply call it before any rule or hook runs, so
// nothing is written. It returns nil for a nil view and for any other type.
func CheckFocusView(v *view.View, entityType string) error {
	if v == nil {
		return nil
	}
	for _, t := range focusEntityTypes {
		if t == entityType {
			if !v.Annotations.FocusSelected.Present {
				return ErrFocusSelectedAbsent
			}
			return nil
		}
	}
	return nil
}

func init() {
	r := focusItemRule{}
	decide.Register(decide.EntityTypeIssue, decide.OrdinalFocusItem, r)
	decide.Register(decide.EntityTypePR, decide.OrdinalFocusItem, r)
}

// focusItemRule is the focus.item rule.
type focusItemRule struct{}

func (focusItemRule) ID() string { return focusRuleID }

func (focusItemRule) Kind() workitem.Kind { return workitem.KindFocusItem }

func (focusItemRule) Evaluate(in decide.Input) decide.Result {
	v := in.View
	bead, hasBead := in.Items.Find(workitem.KindFocusItem, workitem.Context{})

	period, selected := v.Annotations.FocusSelected.Selected()
	if !selected && !focusIsStrike(v.Annotations.FocusSelected) {
		// Absent (the run fails closed elsewhere) or a malformed key.
		cause := focusCauseMalformedKey
		if !v.Annotations.FocusSelected.Present {
			cause = focusCauseNoSelection
		}
		return focusSkip(action.ReasonNotMatched, cause, bead, hasBead, nil)
	}
	terminal := focusSourceTerminal(v)

	if !hasBead {
		return focusNoBead(in, period, selected, terminal)
	}
	return focusWithBead(v, bead, selected, terminal)
}

// focusIsStrike reports whether the annotation reads "not selected": null,
// "none" or the empty string. An absent member and a malformed key are not
// strikes.
func focusIsStrike(f view.FocusSelected) bool {
	if !f.Present {
		return false
	}
	return !f.Set || f.Value == "" || f.Value == "none"
}

func focusNoBead(in decide.Input, period string, selected, terminal bool) decide.Result {
	v := in.View
	if !selected {
		return focusSkip(action.ReasonNotMatched, focusCauseNoSelection, workitem.Item{}, false, nil)
	}
	if terminal {
		return focusSkip(action.ReasonNotMatched, focusCauseSourceTerminal, workitem.Item{}, false, nil)
	}
	if v.Type == decide.EntityTypeIssue {
		re, err := in.Config.BeadIDRegexp()
		if err != nil {
			return focusSkip(action.ReasonNotMatched, focusCauseBeadPatternBad, workitem.Item{}, false, nil)
		}
		if re != nil && re.MatchString(v.ID) {
			return focusSkip(action.ReasonNotMatched, focusCauseSourceIsBead, workitem.Item{}, false, nil)
		}
	}
	return decide.Result{Actions: []action.Action{focusMint(in, period)}}
}

func focusWithBead(v *view.View, bead workitem.Item, selected, terminal bool) decide.Result {
	skip := func(cause string) decide.Result {
		return focusSkip(action.ReasonAlreadyHandled, cause, bead, true, nil)
	}
	switch {
	case !bead.Open():
		return skip(focusCauseClosed)
	case focusClaimed(bead):
		return skip(focusCauseClaimed)
	case focusHeld(bead):
		if selected && !terminal {
			return focusUpdate(bead, FocusTransitionRelease, action.Fields{
				Status:     focusStatusOpen,
				ClearDefer: true,
				Metadata:   map[string]string{focusMarkerKey: focusMarkerReleased},
			}, v)
		}
		if terminal {
			return skip(focusCauseHeldSourceFinal)
		}
		return skip(focusCauseHeld)
	case bead.State == focusStatusDeferred:
		return skip(focusCauseDeferredOther)
	case bead.State != focusStatusOpen:
		return skip(focusCauseOtherStatus)
	}

	// An open, unclaimed bead.
	switch {
	case terminal:
		return focusUpdate(bead, FocusTransitionHoldTerminal, focusHoldFields(), v)
	case !selected:
		return focusUpdate(bead, FocusTransitionHold, focusHoldFields(), v)
	case bead.Metadata[focusMarkerKey] == focusMarkerStruck:
		return focusUpdate(bead, FocusTransitionRelease, action.Fields{
			Metadata: map[string]string{focusMarkerKey: focusMarkerReleased},
		}, v, "marker_only")
	}
	return skip(focusCauseInPlay)
}

func focusHoldFields() action.Fields {
	return action.Fields{
		Status:   focusStatusDeferred,
		Metadata: map[string]string{focusMarkerKey: focusMarkerStruck},
	}
}

// focusClaimed: a bead is claimed when its state is in_progress or its
// assignee is non-empty.
func focusClaimed(b workitem.Item) bool {
	return b.State == focusStatusProgress || b.Assignee != ""
}

// focusHeld: a bead is held only when ALL THREE hold: status deferred,
// focus_hold=struck and an empty assignee. The marker alone never makes a
// bead held.
func focusHeld(b workitem.Item) bool {
	return b.State == focusStatusDeferred &&
		b.Metadata[focusMarkerKey] == focusMarkerStruck &&
		b.Assignee == ""
}

func focusSkip(reason, cause string, bead workitem.Item, hasBead bool, extra map[string]any) decide.Result {
	facts := map[string]any{"cause": cause}
	if hasBead {
		facts["bead"] = map[string]any{"id": bead.ID, "state": bead.State}
	}
	for k, val := range extra {
		facts[k] = val
	}
	return decide.Result{Skip: &action.Skip{Reason: reason, Facts: facts}}
}

// focusUpdate is the single update of a hold or a release. flags are extra
// boolean facts (only "marker_only" today).
func focusUpdate(bead workitem.Item, transition string, f action.Fields, v *view.View, flags ...string) decide.Result {
	target := bead.ID
	facts := map[string]any{
		"transition":  transition,
		"bead_state":  bead.State,
		"source_type": v.Type,
		"source_id":   v.ID,
	}
	for _, fl := range flags {
		facts[fl] = true
	}
	return decide.Result{Actions: []action.Action{{
		Op:     action.OpUpdate,
		Kind:   string(workitem.KindFocusItem),
		Target: &target,
		Fields: f,
		Facts:  facts,
	}}}
}

// focusMint is the create of the focus bead shape (section 8.1).
func focusMint(in decide.Input, period string) action.Action {
	v := in.View
	ref, title, url, srcPriority, srcIssueType := focusSource(v)
	contract := workitem.ContractFor(workitem.KindFocusItem)
	key := workitem.DedupKey(workitem.EntityRefFrom(v), workitem.KindFocusItem, workitem.Context{})

	f := action.Fields{
		Title:       focusTitle(ref, title),
		IssueType:   contract.IssueTypeFor(srcIssueType),
		Description: focusDescription(v.Type, ref, url),
		Priority:    in.Config.FocusPriority(srcPriority),
		Labels:      contract.Labels,
		Metadata: map[string]string{
			"source_type": v.Type,
			"source_id":   v.ID,
			"dedup_key":   key,
		},
	}
	return action.Action{
		Op:     action.OpCreate,
		Kind:   string(workitem.KindFocusItem),
		Fields: f,
		Facts: map[string]any{
			"transition":  FocusTransitionMint,
			"source_type": v.Type,
			"source_id":   v.ID,
			"period":      period,
		},
	}
}

// focusSource reads what the bead shape needs from the source's snapshot: the
// reference the title uses, the source's title and URL, its tracker priority
// name and its tracker issue type. A PR has no priority or issue type.
func focusSource(v *view.View) (ref, title, url, priority, issueType string) {
	if v.Type == decide.EntityTypePR {
		s := v.Snapshot
		ref = v.ID
		if s.Repo != "" && s.Number > 0 {
			ref = fmt.Sprintf("%s#%d", s.Repo, s.Number)
		}
		return ref, s.Title, s.URL, "", ""
	}
	s := v.IssueSnapshot
	ref = v.ID
	if s.ID != "" {
		ref = s.ID
	}
	return ref, s.Title, s.URL, s.Priority, s.IssueType
}

// focusOneLine collapses every run of whitespace, newlines included, to one
// space, so a title or description is one line.
func focusOneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// focusTitle is "Focus <source ref> - <source title>"; with no source title
// it is "Focus <source ref>". It starts with "Focus", never with "<ref>: ",
// which the anchor adoption match would read as the PR's own anchor bead.
func focusTitle(ref, title string) string {
	ref, title = focusOneLine(ref), focusOneLine(title)
	if title == "" {
		return "Focus " + ref
	}
	return "Focus " + ref + " - " + title
}

// focusDescription is one line naming the source entity.
func focusDescription(typ, ref, url string) string {
	d := "Focus item for " + typ + " " + focusOneLine(ref)
	if u := focusOneLine(url); u != "" {
		d += " (" + u + ")"
	}
	return d
}

// focusIssueDoneStates are the issue states read as terminal when the tracker
// reports no status category (always for beads, absent on older Jira
// snapshots): the same set pg-desk's classifier uses.
var focusIssueDoneStates = map[string]bool{
	"closed": true, "done": true, "resolved": true,
	"cancelled": true, "canceled": true, "wontfix": true,
}

// focusSourceTerminal is the decider-side TERMINAL predicate: a PR source is
// terminal when anchorPRState reports merged or closed; an issue source when
// status_category is "done" or, when the category is empty, when its state is
// one of focusIssueDoneStates.
func focusSourceTerminal(v *view.View) bool {
	if v.Type == decide.EntityTypePR {
		st := anchorPRState(v.Snapshot)
		return st == anchorPRStateClosed || st == anchorPRStateMerged
	}
	s := v.IssueSnapshot
	if s.StatusCategory != "" {
		return strings.EqualFold(s.StatusCategory, "done")
	}
	return focusIssueDoneStates[strings.ToLower(strings.TrimSpace(s.State))]
}
