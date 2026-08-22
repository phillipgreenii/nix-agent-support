package sync

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// othersChangesRequestedReviews returns NON-SELF CHANGES_REQUESTED reviews (a
// teammate explicitly asked for changes). It is the changes-requested
// counterpart of othersApprovedReviews (pg2-4dz88.1.8): the viewer's OWN
// review MUST NOT be returned (X3 regression guard), and a teammate's
// COMMENTED review MUST NOT be conflated with CHANGES_REQUESTED.
func TestOthersChangesRequestedReviews(t *testing.T) {
	const self = "phillipg"
	const at = "2026-07-02T00:00:00Z"

	t.Run("teammate CHANGES_REQUESTED → returned", func(t *testing.T) {
		reviews := []api.Review{{Author: "alice", State: "CHANGES_REQUESTED", CommitOID: "h2", SubmittedAt: at}}
		got := othersChangesRequestedReviews(reviews, self)
		if len(got) != 1 {
			t.Fatalf("want 1, got %d: %+v", len(got), got)
		}
		if got[0].Approver != "alice" {
			t.Errorf("Approver: got %q want \"alice\"", got[0].Approver)
		}
		if got[0].State != "changes-requested" {
			t.Errorf("State: got %q want \"changes-requested\"", got[0].State)
		}
		if got[0].CommitSHA != "h2" || got[0].SubmittedAt != at {
			t.Errorf("entry = %+v, want CommitSHA=h2 SubmittedAt=%s", got[0], at)
		}
	})

	t.Run("viewer's OWN CHANGES_REQUESTED → EXCLUDED (X3)", func(t *testing.T) {
		reviews := []api.Review{{Author: self, State: "CHANGES_REQUESTED", CommitOID: "h2", SubmittedAt: at}}
		got := othersChangesRequestedReviews(reviews, self)
		if len(got) != 0 {
			t.Fatalf("the viewer's own review must never count as a teammate's, got %+v", got)
		}
	})

	t.Run("teammate COMMENTED → EXCLUDED (not conflated with changes-requested)", func(t *testing.T) {
		reviews := []api.Review{{Author: "alice", State: "COMMENTED", CommitOID: "h2", SubmittedAt: at}}
		got := othersChangesRequestedReviews(reviews, self)
		if len(got) != 0 {
			t.Fatalf("COMMENTED must not be treated as changes-requested, got %+v", got)
		}
	})

	t.Run("teammate APPROVED → EXCLUDED (only CHANGES_REQUESTED counts here)", func(t *testing.T) {
		reviews := []api.Review{{Author: "alice", State: "APPROVED", CommitOID: "h2", SubmittedAt: at}}
		got := othersChangesRequestedReviews(reviews, self)
		if len(got) != 0 {
			t.Fatalf("APPROVED must not be returned by othersChangesRequestedReviews, got %+v", got)
		}
	})

	t.Run("empty self → still returns any CHANGES_REQUESTED (no self to exclude)", func(t *testing.T) {
		reviews := []api.Review{{Author: "alice", State: "CHANGES_REQUESTED", CommitOID: "h2", SubmittedAt: at}}
		got := othersChangesRequestedReviews(reviews, "")
		if len(got) != 1 {
			t.Fatalf("want 1, got %d: %+v", len(got), got)
		}
	})

	t.Run("mixed → only non-self CHANGES_REQUESTED returned", func(t *testing.T) {
		reviews := []api.Review{
			{Author: self, State: "CHANGES_REQUESTED", CommitOID: "h1", SubmittedAt: at},    // self → excluded
			{Author: "alice", State: "CHANGES_REQUESTED", CommitOID: "h2", SubmittedAt: at}, // teammate → included
			{Author: "bob", State: "COMMENTED", CommitOID: "h2", SubmittedAt: at},           // not conflated → excluded
			{Author: "carol", State: "APPROVED", CommitOID: "h3", SubmittedAt: at},          // approved, not this func → excluded
			{Author: "dave", State: "CHANGES_REQUESTED", CommitOID: "h3", SubmittedAt: at},  // teammate → included
		}
		got := othersChangesRequestedReviews(reviews, self)
		if len(got) != 2 {
			t.Fatalf("want 2 (alice, dave), got %d: %+v", len(got), got)
		}
		if got[0].CommitSHA != "h2" || got[1].CommitSHA != "h3" {
			t.Errorf("order not preserved: %+v", got)
		}
	})
}
