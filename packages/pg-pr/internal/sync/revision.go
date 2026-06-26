package sync

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// ciRollupFromSync maps []api.CIRun to a store.CIRollup.
// Classification:
//   - failed: Status=="completed" && Conclusion=="failure"
//   - passed: Status=="completed" && Conclusion=="success"
//   - pending: anything else (not completed)
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
		case r.Status == "completed" && r.Conclusion == "failure":
			failed++
		case r.Status == "completed" && r.Conclusion == "success":
			passed++
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
