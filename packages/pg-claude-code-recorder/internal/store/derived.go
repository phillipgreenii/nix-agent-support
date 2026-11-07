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
// stays the column every older store reads and records.session_ref is written
// beside it; a store created empty by this code is marked (metaSessionRef) and
// is read through session_ref, the version bump belongs to a later slice. A database created before these tables existed gets them (and the
// session_ref column, see addSessionRefColumn) at open, but its older rows keep
// session_ref 0 and are absent from the session tables until it is rebuilt.
//
// Lookup tables (schema v3 slice 4). lk_cwd, lk_branch, lk_version,
// lk_entrypoint and lk_user_type intern a string once as (id, v); a record
// carries cwd_id, branch_id, version_id, entrypoint_id and user_type_id (NULL
// when its line has no such value), and sessions carries the first version,
// entrypoint and user type seen. They are append-only and shared, so a reparse
// never deletes a lookup row. The values are still ALSO written as fields rows,
// because the query layer reads those until a later slice moves it to the ids;
// the optional partial indexes on cwd_id and branch_id wait for the
// version-bump slice like the session_ref index.
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
  session_ref INTEGER NOT NULL DEFAULT 0,
  cwd_id INTEGER REFERENCES lk_cwd(id),
  branch_id INTEGER REFERENCES lk_branch(id),
  version_id INTEGER REFERENCES lk_version(id),
  entrypoint_id INTEGER REFERENCES lk_entrypoint(id),
  user_type_id INTEGER REFERENCES lk_user_type(id),
  own_time INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS lk_cwd(id INTEGER PRIMARY KEY, v TEXT NOT NULL UNIQUE) STRICT;
CREATE TABLE IF NOT EXISTS lk_branch(id INTEGER PRIMARY KEY, v TEXT NOT NULL UNIQUE) STRICT;
CREATE TABLE IF NOT EXISTS lk_version(id INTEGER PRIMARY KEY, v TEXT NOT NULL UNIQUE) STRICT;
CREATE TABLE IF NOT EXISTS lk_entrypoint(id INTEGER PRIMARY KEY, v TEXT NOT NULL UNIQUE) STRICT;
CREATE TABLE IF NOT EXISTS lk_user_type(id INTEGER PRIMARY KEY, v TEXT NOT NULL UNIQUE) STRICT;
CREATE TABLE IF NOT EXISTS sessions(
  session_ref INTEGER PRIMARY KEY,
  session_id TEXT NOT NULL UNIQUE,
  project TEXT,
  first_time_ns INTEGER NOT NULL,
  first_version_id INTEGER REFERENCES lk_version(id),
  entrypoint_id INTEGER REFERENCES lk_entrypoint(id),
  user_type_id INTEGER REFERENCES lk_user_type(id)
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
	s.lockWrite()
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
		s.dropPend()
		return err
	}
	if point != "" {
		faults.At(point)
	}
	if err := tx.Commit(); err != nil {
		s.rbEpoch.Add(1)
		s.dropPend()
		return err
	}
	done = true
	for _, h := range s.wrHooks {
		h()
	}
	s.fireOffsets(s.takePend())
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
	// Serves the feed retention-floor query (at_ns < cutoff) without a scan of
	// feed_marks. The table is small and pruned, so building it is cheap.
	{"feed_marks_at", `CREATE INDEX IF NOT EXISTS feed_marks_at ON feed_marks(at_ns)`},
	{"session_files_file", `CREATE INDEX IF NOT EXISTS session_files_file ON session_files(file_id)`},
	{"texts_rec", `CREATE INDEX IF NOT EXISTS texts_rec ON texts(rec_id)`},
}

// metaSessionRef is the meta key that marks a store whose every records row
// carries a session_ref (see SessionRefReady). Only a database created empty by
// code that writes session_ref gets it: a database that existed before has older
// rows with session_ref 0 and no session rows until it is rebuilt, so it never
// gets the key and keeps answering from records.session.
const metaSessionRef = "session_ref_complete"

// sessionRefIndexes are the indexes on records.session_ref. They exist only in
// a store marked with metaSessionRef: building one over the live table of an
// older store would cost a full pass over every row for an index no query could
// use yet. The composite serves both a lookup by session_ref alone (it is the
// prefix) and the per-session transcript facts, so no plain session_ref index is
// kept beside it.
var sessionRefIndexes = []struct{ name, sql string }{
	{"records_session_ref_source_time", `CREATE INDEX IF NOT EXISTS records_session_ref_source_time ON records(session_ref, source, time_ns)`},
}

func sessionRefIndexesSQL() string {
	var b strings.Builder
	for _, ix := range sessionRefIndexes {
		b.WriteString(ix.sql + ";\n")
	}
	return b.String()
}

// derivedSQL is the tables and then the indexes: what a normal open runs.
// derivedTables is derivedTablesSQL, without the trigram table when tri is false
// (the layout of a new index.db built with Options.NoTrigramIndex) and with the
// extension tables when ext is true (Options.ExtensionTables).
//
// sessText is whether records has the legacy session TEXT column; the version 3
// layout (Options.V3Layout) has none and reads the session through session_ref.
func derivedTables(tri, ext, sessText bool) string {
	base := derivedTablesSQL
	if ext {
		base += extDDL()
	}
	if tri && sessText {
		return base
	}
	var b strings.Builder
	for _, l := range strings.SplitAfter(base, "\n") {
		if !tri && strings.Contains(l, "VIRTUAL TABLE IF NOT EXISTS fts_tri ") {
			continue
		}
		if !sessText && l == recordsSessionColumnLine {
			continue
		}
		b.WriteString(l)
	}
	return b.String()
}

// recordsSessionColumnLine is the legacy session column inside derivedTablesSQL.
const recordsSessionColumnLine = "  session TEXT NOT NULL DEFAULT '',\n"

// legacySessionIndexes are the derivedIndexes names that exist only with the
// legacy records.session column.
var legacySessionIndexes = map[string]bool{"records_session": true, "records_session_source_time": true}

// v3OnlyIndexes are the optional partial indexes of the version 3 layout: the
// lookup ids of a record, indexed only where present.
var v3OnlyIndexes = []struct{ name, sql string }{
	{"records_cwd", `CREATE INDEX IF NOT EXISTS records_cwd ON records(cwd_id) WHERE cwd_id IS NOT NULL`},
	{"records_branch", `CREATE INDEX IF NOT EXISTS records_branch ON records(branch_id) WHERE branch_id IS NOT NULL`},
}

// layoutIndexes is the secondary indexes of one layout, largest first: the
// legacy session indexes only with the session column, the lookup-id indexes
// only without it.
func layoutIndexes(sessText bool) []struct{ name, sql string } {
	out := make([]struct{ name, sql string }, 0, len(derivedIndexes)+len(v3OnlyIndexes))
	for _, ix := range derivedIndexes {
		if !sessText && legacySessionIndexes[ix.name] {
			continue
		}
		out = append(out, ix)
	}
	if !sessText {
		out = append(out, v3OnlyIndexes...)
	}
	return out
}

func layoutIndexesSQL(sessText bool) string {
	var b strings.Builder
	for _, ix := range layoutIndexes(sessText) {
		b.WriteString(ix.sql + ";\n")
	}
	return b.String()
}
