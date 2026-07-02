package store

import (
	"context"
	"testing"
)

// Schema v5 adds pr_revision.reviewed_by_agent_at (pg2-4c5i.36 re-review gate).
func TestMigrate_V5AgentReviewedColumn(t *testing.T) {
	db := OpenForTest(t)

	var v int
	if err := db.sql.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if v != schemaVersion || schemaVersion < 5 {
		t.Fatalf("user_version=%d schemaVersion=%d; want both >= 5", v, schemaVersion)
	}

	var cnt int
	if err := db.sql.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info('pr_revision') WHERE name=?",
		"reviewed_by_agent_at",
	).Scan(&cnt); err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("column reviewed_by_agent_at missing from pr_revision")
	}
}

func TestMarkRevisionAgentReviewed_StampsLatestMatchingHead(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)

	// Two revisions: h1 (seq 1), h2 (seq 2, latest).
	if _, _, err := db.RecordRevision(ctx, prID, "h1", "b"); err != nil {
		t.Fatalf("rev1: %v", err)
	}
	if _, _, err := db.RecordRevision(ctx, prID, "h2", "b"); err != nil {
		t.Fatalf("rev2: %v", err)
	}

	if err := db.MarkRevisionAgentReviewed(ctx, prID, "h2", "2026-07-02T00:00:00Z"); err != nil {
		t.Fatalf("MarkRevisionAgentReviewed: %v", err)
	}

	latest, err := db.LatestRevision(ctx, prID)
	if err != nil {
		t.Fatalf("LatestRevision: %v", err)
	}
	if latest.HeadSHA != "h2" {
		t.Fatalf("latest head = %q, want h2", latest.HeadSHA)
	}
	if latest.ReviewedByAgentAt != "2026-07-02T00:00:00Z" {
		t.Fatalf("ReviewedByAgentAt = %q, want stamped", latest.ReviewedByAgentAt)
	}

	// The earlier revision (h1) must NOT be stamped.
	revs, err := db.ListRevisions(ctx, prID)
	if err != nil {
		t.Fatalf("ListRevisions: %v", err)
	}
	for _, r := range revs {
		if r.HeadSHA == "h1" && r.ReviewedByAgentAt != "" {
			t.Fatalf("h1 revision must not be agent-reviewed, got %q", r.ReviewedByAgentAt)
		}
	}
}

// Marking a head SHA with no matching revision is a no-op (mirrors
// MarkRevisionReviewed).
func TestMarkRevisionAgentReviewed_NoMatchingHead_IsNoOp(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)
	if _, _, err := db.RecordRevision(ctx, prID, "h1", "b"); err != nil {
		t.Fatalf("rev1: %v", err)
	}
	if err := db.MarkRevisionAgentReviewed(ctx, prID, "nope", "t"); err != nil {
		t.Fatalf("MarkRevisionAgentReviewed: %v", err)
	}
	latest, err := db.LatestRevision(ctx, prID)
	if err != nil {
		t.Fatalf("LatestRevision: %v", err)
	}
	if latest.ReviewedByAgentAt != "" {
		t.Fatalf("no matching head must leave reviewed_by_agent_at empty, got %q", latest.ReviewedByAgentAt)
	}
}
