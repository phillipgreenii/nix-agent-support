// Package sqlite is the SQLite implementation of the store interfaces.
// All connections open with WAL journal mode + foreign keys on so the
// schema's invariants (cascade deletes) actually fire.
package sqlite

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Open returns a *sql.DB pointing at dbPath, with WAL mode, foreign keys,
// and a 5-second busy timeout. The parent directory is created if missing.
//
// dbPath of ":memory:" yields an in-memory DB (used by tests). For
// in-memory the directory creation step is skipped.
func Open(dbPath string) (*sql.DB, error) {
	if dbPath != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
			return nil, fmt.Errorf("mkdir db parent: %w", err)
		}
	}

	// modernc.org/sqlite honours PRAGMAs passed as DSN query params via _pragma.
	dsn := dbPath + "?" + url.Values{
		"_pragma": []string{
			"journal_mode(WAL)",
			"foreign_keys(ON)",
			"busy_timeout(5000)",
			"synchronous(NORMAL)",
		},
	}.Encode()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return db, nil
}
