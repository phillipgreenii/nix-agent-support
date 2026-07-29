package snapshot

import "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"

// Attention reason strings surfaced on the dashboard and carried by the
// attention bead / pr.attention event. Exported so consumers (and their tests)
// reference the canonical values rather than re-typing literals.
const (
	// AttentionReasonUnreviewed: the first-review edge — a teammate PR I have
	// never reviewed at any observed head, that nobody else has approved either.
	// It is the plain "I have not looked at this yet" signal (pg2-kh1ar).
	AttentionReasonUnreviewed = "unreviewed-by-me"
	// AttentionReasonReReview: new commits landed after I approved an earlier
	// head, so the PR needs a re-review.
	AttentionReasonReReview = "re-review-after-my-approval"
)

// NeedsAttention is the ONE pure predicate consumed by BOTH the dashboard read
// model (buildTeamRow) and the attention-bead write model (the refreshPR
// projector in internal/sync). Exported so BOTH consumers call the SAME
// function — they can NEVER diverge (design §2.7, D4 / R4; explicit acceptance
// criterion). Feeding both the SAME store-sourced inputs is what makes the
// dashboard signal and the open-attention-bead set provably consistent.
//
// Inputs are ALL persisted store facts plus the live merge-state flag — never
// live enriched reviews, and (since pg2-kh1ar) never a bead artifact:
//   - revs: the PR's revisions in ascending seq order (store.ListRevisions),
//     latest last.
//   - hasConflict: GitHub's merge-conflict signal (api.PR.HasConflict()). A
//     conflicting team PR is dampened out of the attention signal entirely —
//     it isn't worth reviewing until the author rebases (pg2-tsgkj).
//
// The first-review edge deliberately takes NO review-readiness input. It used to
// require "the pg-pr draft-review bead for this PR is CLOSED", but that bead is
// produced ONLY by the legacy in-daemon pg-pr review path, which ships OFF
// (config.ReviewEnabled defaults to false; ADR 0034 — pr-pool owns reviews). At
// the shipped default no draft-review bead is ever created, so the readiness
// input was permanently false and the whole first-review edge was dead code:
// "a teammate PR I have never reviewed" could never fire (pg2-kh1ar). The signal
// is therefore defined purely over facts pg-pr owns as the PR-DATA interface, so
// it fires regardless of which review owner is active.
//
// Draft PRs are dampened UPSTREAM of this predicate, not inside it: refreshPR
// returns before the attention projection for a hidden TEAM draft, and Build
// admits no draft PR into the team rows — so both consumers agree without a
// draft input here.
//
// The state machine (§2.7), X3-correct (a teammate's approval is the persisted
// others_approved marker; the viewer's own approval lives in my_review_state and
// never counts as "someone else approved"):
//   - the PR has a merge conflict            → off the hook (no attention).
//   - teammate approved the latest head      → off the hook (no attention).
//   - I reviewed the latest head             → off the hook (no attention).
//   - I approved an EARLIER head but not the latest (new commits landed) →
//     needs a re-review.
//   - none of the above (I have never reviewed it) → needs a first review.
func NeedsAttention(revs []store.Revision, hasConflict bool) (need bool, reason string) {
	if len(revs) == 0 {
		return false, ""
	}
	// A conflicting team PR is not worth reviewing until the author rebases —
	// dampen it out of the attention signal entirely (dashboard + bead both, via
	// this shared predicate). (pg2-tsgkj)
	if hasConflict {
		return false, ""
	}
	latest := revs[len(revs)-1]

	// Closing edges: once a teammate approved or I reviewed the current head, the
	// PR is off my plate.
	if latest.OthersApproved {
		return false, ""
	}
	if latest.MyReviewState != "" {
		return false, ""
	}

	// Re-review edge: I approved (or otherwise reviewed) some EARLIER revision but
	// not the latest — new commits landed after my review, so it needs another
	// look.
	for _, r := range revs[:len(revs)-1] {
		if r.MyReviewState != "" {
			return true, AttentionReasonReReview
		}
	}

	// First-review edge: nobody approved the head and I have never reviewed ANY
	// observed revision — I simply have not looked at this teammate PR yet.
	return true, AttentionReasonUnreviewed
}
