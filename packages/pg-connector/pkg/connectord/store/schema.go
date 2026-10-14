package store

import (
	"database/sql"
	"fmt"
)

// SchemaVersion is the newest schema version this binary knows. A store
// above it is refused (ErrSchemaNewer); a store below it is migrated up.
const SchemaVersion = 3

// builtinMigrations is the ladder; len(builtinMigrations) == SchemaVersion
// (asserted by a test). A migration, once shipped, MUST NOT change: add a
// new step and a fixture store for the version it produces under testdata/.
var builtinMigrations = []Migration{
	{Version: 1, Name: "query, alias, entity, entity_group, budget", Apply: migrateV1},
	{Version: 2, Name: "job", Apply: migrateV2},
	{Version: 3, Name: "pending_change, change_log, consumer", Apply: migrateV3},
}

// BuiltinMigrations returns a copy of the built-in ladder. A caller that wants
// to wrap a step (a test that makes one step slow, to watch the daemon answer
// "migrating:" meanwhile) passes the edited copy as Options.Migrations; the
// steps' Version numbers must stay 1..n.
func BuiltinMigrations() []Migration {
	return append([]Migration(nil), builtinMigrations...)
}

// Times are INTEGER Unix milliseconds (NULL = unset). Booleans are INTEGER
// 0/1. A group's `content` is an opaque BLOB owned by the group's Strategy.
//
// Version 1 holds the five tables below. The job table arrives with version 2
// (schemaV2); pending_change, change_log and consumer arrive by later
// migrations.
const schemaV1 = `
CREATE TABLE query (
    key             TEXT    NOT NULL PRIMARY KEY,
    configured      INTEGER NOT NULL DEFAULT 0,
    ids             TEXT    NOT NULL DEFAULT '[]',
    complete        INTEGER NOT NULL DEFAULT 0,
    fetched_at      INTEGER,
    fresh_until     INTEGER,
    last_success_at INTEGER,
    last_error      TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE alias (
    node_id  TEXT    NOT NULL PRIMARY KEY,
    ref      TEXT    NOT NULL,
    seen_at  INTEGER NOT NULL
);
CREATE INDEX alias_ref ON alias (ref);

CREATE TABLE entity (
    kind                 TEXT    NOT NULL,
    id                   TEXT    NOT NULL,
    version              INTEGER NOT NULL DEFAULT 0,
    last_access          INTEGER,
    keep_alive           INTEGER NOT NULL DEFAULT 0,
    last_known_mergeable TEXT    NOT NULL DEFAULT '',
    tombstoned_at        INTEGER,
    PRIMARY KEY (kind, id)
);

CREATE TABLE entity_group (
    kind             TEXT    NOT NULL,
    id               TEXT    NOT NULL,
    group_name       TEXT    NOT NULL,
    content          BLOB,
    fingerprint      TEXT    NOT NULL DEFAULT '',
    fetched_at       INTEGER,
    fresh_until      INTEGER,
    max_age_at       INTEGER,
    last_error       TEXT    NOT NULL DEFAULT '',
    generation       INTEGER NOT NULL DEFAULT 0,
    fetched_for_head TEXT    NOT NULL DEFAULT '',
    fetched_for_base TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (kind, id, group_name)
);
CREATE INDEX entity_group_due ON entity_group (fresh_until);

CREATE TABLE budget (
    seq       INTEGER PRIMARY KEY AUTOINCREMENT,
    bucket    TEXT    NOT NULL,
    at        INTEGER NOT NULL,
    remaining INTEGER NOT NULL,
    reset_at  INTEGER,
    spent     INTEGER NOT NULL DEFAULT 0,
    purpose   TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX budget_bucket_at ON budget (bucket, at);
`

func migrateV1(tx *sql.Tx, report func(string)) error {
	report("creating the version 1 tables")
	if _, err := tx.Exec(schemaV1); err != nil {
		return fmt.Errorf("create version 1 schema: %w", err)
	}
	return nil
}

// schemaV2 adds the job table: the CI jobs of one run attempt, cached by
// (run_id, attempt). A completed attempt's jobs never change, so a row is
// written once and read on every later refresh of the run's group. run_id is
// TEXT because a CI run id is a decimal string on the wire; jobs is a JSON list
// of {id, name, status, conclusion, url} (see JobResult); fetched_at is Unix
// milliseconds, indexed so pruning old rows does not scan the table.
const schemaV2 = `
CREATE TABLE job (
    run_id     TEXT    NOT NULL,
    attempt    INTEGER NOT NULL,
    jobs       TEXT    NOT NULL DEFAULT '[]',
    fetched_at INTEGER NOT NULL,
    PRIMARY KEY (run_id, attempt)
);
CREATE INDEX job_fetched_at ON job (fetched_at);
`

func migrateV2(tx *sql.Tx, report func(string)) error {
	report("creating the job table")
	if _, err := tx.Exec(schemaV2); err != nil {
		return fmt.Errorf("create version 2 schema: %w", err)
	}
	return nil
}

// schemaV3 adds the change engine's persistence. The store keeps opaque kinds,
// ids and JSON; what a change kind means, and when a pending row settles, belong
// to the engine, not to this package.
//
//   - change_log: the append-only feed. seq is AUTOINCREMENT so a sequence number
//     is never reused, even after the newest rows were pruned, and a rolled-back
//     append gives its number back (sqlite_sequence is transactional). kind is
//     the log kind (pr or ci), id the wire reference, kinds a JSON list of change
//     kinds, fields an opaque JSON document the classifier owns, head_sha the
//     head commit recorded with the row.
//   - pending_change: one row per entity key awaiting its settle barrier; kinds
//     is a JSON list, fields and awaited are opaque JSON; head_sha, base_sha,
//     origin and origin_updated_at carry what the engine needs to append the row
//     later; detected_at and deadline are Unix milliseconds.
//   - consumer: a feed cursor, keyed (consumer, kind, query); query is ” for a
//     key with no query.
//   - store_meta: a small key-value table (the feed keeps its "store was moved
//     aside" marker there).
//
// query gains baseline_at: NULL until the first complete run of a configured
// query has established its membership baseline.
const schemaV3 = `
CREATE TABLE change_log (
    seq               INTEGER PRIMARY KEY AUTOINCREMENT,
    kind              TEXT    NOT NULL,
    id                TEXT    NOT NULL,
    version           INTEGER NOT NULL DEFAULT 0,
    head_sha          TEXT    NOT NULL DEFAULT '',
    kinds             TEXT    NOT NULL DEFAULT '[]',
    fields            TEXT    NOT NULL DEFAULT '',
    origin            TEXT    NOT NULL DEFAULT '',
    origin_updated_at INTEGER,
    at                INTEGER NOT NULL
);
CREATE INDEX change_log_at ON change_log (at);
CREATE INDEX change_log_entity ON change_log (kind, id, seq);

CREATE TABLE pending_change (
    kind              TEXT    NOT NULL,
    id                TEXT    NOT NULL,
    kinds             TEXT    NOT NULL DEFAULT '[]',
    fields            TEXT    NOT NULL DEFAULT '',
    awaited           TEXT    NOT NULL DEFAULT '',
    head_sha          TEXT    NOT NULL DEFAULT '',
    base_sha          TEXT    NOT NULL DEFAULT '',
    origin            TEXT    NOT NULL DEFAULT '',
    origin_updated_at INTEGER,
    detected_at       INTEGER,
    deadline          INTEGER,
    PRIMARY KEY (kind, id)
);
CREATE INDEX pending_change_deadline ON pending_change (deadline);

CREATE TABLE consumer (
    consumer  TEXT    NOT NULL,
    kind      TEXT    NOT NULL,
    query     TEXT    NOT NULL DEFAULT '',
    acked_seq INTEGER NOT NULL DEFAULT 0,
    seen_at   INTEGER NOT NULL,
    PRIMARY KEY (consumer, kind, query)
);
CREATE INDEX consumer_seen_at ON consumer (seen_at);

CREATE TABLE store_meta (
    key   TEXT NOT NULL PRIMARY KEY,
    value TEXT NOT NULL
);

ALTER TABLE query ADD COLUMN baseline_at INTEGER;
`

func migrateV3(tx *sql.Tx, report func(string)) error {
	report("creating the change log, pending change, consumer and meta tables")
	if _, err := tx.Exec(schemaV3); err != nil {
		return fmt.Errorf("create version 3 schema: %w", err)
	}
	return nil
}
