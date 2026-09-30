package store

import "fmt"

// schemaVersion is the top of the migration ladder below: the schema Open
// builds and maintains. Stored in SQLite's user_version pragma (ported
// convention from packages/pg-pr/internal/store/migrate.go) and mirrored
// into the meta table's "schema_version" key, since `pg-desk status`
// (design doc section 7.7) reads the schema version out of meta directly
// rather than issuing a PRAGMA.
//
// It is deliberately NOT bumped to NewSchemaVersion: the version-2 cutover
// (cutover.go) is an explicit maintenance step, not a ladder rung, so this
// ladder stops at 1 and Open never runs it.
const schemaVersion = 1

// NewSchemaVersion is the schema version the cutover produces and that
// new-schema commands require (RequireNewSchema). Open accepts a store at
// schemaVersion or at NewSchemaVersion and refuses anything newer.
const NewSchemaVersion = 2

// migrations is the ordered list of DDL applied to reach schemaVersion.
// Index i migrates user_version i -> i+1.
//
// This phase (Phase 9, packet 3) creates all six tables from the design
// doc's section 7.6 in one migration: entity, interpretation, xref,
// annotation, ledger, meta. xref's schema exists here but nothing in this
// docket writes to it until Phase 13; ledger's schema exists here but
// nothing populates/consumes it beyond the bare table until Phase 10.
var migrations = []string{
	// v0 -> v1: initial schema (all six store tables).
	`
CREATE TABLE entity (
    repo         TEXT NOT NULL,
    entity_type  TEXT NOT NULL,
    entity_id    TEXT NOT NULL,
    facts        TEXT NOT NULL,
    as_of        TEXT NOT NULL,
    stale        INTEGER NOT NULL DEFAULT 0,
    content_hash TEXT NOT NULL,
    head_sha     TEXT,
    PRIMARY KEY (repo, entity_type, entity_id)
);

CREATE TABLE interpretation (
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
    sync_error       TEXT,
    as_of            TEXT NOT NULL,
    PRIMARY KEY (repo, entity_type, entity_id)
);

CREATE TABLE xref (
    repo           TEXT NOT NULL,
    from_type      TEXT NOT NULL,
    from_id        TEXT NOT NULL,
    to_type        TEXT NOT NULL,
    to_id          TEXT NOT NULL,
    evidence       TEXT,
    first_seen     TEXT NOT NULL,
    last_confirmed TEXT NOT NULL,
    PRIMARY KEY (repo, from_type, from_id, to_type, to_id)
);

CREATE TABLE annotation (
    repo          TEXT NOT NULL,
    entity_type   TEXT NOT NULL,
    entity_id     TEXT NOT NULL,
    comment_id    TEXT NOT NULL DEFAULT '',
    hidden        INTEGER,
    hidden_reason TEXT,
    wip           INTEGER,
    disposition   TEXT,
    set_by        TEXT NOT NULL,
    set_at        TEXT NOT NULL,
    PRIMARY KEY (repo, entity_type, entity_id, comment_id)
);

CREATE TABLE ledger (
    repo                     TEXT NOT NULL,
    entity_type              TEXT NOT NULL,
    entity_id                TEXT NOT NULL,
    kind                     TEXT NOT NULL,
    bead_id                  TEXT NOT NULL,
    last_synced_content_hash TEXT,
    last_synced_at           TEXT,
    last_reviewed_head_sha   TEXT,
    PRIMARY KEY (repo, entity_type, entity_id, kind)
);

CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`,
}

// migrate applies any pending migrations (comparing SQLite's user_version
// pragma against the ladder) and mirrors the resulting version into meta's
// "schema_version" key.
//
// A store already at NewSchemaVersion is accepted and left completely
// alone: no migration runs, and meta.schema_version is NOT rewritten (it
// says "2" and the ladder would otherwise overwrite it with "1"). Anything
// newer than NewSchemaVersion is refused.
func migrate(s *Store) error {
	var current int
	if err := s.sql.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if current > NewSchemaVersion {
		return fmt.Errorf("database schema version %d is newer than this binary supports (%d)", current, NewSchemaVersion)
	}
	if current >= NewSchemaVersion {
		return nil
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
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", i+1, err)
		}
	}

	if _, err := s.sql.Exec(
		`INSERT INTO meta (key, value) VALUES ('schema_version', ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		fmt.Sprintf("%d", schemaVersion),
	); err != nil {
		return fmt.Errorf("mirror schema_version into meta: %w", err)
	}
	return nil
}
