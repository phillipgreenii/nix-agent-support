// Package sqlite is the SQLite implementation of the store interfaces.
// All connections open through the shared sqliteopen helper, with WAL journal
// mode + foreign keys on so the schema's invariants (cascade deletes) actually
// fire.
package sqlite

import (
	"database/sql"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-sqlite/sqliteopen"
)

// Open returns a *sql.DB pointing at dbPath, opened through the shared
// sqliteopen helper so it carries the repository's standard pragma set (WAL,
// synchronous NORMAL, 5-second busy timeout, foreign keys on, 64 MiB page
// cache, mmap, in-memory temp store, journal size limit). The parent
// directory is created if missing.
//
// dbPath of ":memory:" yields an in-memory DB (used by tests).
func Open(dbPath string) (*sql.DB, error) {
	return sqliteopen.Open(sqliteopen.Standard(dbPath))
}
