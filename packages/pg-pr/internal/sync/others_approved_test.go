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
		if got[0].Approver != "alice" {
			t.Errorf("Approver: got %q want \"alice\" (pg2-4dz88.1.5 per-approver feed)", got[0].Approver)
		}
	})

	t.Run("viewer's OWN APPROVED → EXCLUDED (X3)", func(t *testing.T) {
		reviews := []api.Review{{Author: self, State: "APPROVED", CommitOID: "h2", SubmittedAt: at}}
		got := othersApprovedReviews(reviews, self)
		if len(got) != 0 {
			t.Fatalf("viewer self-approval must not count as others-approved, got %+v", got)
		}
	})

	// Regression guard for pg2-4dz88.1.7, the teammate half: a DISMISSED
	// teammate review used to be dropped by the APPROVED-only filter. It must
	// now come back marked stale (INV-APPROVAL-3) so the per-approver row can
	// record that this teammate DID approve — while its caller keeps it out of
	// the staleness-blind others_approved marker.
	t.Run("teammate DISMISSED → returned as a STALE approval", func(t *testing.T) {
		reviews := []api.Review{{Author: "alice", State: "DISMISSED", CommitOID: "h1", SubmittedAt: at}}
		got := othersApprovedReviews(reviews, self)
		if len(got) != 1 {
			t.Fatalf("a dismissed teammate approval must survive the mapping, got %d: %+v", len(got), got)
		}
		if got[0].Approver != "alice" || got[0].CommitSHA != "h1" || got[0].SubmittedAt != at {
			t.Errorf("entry = %+v, want Approver=alice CommitSHA=h1 SubmittedAt=%s", got[0], at)
		}
		if got[0].State != "approved" {
			t.Errorf("State: got %q want \"approved\" (a dismissed review is a stale APPROVAL)", got[0].State)
		}
		if !got[0].Dismissed {
			t.Errorf("Dismissed: got false want true — otherwise it reads as a CURRENT teammate approval")
		}
	})

	t.Run("viewer's OWN DISMISSED → EXCLUDED (X3 still applies)", func(t *testing.T) {
		reviews := []api.Review{{Author: self, State: "DISMISSED", CommitOID: "h1", SubmittedAt: at}}
		got := othersApprovedReviews(reviews, self)
		if len(got) != 0 {
			t.Fatalf("the viewer's own dismissed approval must not count as others-approved, got %+v", got)
		}
	})

	t.Run("teammate APPROVED is NOT marked dismissed", func(t *testing.T) {
		reviews := []api.Review{{Author: "alice", State: "APPROVED", CommitOID: "h2", SubmittedAt: at}}
		got := othersApprovedReviews(reviews, self)
		if len(got) != 1 {
			t.Fatalf("want 1, got %d: %+v", len(got), got)
		}
		if got[0].Dismissed {
			t.Errorf("a current teammate APPROVED review must not be marked dismissed: %+v", got[0])
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
