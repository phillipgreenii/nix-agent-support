// Package store is pg-pr's SQLite-backed datastore: the system of record for
// PR identity and PR feedback. Both the CLI and the daemon import it.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// DefaultPath returns the canonical store file path, honouring XDG_STATE_HOME.
// Fallback: ~/.local/state/pg-pr/store.db.
func DefaultPath() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "pg-pr", "store.db")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "pg-pr", "store.db")
}

// DB wraps the sql.DB handle plus pg-pr store operations.
type DB struct {
	sql *sql.DB
}

// synchronousPragma, when non-empty, is applied as `PRAGMA synchronous=<value>`
// immediately after open — before the WAL conversion and before migrate, so it
// governs every fsync those steps would otherwise perform.
//
// Production leaves it EMPTY, so SQLite keeps its default (FULL) and every
// commit to the store is durably flushed. Do not set it from non-test code.
//
// Tests set it to "OFF" via SetSynchronousForTests, either directly (the
// store package's own TestMain) or transitively through OpenForTest, which
// every other pg-pr package that needs a store goes through. This is the same
// defect class already fixed for ceta's asklog store (commit `1138b8a1`):
// opening a store costs ~17 fsyncs (WAL conversion, one commit per schema
// migration — 8 of them here, see migrate.go — plus the close checkpoint),
// fsync latency on a loaded/slow-fsync builder runs 1.1-3.6s each, and a
// package with many tests opening stores serially blows `go test`'s 10-minute
// default timeout even though CPU time is trivial. Durability is meaningless
// for a database deleted at test exit, so tests opt out of it.
var synchronousPragma string

// SetSynchronousForTests sets the synchronous pragma applied by Open. It
// exists as the cross-package seam for pg-pr's tests: unlike ceta's asklog
// (whose tests live in one package with a single TestMain), pg-pr's store is
// opened from tests spread across several packages (internal/sync,
// internal/replyposter, internal/reviewsink, cmd/pg-pr, and this package
// itself), each of which must set this before opening any store.
func SetSynchronousForTests(v string) {
	synchronousPragma = v
}

// Open opens (creating if absent) the SQLite database at path, applies the
// connection pragmas, and runs migrations. The modernc driver name is
// "sqlite". WAL + a 5s busy_timeout let an ad-hoc CLI invocation and a running
// daemon serialize writes without "database is locked" errors.
func Open(path string) (*DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", path)
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
	db := &DB{sql: sqlDB}
	if err := migrate(db); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("store: migrate %s: %w", path, err)
	}
	return db, nil
}

// Close closes the underlying handle.
func (db *DB) Close() error { return db.sql.Close() }

// OpenForTest opens an in-temp-dir store for tests, registering cleanup.
func OpenForTest(t interface {
	TempDir() string
	Cleanup(func())
	Fatalf(string, ...any)
},
) *DB {
	SetSynchronousForTests("OFF")
	db, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("OpenForTest: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
