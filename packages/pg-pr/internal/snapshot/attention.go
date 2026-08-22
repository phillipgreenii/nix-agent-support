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
	// AttentionReasonReReview: my review no longer STANDS for the current head,
	// so the PR needs another look. Two ways in, both per-approval staleness
	// (store.Approval.IsStale, INV-APPROVAL-3): new commits landed after I
	// reviewed an earlier head, or the code host DISMISSED my review. The second
	// was unrepresentable before the per-approver cutover (pg2-4dz88.1.9) — the
	// legacy single-slot pr_revision.my_review_state carried no staleness, so a
	// dismissed self review had to be withheld from it entirely and read as
	// "never reviewed".
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
//     latest last. Since pg2-4dz88.1.9 this is consulted for exactly two things:
//     "has any revision been observed at all" and "what is the CURRENT head
//     SHA" (the last element's). Its per-revision review markers are NOT read.
//   - approvals: the PR's PER-APPROVER rows (store.ListApprovals), one per
//     approver login — the source that replaced the collapsed
//     pr_revision.others_approved / my_review_state pair (pg2-4dz88.1.9).
//   - self: the viewer's own login, matched against Approval.Approver to tell
//     "I reviewed it" from "a teammate approved it" (X3). An empty self cannot
//     identify the viewer's own row, so every row reads as a teammate's.
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
// The state machine (§2.7), X3-correct (the viewer's own row is matched by login
// and never counts as "someone else approved"):
//   - the PR has a merge conflict            → off the hook (no attention).
//   - teammate approved the latest head      → off the hook (no attention).
//   - I reviewed the latest head             → off the hook (no attention).
//   - my review no longer stands for the latest head (new commits landed after
//     it, or the host dismissed it) → needs a re-review.
//   - none of the above (I have never reviewed it) → needs a first review.
//
// # What the per-approver cutover changed (pg2-4dz88.1.9)
//
// Every edge above is unchanged in intent; only their SOURCE moved, from the
// two collapsed pr_revision columns to the per-approver rows. The cutover ADDS
// one case those columns could not represent at all: a DISMISSED-only approval.
// `others_approved` is a head-anchored boolean with no dismissal, so
// internal/sync/ingest.go had to withhold a dismissed approval from it
// entirely — which read as "nobody has approved", accidentally the right
// verdict, but by omission rather than by knowing. Read per-approver, the row
// EXISTS and reports itself stale (Approval.IsStale is true for a dismissed row
// REGARDLESS of head), so a dismissed approval provably does not close the
// edge, and a later re-approval provably does.
func NeedsAttention(revs []store.Revision, approvals []store.Approval, self string, hasConflict bool) (need bool, reason string) {
	if len(revs) == 0 {
		return false, ""
	}
	// A conflicting team PR is not worth reviewing until the author rebases —
	// dampen it out of the attention signal entirely (dashboard + bead both, via
	// this shared predicate). (pg2-tsgkj)
	if hasConflict {
		return false, ""
	}
	head := revs[len(revs)-1].HeadSHA

	// One pass over the per-approver rows, splitting self from teammates by
	// login (X3). There is at most ONE self row: pr_approval is UNIQUE
	// (pr_id, approver), so the row IS my latest observed review.
	var myReview *store.Approval
	for i := range approvals {
		a := approvals[i]
		if self != "" && a.Approver == self {
			myReview = &approvals[i]
			continue
		}
		// Closing edge: a teammate approval that currently STANDS puts the PR
		// off my plate. A teammate's "changes-requested" or "commented" row does
		// NOT (asking for changes is not an approval), and neither does a stale
		// one — an approval of an earlier head, or one the host dismissed.
		if a.State == "approved" && !a.IsStale(head) {
			return false, ""
		}
	}

	if myReview != nil {
		// Closing edge: I reviewed the CURRENT head. Any state counts here, as it
		// always has — having looked at this head at all takes it off my plate.
		if !myReview.IsStale(head) {
			return false, ""
		}
		// Re-review edge: my review does not stand for the current head.
		return true, AttentionReasonReReview
	}

	// First-review edge: no standing teammate approval and no review of my own on
	// record — I simply have not looked at this teammate PR yet.
	return true, AttentionReasonUnreviewed
}
