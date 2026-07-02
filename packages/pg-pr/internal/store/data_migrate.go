package store

import (
	"context"
	"fmt"
)

// DataMigrationStep is one idempotent data-migration step. ID must be unique
// across all registered migrations; by convention use a zero-padded sequence
// prefix, e.g. "0001_dedup_legacy_feedback". Run is called at most once per
// store; once applied, the step is recorded in data_migration and skipped on
// all future RunDataMigrations calls.
type DataMigrationStep struct {
	ID  string
	Run func(ctx context.Context, db *DB) error
}

// RegisteredDataMigrations is the ordered list of all data migrations known to
// this binary. Add new steps here; never reorder or remove existing entries.
var RegisteredDataMigrations = []DataMigrationStep{
	{
		ID:  "0001_dedup_legacy_feedback_backfill_posted_at",
		Run: DataMigration0001DedupLegacyFeedback,
	},
}

// PendingDataMigrations returns the subset of steps that have not yet been
// applied to db. The order of the returned slice matches the input order.
func PendingDataMigrations(ctx context.Context, db *DB, steps []DataMigrationStep) ([]DataMigrationStep, error) {
	var pending []DataMigrationStep
	for _, step := range steps {
		applied, err := isDataMigrationApplied(ctx, db, step.ID)
		if err != nil {
			return nil, fmt.Errorf("data migrate: check %s: %w", step.ID, err)
		}
		if !applied {
			pending = append(pending, step)
		}
	}
	return pending, nil
}

// RunDataMigrations applies any pending data migrations from steps in order.
// Each step is skipped if its ID is already recorded in the data_migration
// table, making the whole call idempotent. A nil or empty steps slice is
// valid (no-op).
func RunDataMigrations(ctx context.Context, db *DB, steps []DataMigrationStep) error {
	for _, step := range steps {
		applied, err := isDataMigrationApplied(ctx, db, step.ID)
		if err != nil {
			return fmt.Errorf("data migrate: check %s: %w", step.ID, err)
		}
		if applied {
			continue
		}
		if err := step.Run(ctx, db); err != nil {
			return fmt.Errorf("data migrate: run %s: %w", step.ID, err)
		}
		if err := recordDataMigration(ctx, db, step.ID); err != nil {
			return fmt.Errorf("data migrate: record %s: %w", step.ID, err)
		}
	}
	return nil
}

// isDataMigrationApplied returns true if id is already in data_migration.
func isDataMigrationApplied(ctx context.Context, db *DB, id string) (bool, error) {
	var cnt int
	err := db.sql.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM data_migration WHERE id=?", id,
	).Scan(&cnt)
	if err != nil {
		return false, err
	}
	return cnt > 0, nil
}

// recordDataMigration inserts a row into data_migration for id.
func recordDataMigration(ctx context.Context, db *DB, id string) error {
	_, err := db.sql.ExecContext(ctx,
		"INSERT INTO data_migration (id, applied_at) VALUES (?,?)",
		id, nowRFC3339(),
	)
	return err
}

// DataMigration0001DedupLegacyFeedback is the first data migration. It:
//
//  1. Deletes feedback rows whose external_id starts with "PRRC_" (legacy
//     REST per-PR path) when a corresponding row keyed with "PRRT_" (GraphQL
//     review-thread node id) exists for the same PR. Cascades to
//     code_comment_message via ON DELETE CASCADE. Rows with no PRRT
//     counterpart are left untouched.
//
//  2. Backfills code_comment_message.posted_at for rows where it is NULL
//     by copying the parent feedback row's created_at. This covers messages
//     ingested before the GraphQL createdAt fix (pg2-re7e).
//
// The function is idempotent: running it against a store that has already been
// cleaned up is a no-op.
func DataMigration0001DedupLegacyFeedback(ctx context.Context, db *DB) error {
	// Step 1: delete PRRC-keyed rows that have a PRRT counterpart.
	//
	// Matching strategy: the legacy REST path wrote a feedback row whose
	// external_id is the comment node id (PRRC_xxx). The GraphQL path writes a
	// feedback row whose external_id is the review-thread node id (PRRT_yyy)
	// AND inserts a code_comment_message row with external_id = PRRC_xxx
	// (the original comment id). We exploit this overlap: a PRRC-keyed
	// feedback row is a legacy duplicate iff there exists a
	// code_comment_message in the same DB whose external_id matches the PRRC
	// feedback row's external_id, and that message belongs to a PRRT-keyed
	// feedback row on the same PR.
	//
	// This is exact: it pairs each PRRC row with the specific PRRT row that
	// contains its comment, avoiding false-positive matches across different
	// threads at the same file/line.
	deleteStmt := `
DELETE FROM feedback
WHERE external_id LIKE 'PRRC_%'
  AND kind = 'code-comment-thread'
  AND EXISTS (
      SELECT 1
      FROM code_comment_message AS m
      JOIN feedback             AS prrt ON prrt.id = m.feedback_id
      WHERE m.external_id     = feedback.external_id
        AND prrt.external_id  LIKE 'PRRT_%'
        AND prrt.kind         = 'code-comment-thread'
        AND prrt.pr_id        = feedback.pr_id
  )`
	if _, err := db.sql.ExecContext(ctx, deleteStmt); err != nil {
		return fmt.Errorf("0001 dedup: delete PRRC rows: %w", err)
	}

	// Step 2: backfill NULL posted_at on code_comment_message using the
	// parent feedback row's created_at.
	//
	// Only touches rows where posted_at IS NULL; existing non-NULL values are
	// preserved. Uses a correlated subquery so a single UPDATE covers all
	// affected messages regardless of their feedback row's kind.
	// Normalize: treat empty-string posted_at the same as NULL.
	backfillStmt := `
UPDATE code_comment_message
SET posted_at = (
    SELECT f.created_at
    FROM feedback f
    WHERE f.id = code_comment_message.feedback_id
)
WHERE posted_at IS NULL OR posted_at = ''`

	if _, err := db.sql.ExecContext(ctx, backfillStmt); err != nil {
		return fmt.Errorf("0001 dedup: backfill posted_at: %w", err)
	}

	return nil
}
