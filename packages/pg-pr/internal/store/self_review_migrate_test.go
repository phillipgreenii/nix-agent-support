package store

import (
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

// TestMigrate_V6PreservesCodeCommentMessages actually exercises the v5->v6
// feedback table-rebuild (migrate.go: widen the feedback.kind CHECK to add
// 'self-review' via the SQLite 12-step ALTER) with an FK-bearing child PRESENT.
// The rebuild DROPs feedback, which has an ON DELETE CASCADE child
// (code_comment_message); applyMigration disables foreign_keys around the tx so
// the DROP must NOT cascade and orphan the message. This is the exact hazard the
// plan flags: DROP TABLE feedback with foreign_keys=ON cascades to
// code_comment_message.
//
// Mirrors TestMigrate_V8PreservesFKChildren (pg2-2ozt3): after OpenForTest the
// DB is already at v8, so a second migrate() is a NO-OP (the
// `for current < schemaVersion` loop doesn't run) and the rebuild never re-runs
// against the seeded rows — the earlier version of this test proved only that a
// no-op migrate leaves data alone. The v8 sibling rolls user_version back to N-1
// and calls migrate(), but that works ONLY because v8 is the TERMINAL migration.
// v6 is NOT terminal: rolling user_version to 5 and calling migrate() would run
// migrations[5] (the v6 rebuild) and THEN migrations[6] (v6->v7 ADD COLUMN
// others_approved), and the latter fails with "duplicate column name" because
// the table already carries the v8 columns. So this test rolls the version
// COUNTER back to 5 and re-runs ONLY the v6 rebuild DDL via
// applyMigration(db, 6, migrations[5]) against the populated table — exactly the
// data-bearing step of a production v5->v6 upgrade.
func TestMigrate_V6PreservesCodeCommentMessages(t *testing.T) {
	db := OpenForTest(t) // full v8 schema present
	prID := seedPR(t, db)

	// Seed a code-comment-thread feedback parent + a code_comment_message child
	// (FK child, ON DELETE CASCADE).
	//
	// Use an EXPLICIT non-1 id (42): if a regression dropped `id` from the v6
	// INSERT...SELECT, the single copied row would be re-assigned rowid 1, so a
	// child keyed on feedback_id=1 would still resolve and hide the bug. Seeding
	// id=42 makes both the id-preservation lookup and the child FK link
	// load-bearing: an id-renumbering rebuild leaves feedback_id=42 dangling.
	const fbID int64 = 42
	if _, err := db.sql.Exec(`INSERT INTO feedback
		(id,pr_id,kind,fingerprint,status,file,created_at,updated_at)
		VALUES (?,?,'code-comment-thread','fp1','new','f.go','t','t')`, fbID, prID); err != nil {
		t.Fatalf("seed feedback parent: %v", err)
	}
	if _, err := db.sql.Exec(`INSERT INTO code_comment_message (feedback_id,external_id,body)
		VALUES (?,'ext1','hello')`, fbID); err != nil {
		t.Fatalf("seed code_comment_message child: %v", err)
	}

	// Force the v6 feedback rebuild to re-run against the seeded rows. Rolling the
	// counter to 5 then applying ONLY migrations[5] re-runs the v5->v6 rebuild DDL
	// (see the doc comment for why a full migrate() cannot be used here).
	if _, err := db.sql.Exec("PRAGMA user_version = 5"); err != nil {
		t.Fatalf("roll user_version back to 5: %v", err)
	}
	if err := applyMigration(db, 6, migrations[5]); err != nil {
		t.Fatalf("re-run v6 feedback rebuild over seeded data: %v", err)
	}
	// Evidence the rebuild actually ran (not a no-op): applyMigration only bumps
	// user_version to 6 after the v6 rebuild DDL commits.
	var uv int
	if err := db.sql.QueryRow("PRAGMA user_version").Scan(&uv); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if uv != 6 {
		t.Fatalf("v6 rebuild did not run: user_version=%d, want 6", uv)
	}

	// Parent feedback survived with the SAME id and its row data intact.
	var gotPRID int64
	var gotFile string
	if err := db.sql.QueryRow(
		"SELECT pr_id, file FROM feedback WHERE id=?", fbID,
	).Scan(&gotPRID, &gotFile); err != nil {
		t.Fatalf("feedback parent lost across rebuild (id=%d): %v", fbID, err)
	}
	if gotPRID != prID || gotFile != "f.go" {
		t.Fatalf("feedback row not preserved: pr_id=%d file=%q, want pr_id=%d file=f.go", gotPRID, gotFile, prID)
	}

	// code_comment_message child survived with its FK link AND row data intact.
	var msgFBID int64
	var msgBody string
	if err := db.sql.QueryRow(
		"SELECT feedback_id, body FROM code_comment_message WHERE external_id='ext1'",
	).Scan(&msgFBID, &msgBody); err != nil {
		t.Fatalf("code_comment_message child lost across rebuild: %v", err)
	}
	if msgFBID != fbID {
		t.Fatalf("code_comment_message FK link broken: feedback_id=%d, want %d", msgFBID, fbID)
	}
	if msgBody != "hello" {
		t.Fatalf("code_comment_message row data not preserved: body=%q, want hello", msgBody)
	}

	// idx_feedback_pr was re-created after the rename.
	var idxName string
	if err := db.sql.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='index' AND name='idx_feedback_pr'",
	).Scan(&idxName); err != nil {
		t.Fatalf("idx_feedback_pr missing after rebuild: %v", err)
	}

	// No dangling FK references anywhere after the rebuild.
	rows, err := db.sql.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("foreign_key_check reported a violation after the feedback rebuild")
	}
}
