// Package store is pg-pr's SQLite-backed datastore: the system of record for
// PR identity and PR feedback. Both the CLI and the daemon import it.
package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// DB wraps the sql.DB handle plus pg-pr store operations.
type DB struct {
	sql *sql.DB
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
}) *DB {
	db, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("OpenForTest: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
