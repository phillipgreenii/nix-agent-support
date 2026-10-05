// Package decide is the pure decision core of pg-decider: a rule registry
// keyed by entity type, the precedence order evaluated before any rule runs,
// and the evaluation loop that turns a composite view into an ordered action
// list plus the list of rules that produced no action, with reasons
// (entity-change-flow design 7.1-7.3).
//
// The core writes nothing and calls nothing: Decide is a pure function of the
// view alone. It does not branch on why pg-router routed the item, which is
// what makes it idempotent, and nothing here reads who closed a work item
// (S26). Concrete rules live in sibling packages and register themselves into
// this registry from init().
package decide

import (
	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// Input is what every rule evaluates. The core builds it once per run, so
// rules never rebuild the work-item index.
type Input struct {
	View       *view.View
	Items      *workitem.Index
	EntityType string
}

// Result is one rule's outcome for one view: zero or more actions, or exactly
// one skip. Skip carries the Reason and Facts only; the core fills Skip.Rule.
// When Actions is non-empty Skip is ignored; when both are empty the rule is
// reported as "not matched".
type Result struct {
	Actions []action.Action
	Skip    *action.Skip
}

// Rule is one decider rule.
type Rule interface {
	// ID is the rule id, e.g. review.head-advanced.
	ID() string
	// Kind is the work-item kind the rule governs, or "" for a rule not tied
	// to one kind (such a rule is never suppressed).
	Kind() workitem.Kind
	// Evaluate computes the rule's outcome from the input alone.
	Evaluate(in Input) Result
}

// Finalizer is optionally implemented by a rule that must adjust the whole
// action list after every rule was evaluated (anchor.lazy inserts the anchor
// create ahead of the child creates that need it). Finalize is called in rule
// order, only for rules that were evaluated (not hidden, not suppressed).
type Finalizer interface {
	Finalize(in Input, produced []action.Action) []action.Action
}

// Curation ordinals: they mirror the row order of the PR rule table (design
// 7.3), in steps of 10. Rule packets register with these constants.
const (
	OrdinalAllClosed             = 10
	OrdinalAllReopened           = 20
	OrdinalAnchorLazy            = 30
	OrdinalAnchorBackfill        = 40
	OrdinalAnchorPriority        = 50
	OrdinalAdoption              = 55
	OrdinalAdoptionNodeID        = 56
	OrdinalReviewHeadAdvanced    = 60
	OrdinalFeedbackDigestChanged = 70
	OrdinalFixCIFailingOnHead    = 80
	OrdinalConflictPresent       = 90
	OrdinalLandReady             = 100
)

// EntityTypePR is the one entity type that ships a decider on day one (S20).
const EntityTypePR = "pr"

// SkipReasons is the complete skip-reason vocabulary (design 7.2): exactly
// five values, in the order the design lists them.
func SkipReasons() []string {
	return []string{
		action.ReasonHidden,
		action.ReasonSuppressed,
		action.ReasonAlreadyHandled,
		action.ReasonReviewPending,
		action.ReasonNotMatched,
	}
}

func validSkipReason(r string) bool {
	for _, v := range SkipReasons() {
		if v == r {
			return true
		}
	}
	return false
}
