package store

import (
	"database/sql"
	"fmt"
)

// SchemaVersion is the newest schema version this binary knows. A store
// above it is refused (ErrSchemaNewer); a store below it is migrated up.
const SchemaVersion = 2

// builtinMigrations is the ladder; len(builtinMigrations) == SchemaVersion
// (asserted by a test). A migration, once shipped, MUST NOT change: add a
// new step and a fixture store for the version it produces under testdata/.
var builtinMigrations = []Migration{
	{Version: 1, Name: "query, alias, entity, entity_group, budget", Apply: migrateV1},
	{Version: 2, Name: "job", Apply: migrateV2},
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
