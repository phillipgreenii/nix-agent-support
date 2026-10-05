package rules

import (
	"strings"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

func init() {
	decide.Register(decide.EntityTypePR, decide.OrdinalLandReady, landReadyRule{})
}

// landReadyAnnotation is the annotation key land.ready writes (true or false).
const landReadyAnnotation = "ready_to_land"

// landReadyRule is land.ready (design 7.3, S14): a mine or co-owned PR that is
// green, approved and not a draft is annotated ready_to_land = true, and the
// annotation is flipped back to false when the PR stops being ready. It NEVER
// produces a work item: the ZR deployment set's landing gate forbids any
// agent-reachable merge path. The ci_changed, review_changed and draft_changed
// change kinds it subscribes to are documentation only; Evaluate reads the
// current view and never what changed.
type landReadyRule struct{}

func (landReadyRule) ID() string          { return "land.ready" }
func (landReadyRule) Kind() workitem.Kind { return "" }

func (landReadyRule) Evaluate(in decide.Input) decide.Result {
	v := in.View
	if v == nil || (v.Decorations.Relationship != "mine" && v.Decorations.Relationship != "co-owned") {
		return landReadyNotMatched(nil)
	}
	if skip, terminal := anchorTerminalSkip(v); terminal {
		return skip
	}
	ready := landReadyIs(in)
	current := v.Annotations.ReadyToLand
	switch {
	case ready && current != nil && *current:
		return decide.Result{Skip: &action.Skip{Reason: action.ReasonAlreadyHandled, Facts: map[string]any{"ready_to_land": true}}}
	case ready:
		return landReadyAnnotate("true")
	case current != nil && *current:
		return landReadyAnnotate("false")
	}
	return landReadyNotMatched(nil)
}

// landReadyIs is the pinned readiness predicate: not a draft, review decision
// APPROVED, checks rollup SUCCESS, and the view's CI runs green (at least one
// completed run, none failing or pending; an empty ci.runs is not green).
func landReadyIs(in decide.Input) bool {
	s := in.View.Snapshot
	return !s.Draft &&
		strings.EqualFold(s.ReviewDecision, "APPROVED") &&
		strings.EqualFold(s.ChecksRollup, "SUCCESS") &&
		ciGreen(ciRuns(in.View))
}

func landReadyAnnotate(value string) decide.Result {
	key := landReadyAnnotation
	return decide.Result{Actions: []action.Action{{
		Op:     action.OpAnnotate,
		Target: &key,
		Fields: action.Fields{Value: &value},
		Facts:  map[string]any{"ready_to_land": value == "true"},
	}}}
}

func landReadyNotMatched(facts map[string]any) decide.Result {
	return decide.Result{Skip: &action.Skip{Reason: action.ReasonNotMatched, Facts: facts}}
}
