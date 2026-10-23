package classify

import "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"

// The fourteen upstream change kinds, spelled exactly as the spec spells them.
const (
	KindReopened            = "reopened"
	KindClosed              = "closed"
	KindMerged              = "merged"
	KindDraftChanged        = "draft_changed"
	KindHeadChanged         = "head_changed"
	KindBaseChanged         = "base_changed"
	KindCIChanged           = "ci_changed"
	KindMergeabilityChanged = "mergeability_changed"
	KindReviewChanged       = "review_changed"
	KindFeedbackChanged     = "feedback_changed"
	KindRenamed             = "renamed"
	KindRemoved             = "removed"
	KindEnteredQuery        = "entered_query"
	KindLeftQuery           = "left_query"
)

// Kinds returns the fourteen kind names in the canonical order every kind list
// uses. The caller owns the slice.
func Kinds() []string {
	return []string{
		KindReopened, KindClosed, KindMerged, KindDraftChanged, KindHeadChanged,
		KindBaseChanged, KindCIChanged, KindMergeabilityChanged, KindReviewChanged,
		KindFeedbackChanged, KindRenamed, KindRemoved, KindEnteredQuery, KindLeftQuery,
	}
}

// Result is what a content change amounts to: the upstream kinds it carries and
// the fields that changed, each with its before and after value (the shared
// engine's type, re-exported).
//
// Kinds are unique and in Kinds() order; Fields are sorted by name, so a Result
// has one spelling and two equal changes compare equal. A change to a recorded
// field yields a Result even when it carries no kind (every content change is
// delivered); a change that moves only excluded fields yields the zero Result.
type Result = engine.Result
