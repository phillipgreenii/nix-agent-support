package sync

import (
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
	CommitSHA   string
	State       string // store enum: approved/changes-requested/commented
	SubmittedAt string
}

// mySubmittedReviews filters enriched.Reviews to reviews authored by self
// with a mappable state, returning the store-enum state + commit SHA + timestamp.
// GitHub review state (UPPERCASE) → store enum:
//
//	APPROVED → "approved"
//	CHANGES_REQUESTED → "changes-requested"
//	COMMENTED → "commented"
//	DISMISSED/PENDING/other → skipped
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
		switch r.State {
		case "APPROVED":
			storeState = "approved"
		case "CHANGES_REQUESTED":
			storeState = "changes-requested"
		case "COMMENTED":
			storeState = "commented"
		default:
			continue
		}
		out = append(out, submittedReview{
			CommitSHA:   r.CommitOID,
			State:       storeState,
			SubmittedAt: r.SubmittedAt,
		})
	}
	return out
}

// othersApprovedReviews returns the NON-SELF (teammate) APPROVED reviews — the
// inverse-self counterpart of mySubmittedReviews, filtered to APPROVED only.
// It underpins the store-derived "someone else approved" marker used by the
// attention predicate (pg2-4c5i.13). The viewer's OWN approval is deliberately
// EXCLUDED so it can never be mistaken for a teammate's approval (X3). Only
// APPROVED counts — a teammate's COMMENTED/CHANGES_REQUESTED review does not put
// the PR "off the hook". State is always "approved" for the entries returned.
func othersApprovedReviews(reviews []api.Review, self string) []submittedReview {
	var out []submittedReview
	for _, r := range reviews {
		if self != "" && r.Author == self {
			continue // the viewer's own approval is NOT a teammate approval (X3)
		}
		if r.State != "APPROVED" {
			continue
		}
		out = append(out, submittedReview{
			CommitSHA:   r.CommitOID,
			State:       "approved",
			SubmittedAt: r.SubmittedAt,
		})
	}
	return out
}
