package store

import (
	"fmt"
	"strings"
)

// schemaVersion is the current schema. Bump it and append a migration step
// whenever the DDL changes. Stored in SQLite's user_version pragma.
const schemaVersion = 7

// migrations is the ordered list of DDL applied to reach schemaVersion. Index i
// migrates user_version i -> i+1.
var migrations = []string{
	// v0 -> v1: initial schema.
	`
CREATE TABLE pull_request (
    id             INTEGER PRIMARY KEY,
    repo           TEXT NOT NULL,
    number         INTEGER NOT NULL,
    ownership      TEXT NOT NULL CHECK (ownership IN ('mine','team')),
    author         TEXT,
    state          TEXT NOT NULL,
    branch         TEXT,
    base           TEXT,
    url            TEXT,
    head_sha       TEXT,
    last_synced_at TEXT,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    UNIQUE (repo, number)
);

CREATE TABLE feedback (
    id                 INTEGER PRIMARY KEY,
    pr_id              INTEGER NOT NULL REFERENCES pull_request(id) ON DELETE CASCADE,
    kind               TEXT NOT NULL CHECK (kind IN
                         ('code-comment-thread','pr-comments','ci-failure','review-request','jira-link')),
    external_id        TEXT,
    fingerprint        TEXT NOT NULL,
    status             TEXT NOT NULL DEFAULT 'new' CHECK (status IN
                         ('new','presented','dispositioned','replied','resolved','superseded')),
    title              TEXT,
    body               TEXT,

    subject_sha        TEXT,
    first_seen_head_sha TEXT,
    is_outdated        INTEGER NOT NULL DEFAULT 0,
    is_minimized       INTEGER NOT NULL DEFAULT 0,
    minimized_reason   TEXT,

    author_login       TEXT,
    author_kind        TEXT CHECK (author_kind IS NULL OR author_kind IN ('human','agent')),
    agent_name         TEXT,
    is_ours            INTEGER NOT NULL DEFAULT 0,
    author_role        TEXT,

    disposition_action TEXT CHECK (disposition_action IS NULL OR disposition_action IN
                         ('will-fix','wont-fix','no-action')),
    disposition_note   TEXT,
    reply_body         TEXT,
    response_id        TEXT,
    severity           TEXT,
    managed_upstream   INTEGER NOT NULL DEFAULT 0,

    file               TEXT,
    line               INTEGER,
    thread_resolved    INTEGER,
    comment_node_id    TEXT,
    run_id             TEXT,
    check_name         TEXT,
    conclusion         TEXT,
    related            INTEGER,
    retry_count        INTEGER,
    link               TEXT,

    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    resolved_at        TEXT,

    UNIQUE (pr_id, fingerprint),
    CHECK (kind <> 'code-comment-thread' OR file IS NOT NULL)
);
CREATE INDEX idx_feedback_pr ON feedback(pr_id);

CREATE TABLE code_comment_message (
    id           INTEGER PRIMARY KEY,
    feedback_id  INTEGER NOT NULL REFERENCES feedback(id) ON DELETE CASCADE,
    external_id  TEXT NOT NULL,
    author_login TEXT,
    author_kind  TEXT,
    agent_name   TEXT,
    is_ours      INTEGER NOT NULL DEFAULT 0,
    author_role  TEXT,
    body         TEXT,
    posted_at    TEXT,
    UNIQUE (feedback_id, external_id)
);

CREATE TABLE outbox (
    id           INTEGER PRIMARY KEY,
    type         TEXT NOT NULL,
    payload      TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','complete')),
    created_at   TEXT NOT NULL,
    completed_at TEXT
);
CREATE INDEX idx_outbox_pending ON outbox(id) WHERE status = 'pending';
`,
	// v1 -> v2: PR enrichment columns (kind/languages/size/urgency). One
	// column per ALTER (SQLite limit); defaults backfill existing rows so
	// the new Scan targets are never NULL.
	`
ALTER TABLE pull_request ADD COLUMN kind            TEXT    NOT NULL DEFAULT '';
ALTER TABLE pull_request ADD COLUMN languages       TEXT    NOT NULL DEFAULT '[]';
ALTER TABLE pull_request ADD COLUMN size            TEXT    NOT NULL DEFAULT '';
ALTER TABLE pull_request ADD COLUMN urgency         TEXT    NOT NULL DEFAULT '';
ALTER TABLE pull_request ADD COLUMN urgency_score   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE pull_request ADD COLUMN urgency_reasons TEXT    NOT NULL DEFAULT '[]';
`,
	// v2 -> v3: per-PR revision timeline (head/base SHA + compact CI rollup +
	// my-submitted-review marker). Append-only; one writer (sync).
	`
CREATE TABLE pr_revision (
    id              INTEGER PRIMARY KEY,
    pr_id           INTEGER NOT NULL REFERENCES pull_request(id) ON DELETE CASCADE,
    seq             INTEGER NOT NULL,
    head_sha        TEXT NOT NULL,
    base_sha        TEXT,
    observed_at     TEXT NOT NULL,
    last_seen_at    TEXT NOT NULL,
    ci_state        TEXT NOT NULL DEFAULT 'none'
                      CHECK (ci_state IN ('none','pending','success','failure','error')),
    ci_passed       INTEGER NOT NULL DEFAULT 0,
    ci_failed       INTEGER NOT NULL DEFAULT 0,
    ci_pending      INTEGER NOT NULL DEFAULT 0,
    ci_captured_at  TEXT,
    reviewed_at     TEXT,
    my_review_state TEXT CHECK (my_review_state IS NULL OR
                      my_review_state IN ('approved','changes-requested','commented')),
    UNIQUE (pr_id, seq)
);
CREATE INDEX idx_pr_revision_pr ON pr_revision(pr_id);
`,
	// v3 -> v4: data migration tracking table. Records which one-shot data
	// migrations have been applied (keyed by string ID), so RunDataMigrations
	// can skip already-applied steps on re-run.
	`
CREATE TABLE data_migration (
    id         TEXT PRIMARY KEY,
    applied_at TEXT NOT NULL
);
`,
	// v4 -> v5: per-revision agent-review marker (pg2-4c5i.36). Stamped by the
	// daemon's draft-review consumer when it closes a review, recording the head
	// SHA the agent review was produced against. The re-review gate re-triggers
	// production when LatestRevision.HeadSHA post-dates the stamped revision.
	// Distinct from reviewed_at/my_review_state (that is *my submitted GitHub
	// review*, different semantics).
	`
ALTER TABLE pr_revision ADD COLUMN reviewed_by_agent_at TEXT;
`,
	// v5 -> v6: add the 'self-review' feedback kind (pg2-4c5i.34 my-PR sink).
	// feedback.kind carries a hard column CHECK; SQLite cannot alter a column
	// CHECK in place, so the table is rebuilt (the SQLite-recommended
	// "12-step ALTER" pattern). The new CHECK adds 'self-review' and keeps the
	// code-comment-thread file-NOT-NULL guard (self-review carries no such
	// constraint, so PR-level fileless self-review findings are legal). The
	// UNIQUE(pr_id, fingerprint) is preserved (idempotent re-ingest key) and
	// idx_feedback_pr is re-created after the rename.
	//
	// NOTE: the rebuild DROPs feedback, which has an ON DELETE CASCADE child
	// (code_comment_message). foreign_keys MUST be OFF during this migration or
	// the DROP cascades and orphans the messages — applyMigration disables FKs
	// around the migration tx for exactly this reason.
	`
CREATE TABLE feedback_new (
    id                 INTEGER PRIMARY KEY,
    pr_id              INTEGER NOT NULL REFERENCES pull_request(id) ON DELETE CASCADE,
    kind               TEXT NOT NULL CHECK (kind IN
                         ('code-comment-thread','pr-comments','ci-failure','review-request','jira-link','self-review')),
    external_id        TEXT,
    fingerprint        TEXT NOT NULL,
    status             TEXT NOT NULL DEFAULT 'new' CHECK (status IN
                         ('new','presented','dispositioned','replied','resolved','superseded')),
    title              TEXT,
    body               TEXT,

    subject_sha        TEXT,
    first_seen_head_sha TEXT,
    is_outdated        INTEGER NOT NULL DEFAULT 0,
    is_minimized       INTEGER NOT NULL DEFAULT 0,
    minimized_reason   TEXT,

    author_login       TEXT,
    author_kind        TEXT CHECK (author_kind IS NULL OR author_kind IN ('human','agent')),
    agent_name         TEXT,
    is_ours            INTEGER NOT NULL DEFAULT 0,
    author_role        TEXT,

    disposition_action TEXT CHECK (disposition_action IS NULL OR disposition_action IN
                         ('will-fix','wont-fix','no-action')),
    disposition_note   TEXT,
    reply_body         TEXT,
    response_id        TEXT,
    severity           TEXT,
    managed_upstream   INTEGER NOT NULL DEFAULT 0,

    file               TEXT,
    line               INTEGER,
    thread_resolved    INTEGER,
    comment_node_id    TEXT,
    run_id             TEXT,
    check_name         TEXT,
    conclusion         TEXT,
    related            INTEGER,
    retry_count        INTEGER,
    link               TEXT,

    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    resolved_at        TEXT,

    UNIQUE (pr_id, fingerprint),
    CHECK (kind <> 'code-comment-thread' OR file IS NOT NULL)
);
INSERT INTO feedback_new SELECT * FROM feedback;
DROP TABLE feedback;
ALTER TABLE feedback_new RENAME TO feedback;
CREATE INDEX idx_feedback_pr ON feedback(pr_id);
`,
	// v6 -> v7: per-revision others-approved marker (pg2-4c5i.13). Records when a
	// NON-SELF (teammate) APPROVED review is observed at a revision's head SHA, so
	// the attention predicate ("someone else approved") is store-derived rather
	// than computed live in snapshot.classifyApprovals (which conflated the
	// viewer's own approval with a teammate's — X3). Additive ALTER ADD COLUMN with
	// defaults; existing rows backfill to 0 / NULL. Distinct from
	// reviewed_at/my_review_state (that is *my* submitted GitHub review).
	`
ALTER TABLE pr_revision ADD COLUMN others_approved    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE pr_revision ADD COLUMN others_approved_at TEXT;
`,
}

// migrate brings the DB up to schemaVersion. It runs each pending migration in
// its own transaction. If the DB is newer than this binary it refuses (returns
// an error) rather than writing against a schema it doesn't understand.
func migrate(db *DB) error {
	var current int
	if err := db.sql.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if current > schemaVersion {
		return fmt.Errorf("db schema version %d is newer than this binary (%d); upgrade pg-pr", current, schemaVersion)
	}
	for current < schemaVersion {
		stmt := migrations[current]
		if err := applyMigration(db, current+1, stmt); err != nil {
			return err
		}
		current++
	}
	return nil
}

func applyMigration(db *DB, toVersion int, ddl string) error {
	// Disable foreign-key enforcement for the duration of the migration.
	// SQLite's "12-step ALTER" table-rebuild pattern (used by v6 to change the
	// feedback.kind CHECK) DROPs a table that has ON DELETE CASCADE children
	// (code_comment_message → feedback); with FKs on, the DROP would cascade and
	// orphan those rows. The pragma cannot be toggled inside a transaction, so
	// it is set here (outside the tx) and restored after commit. Additive
	// migrations are unaffected. The runtime DSN keeps foreign_keys ON.
	if _, err := db.sql.Exec("PRAGMA foreign_keys = OFF"); err != nil {
		return fmt.Errorf("disable foreign_keys for migration to v%d: %w", toVersion, err)
	}
	defer func() { _, _ = db.sql.Exec("PRAGMA foreign_keys = ON") }()

	tx, err := db.sql.Begin()
	if err != nil {
		return fmt.Errorf("begin migration to v%d: %w", toVersion, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(ddl); err != nil {
		return fmt.Errorf("apply migration to v%d: %w", toVersion, err)
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", toVersion)); err != nil {
		return fmt.Errorf("set user_version=%d: %w", toVersion, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration to v%d: %w", toVersion, err)
	}

	// After commit (and while foreign_keys is still OFF), run
	// PRAGMA foreign_key_check to verify the migration did not introduce any
	// dangling FK references. A bad migration fails loudly here rather than
	// silently corrupting referential integrity. The check runs on db.sql
	// (outside the now-committed tx) so it sees the fully-committed state.
	fkRows, err := db.sql.Query("PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("foreign_key_check after migration to v%d: %w", toVersion, err)
	}
	defer func() { _ = fkRows.Close() }()
	var violations []string
	for fkRows.Next() {
		// Columns: table, rowid, parent, fkid
		var table, parent string
		var rowid, fkid int64
		if scanErr := fkRows.Scan(&table, &rowid, &parent, &fkid); scanErr != nil {
			return fmt.Errorf("scan foreign_key_check row after migration to v%d: %w", toVersion, scanErr)
		}
		violations = append(violations, fmt.Sprintf("%s rowid=%d → %s fk#%d", table, rowid, parent, fkid))
	}
	if err := fkRows.Err(); err != nil {
		return fmt.Errorf("foreign_key_check iteration after migration to v%d: %w", toVersion, err)
	}
	if len(violations) > 0 {
		return fmt.Errorf("migration to v%d introduced foreign key violations: %s",
			toVersion, strings.Join(violations, "; "))
	}
	return nil
}
