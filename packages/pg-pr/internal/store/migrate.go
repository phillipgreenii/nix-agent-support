package store

import "fmt"

// schemaVersion is the current schema. Bump it and append a migration step
// whenever the DDL changes. Stored in SQLite's user_version pragma.
const schemaVersion = 2

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
	return nil
}
