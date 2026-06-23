// Package store is pg-pr's SQLite-backed datastore: the system of record for
// PR identity and PR feedback. Both the CLI and the daemon import it.
package store

import (
	"database/sql"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// DB wraps the sql.DB handle plus pg-pr store operations.
type DB struct {
	sql *sql.DB //nolint:unused // populated by later tasks
}
