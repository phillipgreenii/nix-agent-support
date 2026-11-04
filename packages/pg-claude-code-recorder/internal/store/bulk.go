package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
)

// Bulk rebuild mode. A rebuild is repeatable from the journal and the copies,
// so while it runs the index trades durability for speed: one write
// transaction spans many files and commits on a record or byte budget,
// synchronous is OFF, the page cache is large, the WAL autocheckpoint is
// raised and FTS5 automerge is off. The normal serve path keeps its durability.
//
// Failure semantics: a marker row (meta key metaRebuildUnfinished) is written
// and made durable BEFORE any of that is relaxed, and cleared LAST by
// FinishRebuild once synchronous is back to FULL and the FTS5 indexes are
// merged. A rebuild killed in between leaves the marker, and Open refuses the
// file with an UnfinishedRebuildError until the operator reruns the rebuild.

const (
	metaRebuildUnfinished = "rebuild_unfinished"

	// DefaultBulkRecords and DefaultBulkBytes are the commit budgets of a
	// bulk rebuild: the open transaction commits when it holds this many
	// records or this many bytes of parsed copy bytes.
	DefaultBulkRecords = 100_000
	DefaultBulkBytes   = 64 << 20

	// bulkCacheKiB is the SQLite page cache of a bulk rebuild (a negative
	// cache_size is in KiB): 256 MiB, a fixed ceiling well under the memory
	// of the machines the recorder runs on.
	bulkCacheKiB = 256 << 10
	// bulkAutocheckpointPages is the WAL autocheckpoint of a bulk rebuild:
	// 262144 pages of 4 KiB = 1 GiB, matching the journal_size_limit.
	bulkAutocheckpointPages = 262144
)

// UnfinishedRebuildError means index.db is the remains of a rebuild that did
// not complete; it must not be served.
type UnfinishedRebuildError struct{}

func (*UnfinishedRebuildError) Error() string {
	return "index.db is an unfinished rebuild (the previous rebuild did not complete); " +
		"run pg-claude-code-recorder rebuild again"
}

func (s *Store) metaString(key string) string {
	var v string
	if err := s.wdb.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v); err != nil {
		return ""
	}
	return v
}

// bulkDSNPragmas is appended to the writer DSN of a bulk rebuild.
func bulkDSNPragmas() string {
	return "&_pragma=synchronous(OFF)&_pragma=cache_size(-" + strconv.Itoa(bulkCacheKiB) + ")" +
		"&_pragma=wal_autocheckpoint(" + strconv.Itoa(bulkAutocheckpointPages) + ")"
}

// beginRebuild writes the unfinished-rebuild marker durably (synchronous FULL,
// checkpointed), then relaxes durability on the writer connection.
func (s *Store) beginRebuild() error {
	ctx := context.Background()
	conn, err := s.wdb.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	for _, q := range []string{
		`PRAGMA synchronous=FULL`,
		`INSERT INTO meta(key,value) VALUES('` + metaRebuildUnfinished + `','1') ON CONFLICT(key) DO UPDATE SET value='1'`,
		`PRAGMA wal_checkpoint(FULL)`,
		`INSERT INTO fts_word(fts_word,rank) VALUES('automerge',0)`,
		`INSERT INTO fts_tri(fts_tri,rank) VALUES('automerge',0)`,
		`PRAGMA synchronous=OFF`,
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	s.bulk = true
	return nil
}

// FinishRebuild completes a bulk rebuild: it commits what is open, restores
// synchronous=FULL, the default FTS5 automerge and autocheckpoint, merges the
// FTS5 segments, checkpoints the WAL into the database file, and only then
// clears the unfinished marker. A store not opened for a bulk rebuild is left
// alone.
func (s *Store) FinishRebuild() error {
	if !s.bulk {
		return nil
	}
	if err := s.BulkEnd(); err != nil {
		return err
	}
	ctx := context.Background()
	s.mu.Lock()
	defer s.mu.Unlock()
	conn, err := s.wdb.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	exec := func(q string) error { _, err := conn.ExecContext(ctx, q); return err }
	for _, q := range []string{
		`PRAGMA synchronous=FULL`,
		`PRAGMA wal_autocheckpoint=1000`,
		`INSERT INTO fts_word(fts_word,rank) VALUES('automerge',4)`,
		`INSERT INTO fts_tri(fts_tri,rank) VALUES('automerge',4)`,
		`INSERT INTO fts_word(fts_word) VALUES('optimize')`,
		`INSERT INTO fts_tri(fts_tri) VALUES('optimize')`,
	} {
		if err := exec(q); err != nil {
			return err
		}
	}
	// The data is durable (a TRUNCATE checkpoint syncs the database file)
	// before the marker is cleared; the marker clear is itself a FULL commit.
	var busy, log, ckpt int
	if err := conn.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &log, &ckpt); err != nil {
		return err
	}
	if busy != 0 {
		return errors.New("store: wal checkpoint blocked while finishing the rebuild")
	}
	if err := exec(`DELETE FROM meta WHERE key='` + metaRebuildUnfinished + `'`); err != nil {
		return err
	}
	if err := exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return err
	}
	s.bulk = false
	return nil
}

// BulkBegin starts batching WithWrite calls into one transaction that spans
// many calls. It does nothing on a store not opened for a bulk rebuild.
func (s *Store) BulkBegin() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bulk {
		s.batching = true
	}
}

// BulkAdd counts records and parsed copy bytes into the open batch and commits
// it when a budget is reached.
func (s *Store) BulkAdd(records int, bytes int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.batching {
		return nil
	}
	s.bRecs += records
	s.bBytes += bytes
	if s.bRecs >= s.bulkRecords || s.bBytes >= s.bulkBytes {
		return s.commitBatchLocked()
	}
	return nil
}

// BulkFlush commits the open batch now (a reader of this process sees the
// records only after it).
func (s *Store) BulkFlush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commitBatchLocked()
}

// BulkEnd commits the open batch and stops batching.
func (s *Store) BulkEnd() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.commitBatchLocked()
	s.batching = false
	return err
}

// BulkCommits returns how many batch transactions a bulk rebuild committed.
func (s *Store) BulkCommits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bCommits
}

func (s *Store) commitBatchLocked() error {
	if s.btx == nil {
		return nil
	}
	tx := s.btx
	s.btx = nil
	s.bRecs, s.bBytes = 0, 0
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	faults.At("rebuild.before-batch-commit")
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	s.bCommits++
	for _, h := range s.wrHooks {
		h()
	}
	return nil
}

// bulkWriteLocked runs fn in a savepoint of the batch transaction, so an
// error undoes only fn's work.
func (s *Store) bulkWriteLocked(point string, fn func(tx *sql.Tx) error) error {
	if s.btx == nil {
		tx, err := s.wdb.Begin()
		if err != nil {
			return err
		}
		s.btx = tx
	}
	tx := s.btx
	if _, err := tx.Exec(`SAVEPOINT bulk_write`); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_, _ = tx.Exec(`ROLLBACK TO bulk_write`)
		_, _ = tx.Exec(`RELEASE bulk_write`)
		return err
	}
	if point != "" {
		faults.At(point)
	}
	_, err := tx.Exec(`RELEASE bulk_write`)
	return err
}
