package sync

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// othersApprovedReviews returns NON-SELF APPROVED reviews (a teammate approved).
// It is the inverse-self counterpart of mySubmittedReviews, filtered to APPROVED
// only. The viewer's OWN approval MUST NOT be returned (X3 regression guard).
func TestOthersApprovedReviews(t *testing.T) {
	const self = "phillipg"
	const at = "2026-07-02T00:00:00Z"

	t.Run("teammate APPROVED → returned", func(t *testing.T) {
		reviews := []api.Review{{Author: "alice", State: "APPROVED", CommitOID: "h2", SubmittedAt: at}}
		got := othersApprovedReviews(reviews, self)
		if len(got) != 1 {
			t.Fatalf("want 1, got %d: %+v", len(got), got)
		}
		if got[0].CommitSHA != "h2" || got[0].SubmittedAt != at {
			t.Errorf("entry = %+v, want CommitSHA=h2 SubmittedAt=%s", got[0], at)
		}
	})

	t.Run("viewer's OWN APPROVED → EXCLUDED (X3)", func(t *testing.T) {
		reviews := []api.Review{{Author: self, State: "APPROVED", CommitOID: "h2", SubmittedAt: at}}
		got := othersApprovedReviews(reviews, self)
		if len(got) != 0 {
			t.Fatalf("viewer self-approval must not count as others-approved, got %+v", got)
		}
	})

	t.Run("teammate CHANGES_REQUESTED → EXCLUDED (only APPROVED counts)", func(t *testing.T) {
		reviews := []api.Review{{Author: "alice", State: "CHANGES_REQUESTED", CommitOID: "h2", SubmittedAt: at}}
		got := othersApprovedReviews(reviews, self)
		if len(got) != 0 {
			t.Fatalf("non-APPROVED teammate review must not count, got %+v", got)
		}
	})

	t.Run("teammate COMMENTED → EXCLUDED", func(t *testing.T) {
		reviews := []api.Review{{Author: "alice", State: "COMMENTED", CommitOID: "h2", SubmittedAt: at}}
		got := othersApprovedReviews(reviews, self)
		if len(got) != 0 {
			t.Fatalf("COMMENTED teammate review must not count, got %+v", got)
		}
	})

	t.Run("empty self → still returns any APPROVED (no self to exclude)", func(t *testing.T) {
		reviews := []api.Review{{Author: "alice", State: "APPROVED", CommitOID: "h2", SubmittedAt: at}}
		got := othersApprovedReviews(reviews, "")
		if len(got) != 1 {
			t.Fatalf("want 1, got %d: %+v", len(got), got)
		}
	})

	t.Run("mixed → only non-self APPROVED returned", func(t *testing.T) {
		reviews := []api.Review{
			{Author: self, State: "APPROVED", CommitOID: "h1", SubmittedAt: at},    // self → excluded
			{Author: "alice", State: "APPROVED", CommitOID: "h2", SubmittedAt: at}, // teammate → included
			{Author: "bob", State: "COMMENTED", CommitOID: "h2", SubmittedAt: at},  // non-approved → excluded
			{Author: "carol", State: "APPROVED", CommitOID: "h3", SubmittedAt: at}, // teammate → included
		}
		got := othersApprovedReviews(reviews, self)
		if len(got) != 2 {
			t.Fatalf("want 2 (alice, carol), got %d: %+v", len(got), got)
		}
		if got[0].CommitSHA != "h2" || got[1].CommitSHA != "h3" {
			t.Errorf("order not preserved: %+v", got)
		}
	})
}
