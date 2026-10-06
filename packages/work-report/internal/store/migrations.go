package store

import (
	"fmt"
	"strconv"
)

// schemaVersion is the top of the migration ladder below, stored in SQLite's
// user_version pragma and mirrored into meta's "schema_version" key.
const schemaVersion = 1

// migrations is the ordered list of DDL applied to reach schemaVersion.
// Index i migrates user_version i -> i+1. Entries are append-only: nothing in
// this package updates or deletes an entry row (INV-APPEND-1).
//
// Timestamps are stored as fixed-width UTC text (see formatTime) so that
// lexicographic order is chronological order; the latest-wins view and the
// range filters depend on it.
var migrations = []string{
	// v0 -> v1: initial schema.
	`
CREATE TABLE entry (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    id           TEXT NOT NULL,
    external_id  TEXT NOT NULL DEFAULT '',
    source_id    TEXT NOT NULL DEFAULT '',
    type         TEXT NOT NULL DEFAULT '',
    occurred_at  TEXT NOT NULL,
    ingested_at  TEXT NOT NULL,
    summary      TEXT NOT NULL DEFAULT '',
    url          TEXT NOT NULL DEFAULT '',
    labels       TEXT NOT NULL DEFAULT '[]',
    fields       TEXT NOT NULL DEFAULT '{}',
    content_hash TEXT NOT NULL
);
CREATE INDEX entry_id          ON entry (id);
CREATE INDEX entry_occurred_at ON entry (occurred_at);
CREATE INDEX entry_type        ON entry (type);
CREATE INDEX entry_source_id   ON entry (source_id);

CREATE TABLE entry_label (
    seq   INTEGER NOT NULL REFERENCES entry (seq),
    label TEXT NOT NULL,
    PRIMARY KEY (seq, label)
);
CREATE INDEX entry_label_label ON entry_label (label);

CREATE TABLE pull (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    source     TEXT NOT NULL,
    since      TEXT NOT NULL DEFAULT '',
    before     TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    ended_at   TEXT NOT NULL,
    status     TEXT NOT NULL,
    count      INTEGER NOT NULL DEFAULT 0,
    unchanged  INTEGER NOT NULL DEFAULT 0,
    rejected   INTEGER NOT NULL DEFAULT 0,
    truncated  INTEGER NOT NULL DEFAULT 0,
    reason     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX pull_source ON pull (source);

CREATE TABLE report (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    since        TEXT NOT NULL DEFAULT '',
    before       TEXT NOT NULL DEFAULT '',
    kind         TEXT NOT NULL,
    narrowing    TEXT NOT NULL DEFAULT '',
    generated_at TEXT NOT NULL,
    generator    TEXT NOT NULL DEFAULT '',
    content      TEXT NOT NULL DEFAULT ''
);

CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- INV-LATEST-1: per id, the row with the greatest (occurred_at, ingested_at,
-- seq). Every read of entries goes through this view.
CREATE VIEW entry_latest AS
SELECT e.* FROM entry e
WHERE NOT EXISTS (
    SELECT 1 FROM entry n
    WHERE n.id = e.id
      AND (n.occurred_at, n.ingested_at, n.seq) > (e.occurred_at, e.ingested_at, e.seq)
);
`,
}

// migrate applies pending migrations (comparing user_version against the
// ladder) and mirrors the resulting version into meta's "schema_version".
// A database newer than this binary understands is refused.
func migrate(s *Store) error {
	var current int
	if err := s.sql.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if current > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than this binary supports (%d)", current, schemaVersion)
	}
	for i := current; i < len(migrations); i++ {
		tx, err := s.sql.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("set user_version %d: %w", i+1, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO meta (key, value) VALUES ('schema_version', ?)
			 ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
			strconv.Itoa(i+1),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record schema_version %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", i+1, err)
		}
	}
	return nil
}
