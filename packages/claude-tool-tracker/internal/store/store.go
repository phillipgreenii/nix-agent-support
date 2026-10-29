// Package store owns the tracker's SQLite database: the schema, its in-place
// migrations, and the published views (docs/views.md).
//
// Tables are private to the tracker; readers use only the views. The events
// files are a spool that is deleted after retention, so the database is the
// lasting record and every schema change is an in-place migration.
package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// DefaultPath is $XDG_STATE_HOME/claude-tool-tracker/tracker.db
// (default ~/.local/state).
func DefaultPath() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "claude-tool-tracker", "tracker.db"), nil
}

// migrations[i] upgrades user_version i to i+1. Append only; never edit one
// that has shipped.
var migrations = []string{
	`CREATE TABLE events (
		event_id    TEXT PRIMARY KEY,
		session_id  TEXT NOT NULL,
		source      TEXT NOT NULL,
		kind        TEXT NOT NULL,
		ts          INTEGER NOT NULL,
		tool_use_id TEXT,
		agent_id    TEXT,
		data        TEXT NOT NULL,
		file        TEXT NOT NULL
	);
	CREATE INDEX events_call ON events(session_id, tool_use_id, kind);
	CREATE INDEX events_source ON events(source);
	CREATE TABLE calls (
		call_id     INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id  TEXT NOT NULL,
		tool_use_id TEXT NOT NULL,
		UNIQUE (session_id, tool_use_id)
	);
	CREATE TABLE call_settlement (
		call_id       INTEGER PRIMARY KEY REFERENCES calls(call_id),
		settled_by    TEXT,
		settlement    TEXT,
		converted_ask INTEGER
	);
	CREATE TABLE cwd_repo (
		cwd  TEXT PRIMARY KEY,
		repo TEXT NOT NULL
	);
	CREATE TABLE ingest_runs (
		run_id     INTEGER PRIMARY KEY AUTOINCREMENT,
		started_ms INTEGER NOT NULL
	);
	CREATE TABLE files (
		file        TEXT PRIMARY KEY,
		session_id  TEXT NOT NULL,
		source      TEXT NOT NULL,
		read_offset INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE file_runs (
		run_id                INTEGER NOT NULL REFERENCES ingest_runs(run_id),
		file                  TEXT NOT NULL REFERENCES files(file),
		lines_read            INTEGER NOT NULL,
		events_new            INTEGER NOT NULL,
		events_duplicate      INTEGER NOT NULL,
		skipped_invalid_json  INTEGER NOT NULL,
		skipped_missing_field INTEGER NOT NULL,
		events_without_call   INTEGER NOT NULL,
		PRIMARY KEY (run_id, file)
	);`,
}

// Open opens (creating and migrating as needed) the database at path for
// writing.
func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	q := url.Values{}
	q.Set("_txlock", "immediate")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	// One connection: a single ingest process is the only writer.
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// OpenReadOnly opens an existing database for reading only.
func OpenReadOnly(path string) (*sql.DB, error) {
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	tx, err := db.Begin() // BEGIN IMMEDIATE: concurrent openers serialize here
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var v int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this binary (%d)", v, len(migrations))
	}
	for ; v < len(migrations); v++ {
		if _, err := tx.Exec(migrations[v]); err != nil {
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			return err
		}
	}
	// Views hold no data, so they are recreated on every open and a view
	// change needs no migration step.
	if _, err := tx.Exec(viewsSQL()); err != nil {
		return fmt.Errorf("views: %w", err)
	}
	return tx.Commit()
}
