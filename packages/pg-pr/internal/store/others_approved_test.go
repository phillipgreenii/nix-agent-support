package store

import (
	"context"
	"testing"
)

// MarkRevisionOthersApproved sets others_approved=1 + others_approved_at on the
// latest revision whose head_sha matches (mirroring MarkRevisionReviewed's
// head-match semantics). No-op when no revision matches that head.
func TestMarkRevisionOthersApproved(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)

	if _, _, err := db.RecordRevision(ctx, prID, "h1", "b1"); err != nil {
		t.Fatalf("record rev1: %v", err)
	}
	if _, _, err := db.RecordRevision(ctx, prID, "h2", "b1"); err != nil {
		t.Fatalf("record rev2: %v", err)
	}

	// A teammate approved at h2.
	if err := db.MarkRevisionOthersApproved(ctx, prID, "h2", "2026-07-02T00:00:00Z"); err != nil {
		t.Fatalf("MarkRevisionOthersApproved: %v", err)
	}

	revs, err := db.ListRevisions(ctx, prID)
	if err != nil {
		t.Fatalf("ListRevisions: %v", err)
	}
	byHead := map[string]Revision{}
	for _, r := range revs {
		byHead[r.HeadSHA] = r
	}
	if byHead["h1"].OthersApproved {
		t.Errorf("h1 should NOT be others_approved")
	}
	if !byHead["h2"].OthersApproved {
		t.Errorf("h2 should be others_approved")
	}
	if byHead["h2"].OthersApprovedAt != "2026-07-02T00:00:00Z" {
		t.Errorf("h2 others_approved_at = %q, want the recorded timestamp", byHead["h2"].OthersApprovedAt)
	}
}

// Marking a head that has no revision is a silent no-op (matches
// MarkRevisionReviewed).
func TestMarkRevisionOthersApproved_NoMatchIsNoop(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)
	if _, _, err := db.RecordRevision(ctx, prID, "h1", "b1"); err != nil {
		t.Fatalf("record rev: %v", err)
	}
	if err := db.MarkRevisionOthersApproved(ctx, prID, "nonexistent", "t"); err != nil {
		t.Fatalf("no-match should be a no-op, got: %v", err)
	}
	revs, _ := db.ListRevisions(ctx, prID)
	if revs[0].OthersApproved {
		t.Errorf("no revision should have been marked")
	}
}
