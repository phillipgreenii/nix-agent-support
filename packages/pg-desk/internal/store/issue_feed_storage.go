package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// issueFeedEntityColumns are the nullable entity columns the owning-backend
// issue flow adds, with their declared types: backend (the registry backend
// instance that serves the entity) and watched (NULL = never stated,
// 1 = watched, 0 = stated not watched).
var issueFeedEntityColumns = []struct{ name, decl string }{
	{"backend", "TEXT"},
	{"watched", "INTEGER"},
}

// ingestFailedDDL creates the failed-ingest side table: one row per
// (instance, entity_id, seq) feed row that could not be hydrated. kinds is
// a JSON array.
const ingestFailedDDL = `CREATE TABLE IF NOT EXISTS ingest_failed (
  instance       TEXT NOT NULL,
  entity_id      TEXT NOT NULL,
  seq            INTEGER NOT NULL,
  store_identity TEXT NOT NULL DEFAULT '',
  kinds          TEXT NOT NULL DEFAULT '[]',
  first_failed_at TEXT NOT NULL,
  last_error     TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (instance, entity_id, seq)
)`

// ensureIssueFeedStorage adds the owning-backend issue flow's storage: the
// entity.backend and entity.watched columns and the ingest_failed table.
//
// It is the ONE additive mechanism of INV-ICD-10(c): CREATE TABLE IF NOT
// EXISTS plus an ALTER TABLE ... ADD COLUMN guarded by a pragma table_info
// check (SQLite has no ADD COLUMN IF NOT EXISTS), and it NEVER touches
// user_version, because a newer version would make an older binary refuse
// the store. Unlike ensureLedgerSettleColumns it is called from Open after
// migrate returns, so it runs for a cut-over store too (migrate returns
// early for every version at or above NewSchemaVersion). It is idempotent,
// and tolerates a concurrent opener that added a column first. A file with
// no entity table at all (nothing migrated) is left alone.
func ensureIssueFeedStorage(s *Store) error {
	have, err := s.tableColumns("entity")
	if err != nil {
		return err
	}
	if len(have) == 0 {
		return nil
	}
	for _, col := range issueFeedEntityColumns {
		if have[col.name] {
			continue
		}
		if _, err := s.sql.Exec(`ALTER TABLE entity ADD COLUMN ` + col.name + ` ` + col.decl); err != nil {
			// A second process may have added it between the check and the
			// ALTER; the column being present now is success.
			again, cerr := s.tableColumns("entity")
			if cerr != nil || !again[col.name] {
				return fmt.Errorf("add entity.%s: %w", col.name, err)
			}
		}
	}
	if _, err := s.sql.Exec(ingestFailedDDL); err != nil {
		return fmt.Errorf("create ingest_failed: %w", err)
	}
	return nil
}

// tableColumns returns the set of declared column names of table (empty
// when the table does not exist). The read is fully drained before it
// returns, so callers may issue a query right after on the single
// connection.
func (s *Store) tableColumns(table string) (map[string]bool, error) {
	rows, err := s.sql.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, fmt.Errorf("store: read %s columns: %w", table, err)
	}
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("store: scan %s column: %w", table, err)
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("store: read %s columns: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("store: read %s columns: %w", table, err)
	}
	return have, nil
}

// issueFeedSelect returns the SELECT expressions for entity.backend and
// entity.watched, in that order: the real columns when the entity table has
// them, the constants NULL, NULL when it does not (a store opened read-only
// or raw that predates the additions). Called before the query is issued,
// never while a result set is open.
func (s *Store) issueFeedSelect() (string, error) {
	have, err := s.tableColumns("entity")
	if err != nil {
		return "", err
	}
	exprs := make([]string, 0, len(issueFeedEntityColumns))
	for _, col := range issueFeedEntityColumns {
		if have[col.name] {
			exprs = append(exprs, col.name)
		} else {
			exprs = append(exprs, "NULL")
		}
	}
	return strings.Join(exprs, ", "), nil
}

// applyIssueFeedColumns copies the scanned backend and watched values onto e.
func applyIssueFeedColumns(e *Entity, backend sql.NullString, watched sql.NullInt64) {
	e.Backend = backend.String
	e.Watched = nil
	if watched.Valid {
		w := watched.Int64 != 0
		e.Watched = &w
	}
}

// SetEntityColumns is the column-only write: it sets entity.backend and
// entity.watched, and nothing else. It updates only the non-nil arguments (a
// pointer to "" writes NULL to backend), appends NO change_log row, does NOT
// bump entity.version, and leaves the snapshot, as_of, hydrated_at, list_fp
// and active untouched. With both arguments nil it is a no-op returning nil.
// It returns ErrNoEntity (matchable with errors.Is) when the row is absent.
func (s *Store) SetEntityColumns(repo, entityType, entityID string, backend *string, watched *bool) error {
	var sets []string
	var args []any
	if backend != nil {
		sets = append(sets, "backend = ?")
		args = append(args, nullableString(*backend))
	}
	if watched != nil {
		sets = append(sets, "watched = ?")
		w := 0
		if *watched {
			w = 1
		}
		args = append(args, w)
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, repo, entityType, entityID)
	res, err := s.sql.Exec(
		`UPDATE entity SET `+strings.Join(sets, ", ")+`
		 WHERE repo = ? AND entity_type = ? AND entity_id = ?`,
		args...,
	)
	if err != nil {
		return fmt.Errorf("store: set entity columns (%s,%s,%s): %w", repo, entityType, entityID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set entity columns (%s,%s,%s): %w", repo, entityType, entityID, err)
	}
	if n == 0 {
		return fmt.Errorf("store: set entity columns (%s,%s,%s): %w", repo, entityType, entityID, ErrNoEntity)
	}
	return nil
}
