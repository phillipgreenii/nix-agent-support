package store

import (
	"context"
	"fmt"
)

// schemaVersion is the one schema version this binary builds and accepts: the
// entity-change-flow schema. It is stored in SQLite's user_version pragma and
// mirrored into the meta table's "schema_version" key, which `pg-desk status`
// reads directly rather than issuing a PRAGMA.
//
// The store had an older version-1 schema and a ladder that reached it, then an
// explicit cutover to version 2. Every store has since been cut over, so both
// were deleted: a fresh store is created directly at this version, and a store
// still at version 1 is refused (see Open).
const schemaVersion = 2

// NewSchemaVersion is the name older callers use for schemaVersion.
const NewSchemaVersion = schemaVersion

// schemaStatements is the whole schema of a fresh store, one statement per
// element so that a failure can be attributed to a step. The version stamps are
// the LAST two steps, so a store is never marked version 2 unless every table
// before them was created. A test (TestFreshStoreShape) compares the result
// with a golden captured from the version-1 ladder plus the cutover, so this
// text and a store that went through those two cannot drift apart.
//
// Columns that the additive ensure steps (ensureIssueFeedStorage,
// ensureInterpretationStamp) also add are created here too, so those steps are
// no-ops on a fresh store; they stay because a store created by an earlier
// release can lack them.
var schemaStatements = []string{
	`CREATE TABLE annotation (
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
	`CREATE TABLE consumer (
  name    TEXT NOT NULL,
  type    TEXT NOT NULL,
  cursor  INTEGER NOT NULL DEFAULT 0,
  seen_at TEXT,
  PRIMARY KEY (name, type)
)`,
	`CREATE TABLE entity (
    repo         TEXT NOT NULL,
    entity_type  TEXT NOT NULL,
    entity_id    TEXT NOT NULL,
    facts        TEXT NOT NULL,
    as_of        TEXT NOT NULL,
    stale        INTEGER NOT NULL DEFAULT 0,
    content_hash TEXT NOT NULL,
    head_sha     TEXT,
    backend      TEXT,
    watched      INTEGER,
    read_attempt_at TEXT,
    read_ok_at   TEXT,
    version      INTEGER NOT NULL DEFAULT 0,
    hydrated_at  TEXT,
    active       INTEGER NOT NULL DEFAULT 1,
    list_fp      TEXT,
    first_seen_at TEXT,
    PRIMARY KEY (repo, entity_type, entity_id)
)`,
	`CREATE TABLE focus_draft (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    period_type TEXT NOT NULL,
    period_key  TEXT NOT NULL,        -- the period the draft was made for; it applies only to a command that addresses it (section 7)
    cap         INTEGER NOT NULL,     -- the cap line the draft was made with
    made_at     TEXT NOT NULL,
    body_json   TEXT NOT NULL,        -- a ` + "`" + `contract` + "`" + ` member (pg-desk.focus.draft/v1; missing or unknown = corrupt), the ordered candidate rows (key, tier, deciding key, the
                                       -- '+' proposal flags, the finished and new marks, via), the
                                       -- trailing epics block, and the coverage state the draft was
                                       -- computed under; NO plan rows and NO facts (both read live)
    CHECK (period_type IN ('day', 'week', 'sprint'))
)`,
	`CREATE TABLE focus_period (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    period_type   TEXT NOT NULL,      -- 'day' this phase; schema allows 'week'/'sprint' later
    period_key    TEXT NOT NULL,      -- e.g. '2026-09-23' for period_type='day'; parsed and
                                       -- re-formatted on write, so '2026-9-3' cannot make a
                                       -- second period
    cap           INTEGER,            -- the cap in force for the period (a re-run without
                                       -- ` + "`" + `cap=` + "`" + ` reuses it); NULL until a run sets one
    closed_at     TEXT,
    close_note    TEXT,
    UNIQUE (period_type, period_key),
    CHECK (period_type IN ('day', 'week', 'sprint'))
)`,
	`CREATE TABLE focus_run (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id      TEXT NOT NULL UNIQUE,   -- a ULID, printed by the verb and carried in --json
    verb        TEXT NOT NULL,          -- 'select' | 'pull' | 'close' | 'repair'
    focus_period_id INTEGER,
    started_at  TEXT NOT NULL,
    actor       TEXT NOT NULL,
    exit_code   INTEGER,                -- NULL until the run is finalised (section 7.2 step 5); a
                                         -- NULL left by a crash counts as outcome ` + "`" + `total` + "`" + `
    counts_json TEXT NOT NULL,          -- ranked, selected, removed by cause (operator, cap,
                                         -- dropped, new_period), forced, handadded, absorbed,
                                         -- hydrated, per-entity annotation outcome with its
                                         -- change_log seq, the draft's age and drift rows, fresh_order,
                                         -- and the rank_inputs counts of section 8.2
    FOREIGN KEY (focus_period_id) REFERENCES focus_period (id)
)`,
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
	`CREATE TABLE interpretation (
    repo             TEXT NOT NULL,
    entity_type      TEXT NOT NULL,
    entity_id        TEXT NOT NULL,
    ownership        TEXT,
    enrichment       TEXT,
    urgency          TEXT,
    category         TEXT,
    dispositions     TEXT,
    approvals        TEXT,
    gate_state       TEXT,
    match_reasons    TEXT,
    panel            TEXT,
    ready_to_promote INTEGER NOT NULL DEFAULT 0,
    degraded         INTEGER NOT NULL DEFAULT 0,
    as_of            TEXT NOT NULL,
    interp_version   TEXT,
    PRIMARY KEY (repo, entity_type, entity_id)
)`,
	`CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
)`,
	`CREATE TABLE xref (
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
	`CREATE INDEX change_log_entity ON change_log(repo, entity_type, entity_id, seq)`,

	// Version stamps, last.
	`INSERT INTO meta (key, value) VALUES ('schema_version', '2')
ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
	`PRAGMA user_version = 2`,
}

// createSchema builds the schema of a fresh (version 0) store as ONE
// transaction: either every table is created and the store reports version 2,
// or the file is left empty. It re-reads user_version under the write lock, so
// two processes opening the same new file cannot both create it; the loser
// sees version 2 and does nothing.
func createSchema(s *Store) error {
	return s.createSchemaWith(schemaStatements)
}

// createSchemaWith runs steps as the creation transaction. It is split from
// createSchema so tests can inject a failing step at any position.
func (s *Store) createSchemaWith(steps []string) error {
	ctx := context.Background()
	conn, err := s.sql.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin: %w", err)
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
		return fmt.Errorf("read user_version: %w", err)
	}
	if current != 0 {
		return nil // another opener created it first (rolled back, empty)
	}
	for i, step := range steps {
		if _, err := conn.ExecContext(ctx, step); err != nil {
			return fmt.Errorf("create schema: step %d of %d failed (store left empty): %w", i+1, len(steps), err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("create schema: commit (store left empty): %w", err)
	}
	committed = true
	return nil
}

// checkSchema reads the store's schema version and applies Open's rule: a
// fresh file (version 0) gets the schema created, version 2 is accepted, the
// retired version 1 is refused with ErrOldSchema, and anything newer is
// refused as newer than this binary.
func checkSchema(s *Store) error {
	var current int
	if err := s.sql.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	switch {
	case current > schemaVersion:
		return fmt.Errorf("database schema version %d is newer than this binary supports (%d)", current, schemaVersion)
	case current == schemaVersion:
		return nil
	case current == 0:
		return createSchema(s)
	default:
		return fmt.Errorf("%w (schema version %d, need %d): %s", ErrOldSchema, current, schemaVersion, oldSchemaAdvice)
	}
}

// oldSchemaAdvice tells the operator how a store left on the retired schema
// version 1 is brought forward: with a release that still has the cutover.
const oldSchemaAdvice = "this release no longer migrates schema version 1; run `pg-desk migrate --cutover` from a release that still has it, then retry"
