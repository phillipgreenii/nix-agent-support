package sync

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// ciRollupFromSync maps []api.CIRun to a store.CIRollup.
// Classification:
//   - passed:  Status=="completed" && Conclusion=="success"
//   - failed:  Status=="completed" && Conclusion!="success" (covers failure,
//     error, cancelled, timed_out, neutral, skipped, etc.)
//   - pending: anything else (Status != "completed")
//
// Overall State: any failed → "failure"; else any pending → "pending";
// else (≥1 run, all passed) → "success"; no runs → "none".
//
// now is an injectable clock; when nil it defaults to time.Now.
func ciRollupFromSync(runs []api.CIRun, now func() time.Time) store.CIRollup {
	if now == nil {
		now = time.Now
	}
	capturedAt := now().UTC().Format(time.RFC3339)
	if len(runs) == 0 {
		return store.CIRollup{State: "none", CapturedAt: capturedAt}
	}
	var passed, failed, pending int
	for _, r := range runs {
		switch {
		case r.Status == "completed" && r.Conclusion == "success":
			passed++
		case r.Status == "completed":
			// conclusion != "success": failure, error, cancelled, timed_out, etc.
			failed++
		default:
			pending++
		}
	}
	state := "success"
	if failed > 0 {
		state = "failure"
	} else if pending > 0 {
		state = "pending"
	}
	return store.CIRollup{
		State:      state,
		Passed:     passed,
		Failed:     failed,
		Pending:    pending,
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
