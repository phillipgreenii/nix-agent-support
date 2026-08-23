package sync

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/cirollup"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// ciRollupFromSync maps []api.CIRun to a store.CIRollup. Classification and
// aggregation are delegated to internal/cirollup — the single source of
// truth for "is CI failed?" (pg2-qs46b). excl drops advisory checks (e.g.
// policy-bot) from the rollup entirely.
//
// now is an injectable clock; when nil it defaults to time.Now.
func ciRollupFromSync(runs []api.CIRun, now func() time.Time, excl *cirollup.Excluder) store.CIRollup {
	if now == nil {
		now = time.Now
	}
	capturedAt := now().UTC().Format(time.RFC3339)
	if len(runs) == 0 {
		return store.CIRollup{State: "none", CapturedAt: capturedAt}
	}
	r := cirollup.Compute(runs, excl)
	return store.CIRollup{
		State:      r.State,
		Passed:     r.Passed,
		Failed:     r.Failed,
		Pending:    r.Pending,
		CapturedAt: capturedAt,
	}
}

// submittedReview is a filtered review targeted at a specific commit.
type submittedReview struct {
	// Approver is the GitHub login the review is attributed to — self for
	// mySubmittedReviews, the reviewing teammate for othersApprovedReviews.
	// Feeds store.SetApproval's per-approver row (pg2-4dz88.1.5).
	Approver    string
	CommitSHA   string
	State       string // store enum: approved/changes-requested/commented
	SubmittedAt string
	// Dismissed marks a review the code host reported as DISMISSED. State is
	// "approved" for such a review (the host does not report what it said
	// before the dismissal) and it lands in the per-approver table as a STALE
	// approval — never dropped (INV-APPROVAL-3, pg2-4dz88.1.7). recordApproval
	// routes a Dismissed review to SetDismissedApproval so it lands stale
	// rather than current.
	Dismissed bool
}

// mySubmittedReviews filters enriched.Reviews to reviews authored by self
// with a mappable state, returning the store-enum state + commit SHA + timestamp.
// GitHub review state (UPPERCASE) → store enum:
//
//	APPROVED → "approved"
//	CHANGES_REQUESTED → "changes-requested"
//	COMMENTED → "commented"
//	DISMISSED → "approved" + Dismissed (a STALE approval, INV-APPROVAL-3)
//	PENDING/other → skipped
func mySubmittedReviews(reviews []api.Review, self string) []submittedReview {
	if self == "" {
		return nil
	}
	var out []submittedReview
	for _, r := range reviews {
		if r.Author != self {
			continue
		}
		var storeState string
		var dismissed bool
		switch r.State {
		case "APPROVED":
			storeState = "approved"
		case "CHANGES_REQUESTED":
			storeState = "changes-requested"
		case "COMMENTED":
			storeState = "commented"
		case "DISMISSED":
			// A dismissed review is a STALE approval, not an absent one
			// (INV-APPROVAL-3). It used to fall through the default below and
			// vanish, so an approver who DID approve was indistinguishable
			// from one who never did (pg2-4dz88.1.7).
			storeState, dismissed = "approved", true
		default:
			continue
		}
		out = append(out, submittedReview{
			Approver:    r.Author,
			CommitSHA:   r.CommitOID,
			State:       storeState,
			SubmittedAt: r.SubmittedAt,
			Dismissed:   dismissed,
		})
	}
	return out
}

// othersApprovedReviews returns the NON-SELF (teammate) reviews that are, or
// once were, approvals — the inverse-self counterpart of mySubmittedReviews.
// It underpins the store-derived "someone else approved" marker used by the
// attention predicate (pg2-4c5i.13). The viewer's OWN approval is deliberately
// EXCLUDED so it can never be mistaken for a teammate's approval (X3). A
// teammate's COMMENTED/CHANGES_REQUESTED review does not put the PR "off the
// hook" and is not returned. State is always "approved" for the entries
// returned; a DISMISSED teammate review is returned with Dismissed set — a
// STALE approval, never an absent one (INV-APPROVAL-3, pg2-4dz88.1.7) — and
// recordApproval routes it to SetDismissedApproval accordingly.
//
// See othersChangesRequestedReviews for the CHANGES_REQUESTED counterpart
// (pg2-4dz88.1.8), which feeds the SAME per-approver pr_approval table.
func othersApprovedReviews(reviews []api.Review, self string) []submittedReview {
	var out []submittedReview
	for _, r := range reviews {
		if self != "" && r.Author == self {
			continue // the viewer's own approval is NOT a teammate approval (X3)
		}
		var dismissed bool
		switch r.State {
		case "APPROVED":
			// A currently-standing teammate approval.
		case "DISMISSED":
			dismissed = true
		default:
			continue
		}
		out = append(out, submittedReview{
			Approver:    r.Author,
			CommitSHA:   r.CommitOID,
			State:       "approved",
			SubmittedAt: r.SubmittedAt,
			Dismissed:   dismissed,
		})
	}
	return out
}

// othersChangesRequestedReviews returns the NON-SELF (teammate)
// CHANGES_REQUESTED reviews (pg2-4dz88.1.8) — the changes-requested
// counterpart of othersApprovedReviews, feeding the SAME per-approver
// pr_approval table so "a teammate explicitly asked for changes" becomes
// representable and distinct from both an absent record and that same
// approver's own APPROVED/STALE state. The viewer's OWN review is excluded
// for the same reason as othersApprovedReviews (X3): it is never a teammate
// review. A teammate's COMMENTED review is deliberately dropped here too —
// it MUST NOT be conflated with CHANGES_REQUESTED, so it is neither returned
// by this function nor by othersApprovedReviews.
//
// A teammate asking for changes does not put the PR "off the hook", so
// callers MUST NOT feed these entries into the others-approved ingest loop.
// State is always "changes-requested" for the entries returned.
func othersChangesRequestedReviews(reviews []api.Review, self string) []submittedReview {
	var out []submittedReview
	for _, r := range reviews {
		if self != "" && r.Author == self {
			continue // the viewer's own review is NOT a teammate review (X3)
		}
		if r.State != "CHANGES_REQUESTED" {
			continue
		}
		out = append(out, submittedReview{
			Approver:    r.Author,
			CommitSHA:   r.CommitOID,
			State:       "changes-requested",
			SubmittedAt: r.SubmittedAt,
		})
	}
	return out
}

// recordApproval writes one observed review as a per-approver row
// (pg2-4dz88.1.5): a DISMISSED review lands as a STALE approval
// (pg2-4dz88.1.7, INV-APPROVAL-3), every other state as the state it was
// observed in.
func (e *Engine) recordApproval(ctx context.Context, prID int64, rv submittedReview) error {
	if rv.Dismissed {
		return e.deps.Store.SetDismissedApproval(ctx, prID, rv.Approver, rv.CommitSHA, rv.SubmittedAt)
	}
	return e.deps.Store.SetApproval(ctx, prID, rv.Approver, rv.CommitSHA, rv.State, rv.SubmittedAt)
}
