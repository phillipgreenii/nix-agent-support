package store

import (
	"context"
	"testing"
)

// Schema v6 rebuilds the feedback.kind CHECK to include 'self-review'
// (pg2-4c5i.34 my-PR sink). The table-rebuild MUST preserve the FK-bearing
// code_comment_message rows and re-create idx_feedback_pr.
func TestMigrate_V6SelfReviewKindAccepted(t *testing.T) {
	db := OpenForTest(t)

	var v int
	if err := db.sql.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if v != schemaVersion || schemaVersion < 6 {
		t.Fatalf("user_version=%d schemaVersion=%d; want both >= 6", v, schemaVersion)
	}

	prID := seedPR(t, db)

	// A self-review row (no file — fileless PR-level finding is legal) is accepted.
	_, err := db.sql.Exec(`INSERT INTO feedback (pr_id,kind,fingerprint,status,body,created_at,updated_at)
		VALUES (?,'self-review','fp-sr','new','a finding','t','t')`, prID)
	if err != nil {
		t.Fatalf("self-review row should be accepted post-migration: %v", err)
	}

	// An unknown kind is still rejected (the CHECK is intact, not dropped).
	if _, err := db.sql.Exec(`INSERT INTO feedback (pr_id,kind,fingerprint,status,created_at,updated_at)
		VALUES (?,'bogus-kind','fp-bogus','new','t','t')`, prID); err == nil {
		t.Fatal("expected kind CHECK to still reject an unknown kind")
	}

	// The code-comment-thread file guard survives the rebuild.
	if _, err := db.sql.Exec(`INSERT INTO feedback (pr_id,kind,fingerprint,status,created_at,updated_at)
		VALUES (?,'code-comment-thread','fp-ccterr','new','t','t')`, prID); err == nil {
		t.Fatal("expected code-comment-thread file-NOT-NULL CHECK to survive the rebuild")
	}
}

// The v6 rebuild MUST NOT orphan or drop code_comment_message rows (FK child of
// feedback). This is the exact hazard the plan flags: DROP TABLE feedback with
// foreign_keys=ON cascades to code_comment_message.
func TestMigrate_V6PreservesCodeCommentMessages(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)

	// Seed a code-comment-thread feedback + a message (FK child).
	res, err := db.sql.Exec(`INSERT INTO feedback (pr_id,kind,fingerprint,status,file,created_at,updated_at)
		VALUES (?,'code-comment-thread','fp1','new','f.go','t','t')`, prID)
	if err != nil {
		t.Fatalf("seed feedback: %v", err)
	}
	fbID, _ := res.LastInsertId()
	if _, err := db.sql.Exec(`INSERT INTO code_comment_message (feedback_id,external_id,body)
		VALUES (?,'ext1','hello')`, fbID); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	// Re-running migrate (idempotent) must be a no-op and must NOT touch data.
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	// The message row survived the rebuild and still references its feedback.
	var cnt int
	if err := db.sql.QueryRow("SELECT COUNT(*) FROM code_comment_message WHERE feedback_id=?", fbID).Scan(&cnt); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("code_comment_message row lost across rebuild: cnt=%d", cnt)
	}

	// idx_feedback_pr was re-created.
	var idxName string
	if err := db.sql.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='index' AND name='idx_feedback_pr'",
	).Scan(&idxName); err != nil {
		t.Fatalf("idx_feedback_pr missing after rebuild: %v", err)
	}

	// No dangling FK references anywhere.
	rows, err := db.sql.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("foreign_key_check reported a violation after the feedback rebuild")
	}

	// The store's own UpsertFeedback still round-trips a self-review row.
	id, err := db.UpsertFeedback(ctx, Feedback{
		PRID: prID, Kind: "self-review", Fingerprint: "fp-upsert", Body: "b", IsOurs: true, AuthorKind: "agent",
	})
	if err != nil || id == 0 {
		t.Fatalf("UpsertFeedback(self-review): id=%d err=%v", id, err)
	}
}
