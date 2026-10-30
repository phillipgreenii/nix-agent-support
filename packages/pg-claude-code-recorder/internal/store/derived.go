package store

import (
	"database/sql"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
)

// derivedSQL is the parser's part of index.db: records, their typed fields,
// the text indexes (a word index and a trigram index, both contentless) and the
// parse accounting. It is derived from the copies, so a rebuilt database simply
// parses again. Full file contents are never stored here.
const derivedSQL = `
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
  time_ns INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS records_ident ON records(file_id, gen, off, item);
CREATE INDEX IF NOT EXISTS records_session ON records(session);
CREATE INDEX IF NOT EXISTS records_time ON records(time_ns);
CREATE INDEX IF NOT EXISTS records_type ON records(type);
CREATE TABLE IF NOT EXISTS fields(
  rec_id INTEGER NOT NULL,
  name TEXT NOT NULL,
  s TEXT,
  n INTEGER,
  f REAL
);
CREATE INDEX IF NOT EXISTS fields_rec ON fields(rec_id, name);
CREATE INDEX IF NOT EXISTS fields_name_n ON fields(name, n);
CREATE TABLE IF NOT EXISTS texts(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  rec_id INTEGER NOT NULL,
  field TEXT NOT NULL,
  kind TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS texts_rec ON texts(rec_id);
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
`

// WithWrite runs fn in one write transaction under the store's write lock. The
// crash point named point (when not empty) is reached after fn succeeds and
// before the commit, so a test can crash exactly there; a panic or an error
// rolls the transaction back.
func (s *Store) WithWrite(point string, fn func(tx *sql.Tx) error) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.wdb.Begin()
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if point != "" {
		faults.At(point)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	done = true
	for _, h := range s.wrHooks {
		h()
	}
	return nil
}

// Read returns the read-only connection pool (queries read committed state).
func (s *Store) Read() *sql.DB { return s.rdb }
