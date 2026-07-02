package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Framework tests
// ---------------------------------------------------------------------------

// TestDataMigrateTableExists verifies that Open creates the data_migration
// tracking table (added as schema v4).
func TestDataMigrateTableExists(t *testing.T) {
	db := newTestDB(t)
	var name string
	err := db.sql.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='data_migration'",
	).Scan(&name)
	if err != nil {
		t.Fatalf("data_migration table missing after Open: %v", err)
	}
}

// TestRunDataMigrations_EmptyRegistryIsNoOp ensures an empty registry
// succeeds without error and leaves the tracking table empty.
func TestRunDataMigrations_EmptyRegistryIsNoOp(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := RunDataMigrations(ctx, db, nil); err != nil {
		t.Fatalf("RunDataMigrations(empty): %v", err)
	}
	var n int
	_ = db.sql.QueryRow("SELECT COUNT(*) FROM data_migration").Scan(&n)
	if n != 0 {
		t.Fatalf("expected 0 rows in data_migration, got %d", n)
	}
}

// TestRunDataMigrations_RunsAndRecordsStep verifies that a migration step is
// executed exactly once and its completion is persisted.
func TestRunDataMigrations_RunsAndRecordsStep(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	calls := 0
	steps := []DataMigrationStep{
		{
			ID: "0001_test_step",
			Run: func(ctx context.Context, tx *Tx) error {
				calls++
				return nil
			},
		},
	}

	if err := RunDataMigrations(ctx, db, steps); err != nil {
		t.Fatalf("RunDataMigrations: %v", err)
	}
	if calls != 1 {
		t.Fatalf("step run %d time(s), want 1", calls)
	}

	// Verify the step is recorded.
	var id string
	var appliedAt string
	err := db.sql.QueryRow(
		"SELECT id, applied_at FROM data_migration WHERE id='0001_test_step'",
	).Scan(&id, &appliedAt)
	if err != nil {
		t.Fatalf("data_migration row missing: %v", err)
	}
	if id != "0001_test_step" {
		t.Fatalf("id = %q, want 0001_test_step", id)
	}
}

// TestRunDataMigrations_IdempotentRerun verifies that re-running
// RunDataMigrations does NOT call an already-applied step a second time.
func TestRunDataMigrations_IdempotentRerun(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	calls := 0
	steps := []DataMigrationStep{
		{
			ID: "0001_idempotent",
			Run: func(ctx context.Context, tx *Tx) error {
				calls++
				return nil
			},
		},
	}

	if err := RunDataMigrations(ctx, db, steps); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := RunDataMigrations(ctx, db, steps); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if calls != 1 {
		t.Fatalf("step run %d time(s) across two calls, want exactly 1", calls)
	}
}

// TestRunDataMigrations_AppliesOnlyPendingSteps verifies that only
// unapplied steps are executed when some are already recorded.
func TestRunDataMigrations_AppliesOnlyPendingSteps(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Pre-seed step A as already applied.
	_, err := db.sql.Exec(
		"INSERT INTO data_migration (id, applied_at) VALUES (?,?)",
		"0001_already_done", time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("seed data_migration: %v", err)
	}

	var bCalls int
	steps := []DataMigrationStep{
		{
			ID: "0001_already_done",
			Run: func(ctx context.Context, tx *Tx) error {
				t.Error("0001_already_done should not run again")
				return nil
			},
		},
		{
			ID: "0002_pending",
			Run: func(ctx context.Context, tx *Tx) error {
				bCalls++
				return nil
			},
		},
	}

	if err := RunDataMigrations(ctx, db, steps); err != nil {
		t.Fatalf("RunDataMigrations: %v", err)
	}
	if bCalls != 1 {
		t.Fatalf("0002_pending called %d time(s), want 1", bCalls)
	}
}

// TestRunDataMigrations_FailedRunIsNotRecorded verifies the transactional
// guarantee: if a step's Run returns an error AFTER doing partial SQL work, the
// entire transaction is rolled back — the partial work is undone AND the step
// is NOT recorded as applied. A subsequent call to RunDataMigrations must
// attempt the step again from scratch.
func TestRunDataMigrations_FailedRunIsNotRecorded(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	prID, _ := db.UpsertPR(ctx, PullRequest{
		Repo: "o/r", Number: 99, Ownership: "mine", State: "open",
	})

	// Seed a feedback row so the step has something to delete.
	_, err := db.sql.Exec(
		`
		INSERT INTO feedback
		  (pr_id, kind, fingerprint, status, title, body, file, created_at, updated_at)
		VALUES (?, 'code-comment-thread', 'fp-atomic', 'new', '', 'body', 'f.go',
		        '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z')`,
		prID,
	)
	if err != nil {
		t.Fatalf("seed feedback: %v", err)
	}

	// A step that deletes the seeded row then deliberately fails.
	runCalls := 0
	errBoom := fmt.Errorf("boom: intentional failure after partial work")
	steps := []DataMigrationStep{
		{
			ID: "0099_atomic_test",
			Run: func(ctx context.Context, tx *Tx) error {
				runCalls++
				// Do real SQL work inside the transaction.
				if _, err := tx.Exec(
					"DELETE FROM feedback WHERE pr_id=?", prID,
				); err != nil {
					return err
				}
				// Confirm the delete took effect inside the tx.
				var cnt int
				_ = tx.QueryRow(
					"SELECT COUNT(*) FROM feedback WHERE pr_id=?", prID,
				).Scan(&cnt)
				if cnt != 0 {
					t.Errorf("in-tx delete did not take effect: %d rows remain", cnt)
				}
				// Now deliberately fail — the whole tx must roll back.
				return errBoom
			},
		},
	}

	// First call: step runs but fails → must error, tx rolls back.
	if err := RunDataMigrations(ctx, db, steps); err == nil {
		t.Fatal("RunDataMigrations: expected an error, got nil")
	}

	// The partial DELETE must have been rolled back — row still exists.
	var cnt int
	_ = db.sql.QueryRow("SELECT COUNT(*) FROM feedback WHERE pr_id=?", prID).Scan(&cnt)
	if cnt != 1 {
		t.Fatalf("after failed step: expected 1 row (rollback), got %d", cnt)
	}

	// The step must NOT be recorded as applied.
	var recorded int
	_ = db.sql.QueryRow(
		"SELECT COUNT(*) FROM data_migration WHERE id='0099_atomic_test'",
	).Scan(&recorded)
	if recorded != 0 {
		t.Fatalf("failed step was incorrectly recorded in data_migration")
	}

	// A re-run must attempt the step again (runCalls increments to 2).
	if err := RunDataMigrations(ctx, db, steps); err == nil {
		t.Fatal("second RunDataMigrations: expected an error again, got nil")
	}
	if runCalls != 2 {
		t.Fatalf("step was called %d time(s) across two failing runs, want 2", runCalls)
	}
}

// ---------------------------------------------------------------------------
// First data migration: dedup PRRC-keyed feedback + backfill posted_at
// ---------------------------------------------------------------------------

// seedLegacyPRRCRow inserts a feedback row with an external_id that starts
// with "PRRC_" (legacy REST path) and one message with empty posted_at.
// prrcCommentID is the comment node id (e.g. "PRRC_aaa"); it becomes both
// the feedback.external_id and the sole message's external_id — exactly how
// the old REST ingest path wrote rows.
// Returns the feedback id.
func seedLegacyPRRCRow(t *testing.T, db *DB, prID int64, prrcCommentID, fingerprint string) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := db.UpsertFeedback(ctx, Feedback{
		PRID:        prID,
		Kind:        "code-comment-thread",
		ExternalID:  prrcCommentID,
		Fingerprint: fingerprint,
		Body:        "legacy comment",
		File:        "foo.go",
	})
	if err != nil {
		t.Fatalf("seedLegacyPRRCRow UpsertFeedback: %v", err)
	}
	// Insert message with NULL posted_at (legacy: no createdAt).
	// The message external_id is the comment node id — same as feedback.external_id.
	_, err = db.sql.Exec(
		`
		INSERT INTO code_comment_message
		  (feedback_id, external_id, body, posted_at)
		VALUES (?, ?, ?, NULL)`,
		id, prrcCommentID, "msg body",
	)
	if err != nil {
		t.Fatalf("seedLegacyPRRCRow message: %v", err)
	}
	return id
}

// seedPRRTRow inserts the canonical GraphQL-keyed feedback row (PRRT_…) for
// the same thread. prrtThreadID is the thread node id (e.g. "PRRT_aaa");
// prrcCommentID is the original comment node id that lives in the thread —
// it becomes the message external_id, establishing the link that the
// migration uses to pair the PRRC feedback row with this PRRT row.
// Returns the feedback id.
func seedPRRTRow(t *testing.T, db *DB, prID int64, prrtThreadID, prrcCommentID, fingerprint string) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := db.UpsertFeedback(ctx, Feedback{
		PRID:        prID,
		Kind:        "code-comment-thread",
		ExternalID:  prrtThreadID,
		Fingerprint: fingerprint,
		Body:        "canonical comment",
		File:        "foo.go",
	})
	if err != nil {
		t.Fatalf("seedPRRTRow UpsertFeedback: %v", err)
	}
	// The message external_id is the original comment node id (PRRC_).
	// This is the overlap the migration exploits to pair PRRC and PRRT rows.
	_, err = db.sql.Exec(
		`
		INSERT INTO code_comment_message
		  (feedback_id, external_id, body, posted_at)
		VALUES (?, ?, ?, ?)`,
		id, prrcCommentID, "msg body", "2024-01-15T10:00:00Z",
	)
	if err != nil {
		t.Fatalf("seedPRRTRow message: %v", err)
	}
	return id
}

// TestMigration0001_DedupLegacyFeedback verifies that after running the first
// data migration, PRRC-keyed feedback rows whose thread now exists under a
// PRRT-keyed row are deleted, leaving only the PRRT row.
func TestMigration0001_DedupLegacyFeedback(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	prID, _ := db.UpsertPR(ctx, PullRequest{
		Repo: "o/r", Number: 1, Ownership: "mine", State: "open",
	})

	// PRRC-keyed row (legacy) — different fingerprint because they're keyed differently.
	prrcFP := "fp-prrc-aaa"
	prrcID := seedLegacyPRRCRow(t, db, prID, "PRRC_aaa", prrcFP)

	// PRRT-keyed row for the SAME thread (canonical).
	// The message external_id "PRRC_aaa" pairs it to the PRRC row above.
	prrtFP := "fp-prrt-aaa"
	prrtID := seedPRRTRow(t, db, prID, "PRRT_aaa", "PRRC_aaa", prrtFP)

	// Sanity: both rows exist before migration.
	var cnt int
	_ = db.sql.QueryRow("SELECT COUNT(*) FROM feedback WHERE pr_id=?", prID).Scan(&cnt)
	if cnt != 2 {
		t.Fatalf("pre-migration: expected 2 feedback rows, got %d", cnt)
	}

	// Run migration.
	if err := db.InTx(ctx, func(tx *Tx) error {
		return DataMigration0001DedupLegacyFeedback(ctx, tx)
	}); err != nil {
		t.Fatalf("migration: %v", err)
	}

	// PRRC row must be gone, PRRT row must survive.
	var surviving int64
	err := db.sql.QueryRow("SELECT id FROM feedback WHERE pr_id=?", prID).Scan(&surviving)
	if err != nil {
		t.Fatalf("expected 1 feedback row after migration: %v", err)
	}
	if surviving != prrtID {
		t.Fatalf("surviving row id=%d, want PRRT id=%d", surviving, prrtID)
	}
	_ = prrcID // silenced: we just verified it's gone
}

// TestMigration0001_LeavesOrphanPRRC verifies that a PRRC-keyed feedback
// row with NO corresponding PRRT row is left untouched (we can only dedup
// rows where the canonical PRRT exists).
func TestMigration0001_LeavesOrphanPRRC(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	prID, _ := db.UpsertPR(ctx, PullRequest{
		Repo: "o/r", Number: 2, Ownership: "mine", State: "open",
	})

	// Only a PRRC row, no PRRT counterpart.
	seedLegacyPRRCRow(t, db, prID, "PRRC_orphan", "fp-prrc-orphan")

	if err := db.InTx(ctx, func(tx *Tx) error {
		return DataMigration0001DedupLegacyFeedback(ctx, tx)
	}); err != nil {
		t.Fatalf("migration: %v", err)
	}

	var cnt int
	_ = db.sql.QueryRow("SELECT COUNT(*) FROM feedback WHERE pr_id=?", prID).Scan(&cnt)
	if cnt != 1 {
		t.Fatalf("orphan PRRC row was wrongly deleted; got %d rows, want 1", cnt)
	}
}

// TestMigration0001_BackfillsPostedAt verifies that code_comment_message rows
// with NULL posted_at get backfilled with the parent feedback row's created_at.
func TestMigration0001_BackfillsPostedAt(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	prID, _ := db.UpsertPR(ctx, PullRequest{
		Repo: "o/r", Number: 3, Ownership: "mine", State: "open",
	})

	// Insert a feedback row with a known created_at so we can verify the backfill.
	const knownCreatedAt = "2024-03-01T08:00:00Z"
	_, err := db.sql.Exec(
		`
		INSERT INTO feedback
		  (pr_id, kind, fingerprint, status, title, body, file, created_at, updated_at)
		VALUES (?, 'code-comment-thread', 'fp-backfill', 'new', '', 'body', 'f.go', ?, ?)`,
		prID, knownCreatedAt, knownCreatedAt,
	)
	if err != nil {
		t.Fatalf("seed feedback: %v", err)
	}
	var fbID int64
	_ = db.sql.QueryRow("SELECT id FROM feedback WHERE fingerprint='fp-backfill'").Scan(&fbID)

	// Insert message with NULL posted_at.
	_, err = db.sql.Exec(
		`
		INSERT INTO code_comment_message
		  (feedback_id, external_id, body, posted_at)
		VALUES (?, 'ext-backfill', 'msg', NULL)`,
		fbID,
	)
	if err != nil {
		t.Fatalf("seed message: %v", err)
	}

	if err := db.InTx(ctx, func(tx *Tx) error {
		return DataMigration0001DedupLegacyFeedback(ctx, tx)
	}); err != nil {
		t.Fatalf("migration: %v", err)
	}

	var postedAt string
	err = db.sql.QueryRow(
		"SELECT posted_at FROM code_comment_message WHERE feedback_id=?", fbID,
	).Scan(&postedAt)
	if err != nil {
		t.Fatalf("query posted_at: %v", err)
	}
	if postedAt != knownCreatedAt {
		t.Fatalf("posted_at = %q, want %q", postedAt, knownCreatedAt)
	}
}

// TestMigration0001_DoesNotOverwriteExistingPostedAt verifies that messages
// that already have a non-NULL posted_at are left unchanged.
func TestMigration0001_DoesNotOverwriteExistingPostedAt(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	prID, _ := db.UpsertPR(ctx, PullRequest{
		Repo: "o/r", Number: 4, Ownership: "mine", State: "open",
	})

	const origCreatedAt = "2024-01-01T00:00:00Z"
	const existingPostedAt = "2024-01-10T12:00:00Z"

	_, err := db.sql.Exec(
		`
		INSERT INTO feedback
		  (pr_id, kind, fingerprint, status, title, body, file, created_at, updated_at)
		VALUES (?, 'code-comment-thread', 'fp-existing', 'new', '', 'body', 'f.go', ?, ?)`,
		prID, origCreatedAt, origCreatedAt,
	)
	if err != nil {
		t.Fatalf("seed feedback: %v", err)
	}
	var fbID int64
	_ = db.sql.QueryRow("SELECT id FROM feedback WHERE fingerprint='fp-existing'").Scan(&fbID)

	_, err = db.sql.Exec(
		`
		INSERT INTO code_comment_message
		  (feedback_id, external_id, body, posted_at)
		VALUES (?, 'ext-existing', 'msg', ?)`,
		fbID, existingPostedAt,
	)
	if err != nil {
		t.Fatalf("seed message: %v", err)
	}

	if err := db.InTx(ctx, func(tx *Tx) error {
		return DataMigration0001DedupLegacyFeedback(ctx, tx)
	}); err != nil {
		t.Fatalf("migration: %v", err)
	}

	var postedAt string
	_ = db.sql.QueryRow(
		"SELECT posted_at FROM code_comment_message WHERE feedback_id=?", fbID,
	).Scan(&postedAt)
	if postedAt != existingPostedAt {
		t.Fatalf("posted_at changed: got %q, want %q", postedAt, existingPostedAt)
	}
}

// TestMigration0001_Idempotent verifies that running the migration twice
// produces the same result without error.
func TestMigration0001_Idempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	prID, _ := db.UpsertPR(ctx, PullRequest{
		Repo: "o/r", Number: 5, Ownership: "mine", State: "open",
	})

	seedLegacyPRRCRow(t, db, prID, "PRRC_dup", "fp-prrc-dup")
	seedPRRTRow(t, db, prID, "PRRT_dup", "PRRC_dup", "fp-prrt-dup")

	if err := db.InTx(ctx, func(tx *Tx) error {
		return DataMigration0001DedupLegacyFeedback(ctx, tx)
	}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := db.InTx(ctx, func(tx *Tx) error {
		return DataMigration0001DedupLegacyFeedback(ctx, tx)
	}); err != nil {
		t.Fatalf("second run (idempotent): %v", err)
	}

	var cnt int
	_ = db.sql.QueryRow("SELECT COUNT(*) FROM feedback WHERE pr_id=?", prID).Scan(&cnt)
	if cnt != 1 {
		t.Fatalf("after idempotent run: expected 1 feedback row, got %d", cnt)
	}
}

// TestMigration0001_MultipleThreadsSamePR verifies per-thread dedup when a
// PR has multiple threads, some with PRRC+PRRT pairs and some with only PRRT.
func TestMigration0001_MultipleThreadsSamePR(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	prID, _ := db.UpsertPR(ctx, PullRequest{
		Repo: "o/r", Number: 6, Ownership: "mine", State: "open",
	})

	// Thread A: PRRC + PRRT pair → PRRC should be deleted.
	// The PRRT message carries external_id="PRRC_a", linking it to PRRC_a.
	seedLegacyPRRCRow(t, db, prID, "PRRC_a", "fp-prrc-a")
	seedPRRTRow(t, db, prID, "PRRT_a", "PRRC_a", "fp-prrt-a")

	// Thread B: PRRT only (no PRRC counterpart) → must survive.
	// Use a distinct message external_id so it does not accidentally pair.
	seedPRRTRow(t, db, prID, "PRRT_b", "PRRC_b_unrelated", "fp-prrt-b")

	// Thread C: PRRC only (no PRRT counterpart) → must survive.
	seedLegacyPRRCRow(t, db, prID, "PRRC_c", "fp-prrc-c")

	// Pre-condition: 4 rows.
	var pre int
	_ = db.sql.QueryRow("SELECT COUNT(*) FROM feedback WHERE pr_id=?", prID).Scan(&pre)
	if pre != 4 {
		t.Fatalf("pre-migration: expected 4 rows, got %d", pre)
	}

	if err := db.InTx(ctx, func(tx *Tx) error {
		return DataMigration0001DedupLegacyFeedback(ctx, tx)
	}); err != nil {
		t.Fatalf("migration: %v", err)
	}

	// Post-condition: 3 rows (PRRC_a deleted; PRRT_a, PRRT_b, PRRC_c survive).
	var post int
	_ = db.sql.QueryRow("SELECT COUNT(*) FROM feedback WHERE pr_id=?", prID).Scan(&post)
	if post != 3 {
		t.Fatalf("post-migration: expected 3 rows, got %d", post)
	}

	// PRRC_a specifically must be gone.
	var gone int
	_ = db.sql.QueryRow(
		"SELECT COUNT(*) FROM feedback WHERE external_id='PRRC_a'",
	).Scan(&gone)
	if gone != 0 {
		t.Fatalf("PRRC_a should be deleted, but still present")
	}
}
