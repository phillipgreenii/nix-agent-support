package store

import (
	"context"
	"fmt"
)

// The schema cutover: version 1 -> version 2 of the store, applied
// explicitly by `pg-desk migrate --cutover` and by nothing else.
//
// It is kept apart from the migration ladder (migrations.go) on purpose.
// The ladder runs inside every Open; the cutover reshapes tables the
// running system still reads with the old code (annotation, ledger,
// interpretation.sync_error), so it MUST run only in a maintenance window
// with sync stopped, and it MUST be all-or-nothing.
//
// What it does, in one transaction:
//
//   - entity gains version (INTEGER NOT NULL DEFAULT 0), hydrated_at,
//     active (INTEGER NOT NULL DEFAULT 1) and list_fp (nullable TEXT, the
//     list fingerprint at the last successful hydration);
//   - interpretation loses sync_error;
//   - change_log (append-only, one row per change record) and consumer
//     (one cursor per named consumer of a type) are created;
//   - annotation is rebuilt from its per-column shape (hidden,
//     hidden_reason, wip, disposition, keyed by comment_id) into key/value
//     rows, with existing state copied onto the reserved keys `hidden`
//     (JSON {value, reason}), `wip` ('true' or 'false') and
//     `disposition.<comment_id>`, all with origin 'pg-desk';
//   - ledger is dropped;
//   - xref is rebuilt with origin, relation, actor, acted_at and reason and
//     a primary key widened to include relation and origin; existing rows
//     become origin 'derived:legacy', relation 'references';
//   - meta.schema_version and PRAGMA user_version are set to 2.
//
// Nothing here appends to change_log or reads consumer: those tables are
// created empty and are filled by later work.

// cutoverStatements is the whole cutover block, one statement per element
// so that a failure can be attributed to a step and so tests can inject a
// failure at any position. The version stamps are the LAST two steps, so
// the store is never marked version 2 unless everything before them held.
var cutoverStatements = []string{
	// entity: change-flow bookkeeping columns. Existing rows take the
	// defaults (version 0, never hydrated, active).
	`ALTER TABLE entity ADD COLUMN version INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE entity ADD COLUMN hydrated_at TEXT`,
	`ALTER TABLE entity ADD COLUMN active INTEGER NOT NULL DEFAULT 1`,
	// list_fp: the list fingerprint observed at the last successful
	// hydration. NULL (read back as "") means no baseline: such a row is
	// treated as changed once.
	`ALTER TABLE entity ADD COLUMN list_fp TEXT`,

	// interpretation: sync_error was written only by the old sync stage.
	`ALTER TABLE interpretation DROP COLUMN sync_error`,

	// change_log: the append-only record of every change. kinds is a JSON
	// array of change kinds.
	`CREATE TABLE change_log (
  seq         INTEGER PRIMARY KEY AUTOINCREMENT,
  repo        TEXT NOT NULL,
  entity_type TEXT NOT NULL,
  entity_id   TEXT NOT NULL,
  version     INTEGER NOT NULL,
  kinds       TEXT NOT NULL,
  origin      TEXT NOT NULL,
  at          TEXT NOT NULL
)`,
	`CREATE INDEX change_log_entity ON change_log(repo, entity_type, entity_id, seq)`,

	// consumer: one durable cursor into change_log per (name, type).
	`CREATE TABLE consumer (
  name    TEXT NOT NULL,
  type    TEXT NOT NULL,
  cursor  INTEGER NOT NULL DEFAULT 0,
  seen_at TEXT,
  PRIMARY KEY (name, type)
)`,

	// annotation: generalize to key/value. Create the new table, copy the
	// hidden / wip / per-comment disposition state onto its reserved keys,
	// then swap it in.
	`CREATE TABLE annotation_v2 (
  repo        TEXT NOT NULL,
  entity_type TEXT NOT NULL,
  entity_id   TEXT NOT NULL,
  key         TEXT NOT NULL,
  value       TEXT NOT NULL,
  origin      TEXT NOT NULL,
  set_by      TEXT NOT NULL,
  set_at      TEXT NOT NULL,
  PRIMARY KEY (repo, entity_type, entity_id, key)
)`,
	// hidden's value is the JSON object {value, reason} with value a JSON
	// BOOLEAN. SQLite's `hidden = 1` yields the integer 1/0, and
	// json_object would embed that as a number, so the comparison is turned
	// into the literal true/false through json().
	`INSERT INTO annotation_v2 (repo, entity_type, entity_id, key, value, origin, set_by, set_at)
SELECT repo, entity_type, entity_id, 'hidden',
       json_object('value', json(CASE WHEN hidden = 1 THEN 'true' ELSE 'false' END), 'reason', hidden_reason),
       'pg-desk', set_by, set_at
FROM annotation WHERE comment_id = '' AND hidden IS NOT NULL`,
	`INSERT INTO annotation_v2 (repo, entity_type, entity_id, key, value, origin, set_by, set_at)
SELECT repo, entity_type, entity_id, 'wip',
       CASE WHEN wip = 1 THEN 'true' ELSE 'false' END,
       'pg-desk', set_by, set_at
FROM annotation WHERE comment_id = '' AND wip IS NOT NULL`,
	`INSERT INTO annotation_v2 (repo, entity_type, entity_id, key, value, origin, set_by, set_at)
SELECT repo, entity_type, entity_id, 'disposition.' || comment_id, disposition,
       'pg-desk', set_by, set_at
FROM annotation WHERE comment_id != '' AND disposition IS NOT NULL`,
	`DROP TABLE annotation`,
	`ALTER TABLE annotation_v2 RENAME TO annotation`,

	// ledger: the old sync stage's entity-to-bead mapping. Not carried over.
	`DROP TABLE ledger`,

	// xref: record where a link came from and what it means. SQLite cannot
	// alter a primary key, so rebuild the table. Existing rows are marked
	// with the legacy origin and the generic relation (xrefLegacyOrigin,
	// xrefLegacyRelation): the old xref accessors read and write exactly
	// those rows on a version-2 store.
	`CREATE TABLE xref_v2 (
  repo           TEXT NOT NULL,
  from_type      TEXT NOT NULL,
  from_id        TEXT NOT NULL,
  to_type        TEXT NOT NULL,
  to_id          TEXT NOT NULL,
  origin         TEXT NOT NULL,
  relation       TEXT NOT NULL,
  evidence       TEXT,
  first_seen     TEXT NOT NULL,
  last_confirmed TEXT NOT NULL,
  actor          TEXT,
  acted_at       TEXT,
  reason         TEXT,
  PRIMARY KEY (repo, from_type, from_id, to_type, to_id, relation, origin)
)`,
	`INSERT INTO xref_v2 (repo, from_type, from_id, to_type, to_id, origin, relation, evidence, first_seen, last_confirmed)
SELECT repo, from_type, from_id, to_type, to_id, 'derived:legacy', 'references', evidence, first_seen, last_confirmed
FROM xref`,
	`DROP TABLE xref`,
	`ALTER TABLE xref_v2 RENAME TO xref`,

	// Version stamps, last.
	`INSERT INTO meta (key, value) VALUES ('schema_version', '2')
ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
	`PRAGMA user_version = 2`,
}

// Cutover applies the version-2 schema cutover as ONE transaction: either
// every step of it lands and the store reports version 2, or the store is
// left exactly as it was. It is idempotent: on a store already at version 2
// it does nothing and returns nil. It refuses a store that is neither at
// version 1 nor at version 2 (an uninitialized file has nothing to cut
// over; a newer store is not this binary's to touch).
//
// It is the caller's job to make sure nothing else is writing the store
// (sync stopped); the transaction is taken IMMEDIATE so a concurrent writer
// makes this fail or wait rather than interleave.
func (s *Store) Cutover() error {
	return s.cutoverWith(cutoverStatements)
}

// cutoverWith runs steps as the cutover transaction. It is split from
// Cutover so tests can inject a failing step at any position.
func (s *Store) cutoverWith(steps []string) error {
	ctx := context.Background()
	// A dedicated connection: the transaction is driven with explicit
	// BEGIN IMMEDIATE / COMMIT so the version check below happens under the
	// write lock, closing the window in which two cutovers could both see
	// version 1.
	conn, err := s.sql.Conn(ctx)
	if err != nil {
		return fmt.Errorf("store: cutover: acquire connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("store: cutover: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// A failed statement may already have ended the transaction; a
			// "no transaction is active" error here is expected and harmless.
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()

	var current int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("store: cutover: read user_version: %w", err)
	}
	switch {
	case current == NewSchemaVersion:
		return nil // already cut over: nothing to do (rolled back, empty)
	case current != schemaVersion:
		return fmt.Errorf("store: cutover: store is at schema version %d, but the cutover applies to version %d", current, schemaVersion)
	}

	for i, step := range steps {
		if _, err := conn.ExecContext(ctx, step); err != nil {
			return fmt.Errorf("store: cutover: step %d of %d failed (store left unchanged): %w", i+1, len(steps), err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("store: cutover: commit (store left unchanged): %w", err)
	}
	committed = true
	return nil
}
