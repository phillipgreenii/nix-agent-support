// Package store is pg-desk's SQLite-backed datastore: the system of record
// for gathered facts, derived interpretation, cross-references, human/agent
// annotations, the sync ledger, and store-level metadata (design doc
// section 7.6, docs/superpowers/specs/2026-09-09-pg-desk-and-connector-discovery-design.md
// lines 913-930).
//
// This phase (docket pg2-2j5ac.32, Phase 9, packet 3) implements the SCHEMA
// and per-table writer/reader methods for exactly six tables: entity,
// interpretation, xref, annotation, ledger, meta. It does NOT implement the
// sync logic that populates/consumes the ledger table beyond its bare
// schema (Phase 10), and it does NOT populate xref (Phase 13). No pipeline
// stage in this docket calls the annotation writer — packet 8's CLI
// commands (hide/unhide/wip/feedback set) and packet 9's
// import-pg-pr-annotations command are its only callers.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// busyTimeoutMillis is the SQLite busy_timeout applied on every connection,
// letting `serve`, the CLI, and `run` overlap without "database is locked"
// errors. The design doc's section 7.6 states WAL + a busy timeout but no
// specific value, so this ports pg-pr's own store's existing precedent
// (packages/pg-pr/internal/store/store.go's Open) verbatim: 5000ms.
const busyTimeoutMillis = 5000

// DefaultPath returns the canonical store file path, honouring
// XDG_STATE_HOME per the design doc's section 7.6 ("SQLite at
// $XDG_STATE_HOME/pg-desk/store.db"). Fallback: ~/.local/state/pg-desk/store.db,
// mirroring pg-pr's internal/store.DefaultPath.
func DefaultPath() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "pg-desk", "store.db")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "pg-desk", "store.db")
}

// Store wraps the sql.DB handle plus pg-desk's store operations.
type Store struct {
	sql *sql.DB
}

// synchronousPragma, when non-empty, is applied as `PRAGMA synchronous=<value>`
// immediately after open, before the WAL conversion and before migrate, so
// it governs every fsync those steps would otherwise perform.
//
// Production leaves it EMPTY, so SQLite keeps its default (FULL) and every
// commit to the store is durably flushed. Tests set it to "OFF" via
// SetSynchronousForTests (directly, or transitively through OpenForTest),
// mirroring pg-pr's internal/store — durability is meaningless for a
// database deleted at test exit, and skipping the fsyncs keeps a package
// with many tests well inside go test's default timeout.
var synchronousPragma string

// SetSynchronousForTests sets the synchronous pragma applied by Open. Exists
// as the cross-package seam for this package's own tests.
func SetSynchronousForTests(v string) {
	synchronousPragma = v
}

// Open opens (creating if absent) the SQLite database at path, creating its
// parent directory if needed, applies the connection pragmas (WAL mode, a
// busy timeout, foreign keys on), and runs migrations. The modernc driver
// name is "sqlite".
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store: create dir for %s: %w", path, err)
	}

	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(%d)&_pragma=foreign_keys(ON)",
		path, busyTimeoutMillis)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// modernc serializes writes correctly with a single connection; cap the
	// pool so WAL writers don't contend within one process.
	sqlDB.SetMaxOpenConns(1)
	if synchronousPragma != "" {
		if _, err := sqlDB.Exec("PRAGMA synchronous=" + synchronousPragma); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("store: set synchronous %s: %w", path, err)
		}
	}
	s := &Store{sql: sqlDB}
	if err := migrate(s); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("store: migrate %s: %w", path, err)
	}
	return s, nil
}

// Close closes the underlying handle.
func (s *Store) Close() error { return s.sql.Close() }

// OpenForTest opens an in-temp-dir store for tests, registering cleanup.
func OpenForTest(t interface {
	TempDir() string
	Cleanup(func())
	Fatalf(string, ...any)
},
) *Store {
	SetSynchronousForTests("OFF")
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenForTest: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
