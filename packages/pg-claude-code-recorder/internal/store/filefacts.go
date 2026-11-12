package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
)

// MetaFileFactsComplete is the meta key that marks an index.db whose file_facts
// table covers every file that has records. A database created empty by this
// code carries it from the start (the parser maintains the table from the first
// record). A database that existed before the table is backfilled in the
// background, file by file in small transactions, and marked when the backfill
// reaches the last file: a rewrite of the whole table in one transaction would
// hold the write lock for a pass over every record of a multi-gigabyte database.
const MetaFileFactsComplete = "file_facts_complete"

// metaFileFactsCursor is the id of the last file the backfill finished, so a
// restart resumes instead of starting over.
const metaFileFactsCursor = "file_facts_cursor"

// FileFactsComplete reports whether file_facts covers every file with records
// (see MetaFileFactsComplete).
func (s *Store) FileFactsComplete() bool {
	ctx := context.Background()
	var v string
	if err := s.rdb.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, MetaFileFactsComplete).Scan(&v); err != nil {
		return false
	}
	return v == "1"
}

// MarkFileFactsComplete records that the backfill is done.
func (s *Store) MarkFileFactsComplete() error {
	ctx := context.Background()
	return s.WithWrite("", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,'1') ON CONFLICT(key) DO UPDATE SET value='1'`, MetaFileFactsComplete)
		return err
	})
}

// FileFactsCursor returns the id of the last file the backfill finished (0 when
// it has not started).
func (s *Store) FileFactsCursor() int64 {
	ctx := context.Background()
	var v string
	if err := s.rdb.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, metaFileFactsCursor).Scan(&v); err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

// SetFileFactsCursor saves the backfill's position.
func (s *Store) SetFileFactsCursor(id int64) error {
	ctx := context.Background()
	return s.WithWrite("", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, metaFileFactsCursor, strconv.FormatInt(id, 10))
		return err
	})
}

// FileIDsAfter lists up to limit file ids greater than after, ascending.
func (s *Store) FileIDsAfter(ctx context.Context, after int64, limit int) ([]int64, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT id FROM files WHERE id>? ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// FileNewestRecordNS is the newest record time over every generation of a
// file, read from records (the oracle file_facts is checked against). ok is
// false when the file has no records.
func (s *Store) FileNewestRecordNS(ctx context.Context, file int64) (ns int64, ok bool, err error) {
	var m sql.NullInt64
	if err := s.rdb.QueryRowContext(ctx, `SELECT MAX(time_ns) FROM records WHERE file_id=?`, file).Scan(&m); err != nil {
		return 0, false, err
	}
	return m.Int64, m.Valid, nil
}

// FileFactLast returns the stored newest record time of a file; ok is false when
// the file has no file_facts row.
func (s *Store) FileFactLast(ctx context.Context, file int64) (ns int64, ok bool, err error) {
	err = s.rdb.QueryRowContext(ctx, `SELECT last_time_ns FROM file_facts WHERE file_id=?`, file).Scan(&ns)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return ns, err == nil, err
}
