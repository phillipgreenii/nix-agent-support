package snapshot

import "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"

// Attention reason strings surfaced on the dashboard and carried by the
// attention bead / pr.attention event. Exported so consumers (and their tests)
// reference the canonical values rather than re-typing literals.
const (
	// AttentionReasonDraftReviewReady: a draft review is ready (its bead was
	// closed by pg2-4c5i.36) and nobody has approved / I have not reviewed head.
	AttentionReasonDraftReviewReady = "draft-review-ready-unapproved"
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
// Inputs are ALL persisted store facts — never live enriched reviews:
//   - revs: the PR's revisions in ascending seq order (store.ListRevisions),
//     latest last.
//   - draftReviewClosed: true iff pg2-4c5i.36 CLOSED the draft-review bead for
//     this PR (the "draft review ready" signal — NOT the on-disk Draft, which
//     .35 clears; design D1#1).
//
// The state machine (§2.7), X3-correct (a teammate's approval is the persisted
// others_approved marker; the viewer's own approval lives in my_review_state and
// never counts as "someone else approved"):
//   - teammate approved the latest head  → off the hook (no attention).
//   - I reviewed the latest head          → off the hook (no attention).
//   - I approved an EARLIER head but not the latest (new commits landed) →
//     needs a re-review.
//   - a draft review is ready and none of the above → needs attention.
//   - otherwise → no attention.
func NeedsAttention(revs []store.Revision, draftReviewClosed bool) (need bool, reason string) {
	if len(revs) == 0 {
		return false, ""
	}
	latest := revs[len(revs)-1]

	// Closing edges take precedence: once a teammate approved or I reviewed the
	// current head, the PR is off my plate regardless of draft-review readiness.
	if latest.OthersApproved {
		return false, ""
	}
	if latest.MyReviewState != "" {
		return false, ""
	}

	// Re-review edge: I approved (or otherwise reviewed) some EARLIER revision but
	// not the latest — new commits landed after my review, so it needs another
	// look. This fires independently of draft-review readiness.
	for _, r := range revs[:len(revs)-1] {
		if r.MyReviewState != "" {
			return true, AttentionReasonReReview
		}
	}

	// First-review edge: a draft review is ready (its bead was closed) and nobody
	// has approved / I have not reviewed the head.
	if draftReviewClosed {
		return true, AttentionReasonDraftReviewReady
	}

	return false, ""
}
