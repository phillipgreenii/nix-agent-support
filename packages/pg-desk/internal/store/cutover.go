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
//   - entity gains first_seen_at (nullable TEXT), backfilled from as_of for
//     the rows that exist at the cutover and stamped once by every later
//     insert (the focus rank's age fallback);
//   - the four focus tables are created empty: focus_period,
//     focus_selection, focus_draft and focus_run (docket pg2-2j5ac.44);
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
	// first_seen_at: when pg-desk first inserted the row. Set once by the
	// insert paths (store.Entity.FirstSeenAt) and backfilled below from
	// as_of for the rows that exist at the cutover. It is the focus rank's
	// age fallback; the change log is pruned and cannot supply it.
	`ALTER TABLE entity ADD COLUMN first_seen_at TEXT`,
	`UPDATE entity SET first_seen_at = as_of WHERE first_seen_at IS NULL`,

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

	// The focus tables (docket pg2-2j5ac.44, spec section 5): the daily-focus
	// plan, the one stored draft and the run record. They join this block
	// (one transaction, one operator window) rather than a version-3 ladder
	// step because the cutover has not shipped. They reference entity, so
	// they come after every entity ALTER, and they are created empty. The
	// surrogate keys are deliberate (D-F6). Telemetry: SQLite rows only, no
	// OpenTelemetry.
	`CREATE TABLE focus_period (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    period_type   TEXT NOT NULL,      -- 'day' this phase; schema allows 'week'/'sprint' later
    period_key    TEXT NOT NULL,      -- e.g. '2026-09-23' for period_type='day'; parsed and
                                       -- re-formatted on write, so '2026-9-3' cannot make a
                                       -- second period
    cap           INTEGER,            -- the cap in force for the period (a re-run without
                                       -- ` + "`cap=`" + ` reuses it); NULL until a run sets one
    closed_at     TEXT,
    close_note    TEXT,
    UNIQUE (period_type, period_key),
    CHECK (period_type IN ('day', 'week', 'sprint'))
)`,

	// The current plan, and nothing else (RV-B): every row is selected, none
	// is ever kept after it leaves the plan, and there is no history table.
	`CREATE TABLE focus_selection (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    focus_period_id INTEGER NOT NULL,
    repo            TEXT NOT NULL,    -- same repo constant pg-desk already uses for every
                                       -- entity it gathers (section 8's "single configured repo" in
                                       -- the parent design), not a per-issue attribute
    entity_type     TEXT NOT NULL,    -- 'pr' | 'issue' (Jira or bd; told apart by the bead-id
                                       -- pattern, section 8.1)
    entity_id       TEXT NOT NULL,
    selected_at     TEXT NOT NULL,
    rank_position   INTEGER NOT NULL, -- the row's place in the frozen plan: a per-period lock
                                       -- sequence (section 7.2 step 5), the draft position
                                       -- only for the first lock (D-F22)
    tier            TEXT,             -- 'overdue' | 'started' | 'not_started' as of that draft;
                                       -- NULL for a hand-add
    UNIQUE (focus_period_id, repo, entity_type, entity_id),
    CHECK (tier IS NULL OR tier IN ('overdue', 'started', 'not_started')),
    FOREIGN KEY (focus_period_id) REFERENCES focus_period (id),
    FOREIGN KEY (repo, entity_type, entity_id) REFERENCES entity (repo, entity_type, entity_id)
)`,

	// The ONE stored draft (RV-A, D-F22): at most one row, managed by
	// pg-desk, never held by the caller. It ends at lock (select --apply
	// deletes it in the same transaction as the plan write), and replan
	// replaces it. The period need not have a focus_period row yet, so it is
	// not a foreign key.
	`CREATE TABLE focus_draft (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    period_type TEXT NOT NULL,
    period_key  TEXT NOT NULL,        -- the period the draft was made for; it applies only to a command that addresses it (section 7)
    cap         INTEGER NOT NULL,     -- the cap line the draft was made with
    made_at     TEXT NOT NULL,
    body_json   TEXT NOT NULL,        -- a ` + "`contract`" + ` member (pg-desk.focus.draft/v1; missing or unknown = corrupt), the ordered candidate rows (key, tier, deciding key, the
                                       -- '+' proposal flags, the finished and new marks, via), the
                                       -- trailing epics block, and the coverage state the draft was
                                       -- computed under; NO plan rows and NO facts (both read live)
    CHECK (period_type IN ('day', 'week', 'sprint'))
)`,

	// One row per non-dry-run verb run (select, pull, close, select
	// --repair), the durable copy of the structured stderr line of section
	// 8.2. It is run telemetry, not plan history. A total failure that
	// commits nothing else still leaves this row when the store is writable.
	`CREATE TABLE focus_run (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id      TEXT NOT NULL UNIQUE,   -- a ULID, printed by the verb and carried in --json
    verb        TEXT NOT NULL,          -- 'select' | 'pull' | 'close' | 'repair'
    focus_period_id INTEGER,
    started_at  TEXT NOT NULL,
    actor       TEXT NOT NULL,
    exit_code   INTEGER,                -- NULL until the run is finalised (section 7.2 step 5); a
                                         -- NULL left by a crash counts as outcome ` + "`total`" + `
    counts_json TEXT NOT NULL,          -- ranked, selected, removed by cause (operator, cap,
                                         -- dropped, new_period), forced, handadded, absorbed,
                                         -- hydrated, per-entity annotation outcome with its
                                         -- change_log seq, the draft's age and drift rows, fresh_order,
                                         -- and the rank_inputs counts of section 8.2
    FOREIGN KEY (focus_period_id) REFERENCES focus_period (id)
)`,

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
