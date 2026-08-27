package snapshot

import (
	"cmp"
	"slices"
	"strings"
)

// CompareTeamRows is the ONE exported ordering function for the "PRs to
// Review" set (design precedent: attention.go's NeedsAttention -- "the ONE
// pure predicate consumed by BOTH the dashboard read model and the
// attention-bead write model ... Exported so BOTH consumers call the SAME
// function -- they can NEVER diverge"). CompareTeamRows gets exactly that
// treatment: it is the SOLE place `[]TeamRow` is ordered (TestNoSecondSortOverRows
// enforces this mechanically), usable directly with slices.SortStableFunc, and
// every consumer -- Build (which now sorts snap.Team with it before returning),
// the Grafana "Team PRs" panels, and `pg-pr open`'s team-side listing -- reads
// the SAME order rather than each re-deriving one (pg2-4dz88.7.2).
//
// # Sort key sequence (operator-approved 2026-08-21; amended 2026-08-24 via
// pg2-o7bll to add the already-engaged top slot pg2-4dz88.11 needs)
//
//  1. Reviewer-role tier (5 rungs): already-engaged (reviewed-by-me OR
//     assigned-to-me, regardless of owner/labels) > requested-reviewer >
//     CODEOWNERS-required > watch-label-only > rest.
//  2. Stale-review-of-mine before never-reviewed (before "nothing to review").
//  3. Upstream-of-another-PR before waiting-on-another-PR.
//  4. Smaller first (by LinesChanged).
//  5. repo+number, as the FINAL tie-break -- the only key guaranteed injective
//     across distinct rows, so no two distinct rows can tie on every key
//     (TestCompareTeamRows_FinalKeyIsTotal).
//
// # Key 1's CODEOWNERS-required rung has no producer yet
//
// MatchReasonCodeownersRequired is a reserved match-reason value (see its own
// doc on the MatchReason* const block in builder.go): nothing in this module
// detects GitHub CODEOWNERS-driven review requests today (pg2-4dz88.2 shipped
// the policy-bot CHECK-INTERPRETER axis, a different concept, not CODEOWNERS
// branch-protection detection), so this rung is UNREACHABLE from a real
// Build() call until a later change starts setting it. The rung still exists
// in the ladder -- and is fully tested via a TeamRow constructed directly with
// that MatchReason -- because an absent decision must not be indistinguishable
// from a forgotten one (this bead's own grooming-review precedent), and because
// wiring the rung in NOW means the eventual producer needs to set one match
// reason and nothing in this file changes.
//
// # Key 3's "upstream vs standalone" collapse
//
// TeamRow.DependencyOrderingKey (pg2-4dz88.3.7) is 0 for BOTH "this PR is the
// upstream some other PR in the set is waiting on" and "this PR has no stack
// relation to anything" -- its own doc says the PR being waited ON gets the
// same 0 a genuinely unrelated PR gets, only the WAITING PR gets a negative
// value. So key 3, sorting DependencyOrderingKey descending exactly as that
// field's doc prescribes ("a comparator that sorts this key in DESCENDING
// order ... always places an upstream PR ahead of anything waiting on it"),
// correctly ranks a genuinely blocked/stacked PR below the PR it is blocked
// by, but cannot further distinguish "is upstream of something" from
// "standalone" -- both rank identically at key 3 and fall through to key 4.
// No TeamRow fact represents "has a downstream dependent" today (mirroring
// key 1's CODEOWNERS gap -- pg2-4dz88.3 is closed, but, per this bead's own
// dependency note, shipped the FACT + ordering-KEY VALUE only, not that
// specific boolean), so this is the honest behavior available from current
// facts, not an oversight.
func CompareTeamRows(a, b TeamRow) int {
	return cmp.Or(
		cmp.Compare(reviewerRoleTier(a), reviewerRoleTier(b)),
		cmp.Compare(stalenessRank(a), stalenessRank(b)),
		// Descending: a HIGHER DependencyOrderingKey (less blocked; 0 for
		// "upstream of another PR or standalone") ranks FIRST.
		cmp.Compare(b.DependencyOrderingKey, a.DependencyOrderingKey),
		cmp.Compare(a.LinesChanged, b.LinesChanged), // smaller first
		cmp.Compare(a.Repo, b.Repo),
		cmp.Compare(a.Number, b.Number),
	)
}

// sortTeamRows sorts rows in place by CompareTeamRows. This is the ONLY call
// site (besides ordering_test.go's own tests) that invokes a sort primitive
// over a []TeamRow anywhere in the module -- see TestNoSecondSortOverRows.
// Build calls this directly rather than reaching for slices.SortStableFunc
// itself, keeping every sort-primitive call site for TeamRow ordering
// confined to this one file.
func sortTeamRows(rows []TeamRow) {
	slices.SortStableFunc(rows, CompareTeamRows)
}

// Reviewer-role tier ordinals for sort key 1. Lower value ranks FIRST (higher
// precedence). Unexported: no consumer outside this file needs the ordinal
// values themselves, only CompareTeamRows's resulting order.
const (
	tierAlreadyEngaged = iota
	tierRequestedReviewer
	tierCodeownersRequired
	tierWatchLabelOnly
	tierRest
)

// reviewerRoleTier maps a TeamRow's MatchReason facts onto the 5-rung ladder.
// The switch is written in ladder order so earlier (higher-precedence) cases
// take priority when a row's MatchReason carries more than one qualifying
// value (e.g. a row that is both reviewed-by-me AND review-requested is
// already-engaged, not requested-reviewer).
func reviewerRoleTier(row TeamRow) int {
	switch {
	case hasMatchReason(row.MatchReason, MatchReasonReviewedByMe) || hasMatchReason(row.MatchReason, MatchReasonAssignedToMe):
		return tierAlreadyEngaged
	case hasMatchReason(row.MatchReason, MatchReasonReviewRequested):
		return tierRequestedReviewer
	case hasMatchReason(row.MatchReason, MatchReasonCodeownersRequired):
		return tierCodeownersRequired
	case hasWatchLabelReason(row.MatchReason):
		return tierWatchLabelOnly
	default:
		return tierRest
	}
}

// hasMatchReason reports whether reasons contains want, by EXACT value (never
// a prefix match -- that distinction matters only for the label family, which
// hasWatchLabelReason handles separately).
func hasMatchReason(reasons []string, want string) bool {
	for _, r := range reasons {
		if r == want {
			return true
		}
	}
	return false
}

// hasWatchLabelReason reports whether reasons contains any matched
// watch-label reason (MatchReasonLabelPrefix + <label>).
func hasWatchLabelReason(reasons []string) bool {
	for _, r := range reasons {
		if strings.HasPrefix(r, MatchReasonLabelPrefix) {
			return true
		}
	}
	return false
}

// stalenessRank maps sort key 2: a stale review of mine ranks before a
// never-reviewed PR, which ranks before a row with nothing to review at all
// (NeedsAttention already false -- off the hook, so there is no urgency left
// on this axis). See snapshot.AttentionReason* for the two named values;
// anything else (including the empty string) is the third, lowest-urgency
// rank.
func stalenessRank(row TeamRow) int {
	switch row.AttentionReason {
	case AttentionReasonReReview:
		return 0
	case AttentionReasonUnreviewed:
		return 1
	default:
		return 2
	}
}
