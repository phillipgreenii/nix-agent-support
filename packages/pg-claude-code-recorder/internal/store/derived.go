package store

import (
	"database/sql"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
)

// derivedTablesSQL is the parser's part of index.db: records, their typed fields,
// the text indexes (a word index and a trigram index, both contentless) and the
// parse accounting. It is derived from the copies, so a rebuilt database simply
// parses again. Full file contents are never stored here.
//
// A field name is stored once, in field_names, and fields refers to it by
// name_id. fields is WITHOUT ROWID keyed (rec_id, name_id), so the primary key
// is also the by-record lookup index. A record holds at most one row per field
// name: when a record yields the same name twice, the LAST value wins and
// replaces the whole row (s, n and f together); see Parser.insert.
//
// Facts another table already holds are not stored as fields rows: type and
// session_id are the records.type and records.session columns, and the project
// and file_session of a path capture are read back from the file's path (see
// query path facts). No schema version change: a database that still holds
// those rows answers the same (a stored row wins), it is just larger until it
// is rebuilt.
//
// Session tables (schema v3 slice 2). records.session_ref is the integer
// reference of the record's session in sessions (first-seen order; the empty
// session has a sentinel row with session_id ”). sessions is append-only,
// session_stats is its mutable sibling (written once per parse batch, an
// upsert), and session_files fans a session out to the files that hold it. They
// are derived from records and rebuildable from it. For now records.session
// stays the column every query reads and records.session_ref is written beside
// it: the swap, the index on session_ref and the version bump belong to later
// slices. A database created before these tables existed gets them (and the
// session_ref column, see addSessionRefColumn) at open, but its older rows keep
// session_ref 0 and are absent from the session tables until it is rebuilt.
// first_version_id, entrypoint_id and user_type_id stay NULL until the lookup
// tables of a later slice exist.
//
// fields_name_n is partial: only rows with a number are indexed. A filter or
// sort on a numeric field compares n with = < > IN and the like, which the
// planner can prove implies n IS NOT NULL, so it can use the index; a query
// that asks for n IS NULL cannot, and none exists. A database created with the
// full index has it dropped at open (dropFullFieldsIndex in store.go).
const derivedTablesSQL = `
CREATE TABLE IF NOT EXISTS records(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  file_id INTEGER NOT NULL,
  gen INTEGER NOT NULL,
  off INTEGER NOT NULL,
  item INTEGER NOT NULL,
  len INTEGER NOT NULL,
  source TEXT NOT NULL,
  type TEXT NOT NULL,
  parser_version INTEGER NOT NULL,
  session TEXT NOT NULL DEFAULT '',
  time_ns INTEGER NOT NULL DEFAULT 0,
  session_ref INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS sessions(
  session_ref INTEGER PRIMARY KEY,
  session_id TEXT NOT NULL UNIQUE,
  project TEXT,
  first_time_ns INTEGER NOT NULL,
  first_version_id INTEGER,
  entrypoint_id INTEGER,
  user_type_id INTEGER
) STRICT;
CREATE TABLE IF NOT EXISTS session_stats(
  session_ref INTEGER PRIMARY KEY REFERENCES sessions(session_ref),
  last_time_ns INTEGER NOT NULL,
  record_count INTEGER NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS session_files(
  session_ref INTEGER NOT NULL REFERENCES sessions(session_ref),
  file_id INTEGER NOT NULL,
  PRIMARY KEY(session_ref, file_id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS field_names(
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS fields(
  rec_id INTEGER NOT NULL,
  name_id INTEGER NOT NULL,
  s TEXT,
  n INTEGER,
  f REAL,
  PRIMARY KEY(rec_id, name_id)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS texts(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  rec_id INTEGER NOT NULL,
  field TEXT NOT NULL,
  kind TEXT NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS fts_word USING fts5(body, content='', contentless_delete=1, tokenize='unicode61');
CREATE VIRTUAL TABLE IF NOT EXISTS fts_tri USING fts5(body, content='', contentless_delete=1, tokenize="trigram case_sensitive 1");
CREATE TABLE IF NOT EXISTS parse_state(
  file_id INTEGER NOT NULL,
  gen INTEGER NOT NULL,
  version INTEGER NOT NULL,
  PRIMARY KEY(file_id, gen)
);
CREATE TABLE IF NOT EXISTS parse_resets(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  file_id INTEGER NOT NULL,
  gen INTEGER NOT NULL,
  parser TEXT NOT NULL,
  at_ns INTEGER NOT NULL,
  next_rec INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS feed_marks(
  rec_id INTEGER PRIMARY KEY,
  at_ns INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS bad_lines(
  file_id INTEGER NOT NULL,
  gen INTEGER NOT NULL,
  off INTEGER NOT NULL,
  len INTEGER NOT NULL,
  reason TEXT NOT NULL,
  PRIMARY KEY(file_id, gen, off)
);
CREATE TABLE IF NOT EXISTS unknown_keys(
  file_id INTEGER NOT NULL,
  gen INTEGER NOT NULL,
  source TEXT NOT NULL,
  rtype TEXT NOT NULL,
  path TEXT NOT NULL,
  count INTEGER NOT NULL,
  PRIMARY KEY(file_id, gen, source, rtype, path)
);
CREATE TABLE IF NOT EXISTS unknown_keys_total(
  source TEXT NOT NULL,
  rtype TEXT NOT NULL,
  path TEXT NOT NULL,
  count INTEGER NOT NULL,
  PRIMARY KEY(source, rtype, path)
) WITHOUT ROWID;
`

// unknownTotalBackfillSQL fills unknown_keys_total from the per-file rows. An
// open runs it in the same transaction that creates the table, only when the
// table did not exist, so an index.db written before the aggregate existed is
// backfilled exactly once and atomically with the table's creation.
const unknownTotalBackfillSQL = `
INSERT INTO unknown_keys_total(source,rtype,path,count)
SELECT source,rtype,path,SUM(count) FROM unknown_keys GROUP BY source,rtype,path;
`

// WithWrite runs fn in one write transaction under the store's write lock. The
// crash point named point (when not empty) is reached after fn succeeds and
// before the commit, so a test can crash exactly there; a panic or an error
// rolls the transaction back.
func (s *Store) WithWrite(point string, fn func(tx *sql.Tx) error) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.batching {
		return s.bulkWriteLocked(point, fn)
	}
	tx, err := s.wdb.Begin()
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			s.rbEpoch.Add(1)
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		s.rbEpoch.Add(1)
		return err
	}
	if point != "" {
		faults.At(point)
	}
	if err := tx.Commit(); err != nil {
		s.rbEpoch.Add(1)
		return err
	}
	done = true
	for _, h := range s.wrHooks {
		h()
	}
	return nil
}

// RollbackEpoch counts the times rows this store accepted may have been undone:
// a write that failed (the whole transaction or just its savepoint in bulk
// mode), a panic, and a batch that did not commit. A cache of ids read from a
// table is valid only while this number is unchanged; a holder compares it
// under the write lock (inside WithWrite) and reloads when it moved.
func (s *Store) RollbackEpoch() uint64 { return s.rbEpoch.Load() }

// Read returns the read-only connection pool (queries read committed state).
func (s *Store) Read() *sql.DB { return s.rdb }

// The secondary indexes of the derived tables, each as its own statement and
// listed largest first (fields_name_n dominates the bytes a load writes). A
// normal open creates them with the tables (derivedSQL); a bulk rebuild creates
// them after the load instead (see buildIndexesLocked in bulk.go), because
// maintaining them row by row writes many times the bytes of building them
// once. The schema is the same either way. records_ident is the one unique
// index: a deferred build checks it only at the end.
var derivedIndexes = []struct{ name, sql string }{
	{"fields_name_n", `CREATE INDEX IF NOT EXISTS fields_name_n ON fields(name_id, n) WHERE n IS NOT NULL`},
	{"records_ident", `CREATE UNIQUE INDEX IF NOT EXISTS records_ident ON records(file_id, gen, off, item)`},
	{"records_session", `CREATE INDEX IF NOT EXISTS records_session ON records(session)`},
	// Serves the per-session transcript facts (newest write per file, newest cwd)
	// without walking and sorting every record of the session.
	{"records_session_source_time", `CREATE INDEX IF NOT EXISTS records_session_source_time ON records(session, source, time_ns)`},
	{"records_time", `CREATE INDEX IF NOT EXISTS records_time ON records(time_ns)`},
	{"records_type", `CREATE INDEX IF NOT EXISTS records_type ON records(type)`},
	// Serves every "WHERE source=? [AND type=?]" lookup (the lifecycle engine's
	// registry, history and control-end reads); without it the planner scans every row.
	{"records_source_type", `CREATE INDEX IF NOT EXISTS records_source_type ON records(source, type)`},
	{"session_files_file", `CREATE INDEX IF NOT EXISTS session_files_file ON session_files(file_id)`},
	{"texts_rec", `CREATE INDEX IF NOT EXISTS texts_rec ON texts(rec_id)`},
}

// derivedIndexesSQL is every derivedIndexes statement as one script.
func derivedIndexesSQL() string {
	var b strings.Builder
	for _, ix := range derivedIndexes {
		b.WriteString(ix.sql + ";\n")
	}
	return b.String()
}

// derivedSQL is the tables and then the indexes: what a normal open runs.
func derivedSQL() string { return derivedTablesSQL + derivedIndexesSQL() }
