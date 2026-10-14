package classify

import "slices"

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
// the fields that changed, each with its before and after value.
//
// Kinds are unique and in Kinds() order; Fields are sorted by name, so a Result
// has one spelling and two equal changes compare equal. A change to a recorded
// field yields a Result even when it carries no kind (every content change is
// delivered); a change that moves only excluded fields yields the zero Result.
type Result struct {
	Kinds  []string
	Fields []Field
}

// Empty reports whether r has no kinds and no fields.
func (r Result) Empty() bool { return len(r.Kinds) == 0 && len(r.Fields) == 0 }

// kindRank is the position of each kind in the canonical order.
var kindRank = func() map[string]int {
	m := map[string]int{}
	for i, k := range Kinds() {
		m[k] = i
	}
	return m
}()

// orderKinds returns the distinct kinds of ks in canonical order. A name that is
// not one of the fourteen sorts after them, alphabetically, so a caller's typo
// is kept rather than lost.
func orderKinds(ks []string) []string {
	if len(ks) == 0 {
		return nil
	}
	out := slices.Clone(ks)
	slices.SortFunc(out, func(a, b string) int {
		ra, oka := kindRank[a]
		rb, okb := kindRank[b]
		switch {
		case oka && okb:
			return ra - rb
		case oka:
			return -1
		case okb:
			return 1
		}
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		}
		return 0
	})
	return slices.Compact(out)
}
