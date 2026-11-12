package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The seen table records the last observation of each file by any actor (a
// watcher event, a sweep, an ingest) so the next start can skip work on files
// that have not changed. It is DERIVED data: it is never written through Rec or
// the journal, and a lost or dropped row costs one re-ingest of that file.

const (
	metaSeenEpoch = "seen_epoch"
	metaSeenToken = "seen_token"
	// seenTokenFile holds the token the meta row carried when the daemon last
	// ran. It lives beside index.db, not in it, so a restored backup of
	// index.db (an older token) no longer matches it.
	seenTokenFile = "seen.token"
)

// SeenRow is one observation: the walk's view of a path when the copier last
// found it fully caught up.
type SeenRow struct {
	Path    string
	File    int64
	Dev     uint64
	Ino     uint64
	BirthNS int64
	Size    int64
	MTimeNS int64
}

// SeenPrepare makes the table safe to trust and reports whether it was
// truncated. It truncates when the build's seen epoch differs from the one the
// table was written under (a downgrade then an upgrade), and when the database
// identity differs from the one recorded beside it (a restored backup). It then
// rotates the identity token, in the database and in the sidecar file.
func (s *Store) SeenPrepare(epoch int) (reset bool, err error) {
	ctx := context.Background()
	tokPath := filepath.Join(s.dir, seenTokenFile)
	side, _ := os.ReadFile(tokPath)
	meta := s.metaString(ctx, metaSeenToken)
	reset = s.metaString(ctx, metaSeenEpoch) != strconv.Itoa(epoch) || meta == "" || strings.TrimSpace(string(side)) != meta
	if reset {
		if err := s.SeenTruncate(); err != nil {
			return false, err
		}
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return reset, err
	}
	tok := hex.EncodeToString(b[:])
	for k, v := range map[string]string{metaSeenEpoch: strconv.Itoa(epoch), metaSeenToken: tok} {
		if _, err := s.wdb.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, v); err != nil {
			return reset, err
		}
	}
	return reset, os.WriteFile(tokPath, []byte(tok), 0o600)
}

// SeenTruncate empties the table.
func (s *Store) SeenTruncate() error {
	ctx := context.Background()
	_, err := s.wdb.ExecContext(ctx, `DELETE FROM seen`)
	return err
}

// SeenCount is the number of rows.
func (s *Store) SeenCount() (int64, error) {
	ctx := context.Background()
	var n int64
	err := s.wdb.QueryRowContext(ctx, `SELECT COUNT(*) FROM seen`).Scan(&n)
	return n, err
}

// SeenLoad returns the rows that still describe the store: the file is not
// deleted, is the same file (device, inode, birth time) and generation it was
// when the row was written, its stored last_size equals the row's size, and the
// path is still one of its open paths (a closed, renamed or tombstoned path is
// not).
func (s *Store) SeenLoad() ([]SeenRow, error) {
	ctx := context.Background()
	rows, err := s.wdb.QueryContext(ctx, `SELECT n.path,n.file_id,n.dev,n.ino,n.birth_ns,n.size,n.mtime_ns
FROM seen n JOIN files f ON f.id=n.file_id
WHERE f.state<>'deleted' AND f.last_size=n.size AND f.cur_gen=n.gen
  AND f.dev=n.dev AND f.ino=n.ino AND f.birth_ns=n.birth_ns
  AND EXISTS(SELECT 1 FROM paths p WHERE p.file_id=n.file_id AND p.path=n.path AND p.closed_ns IS NULL)`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SeenRow
	for rows.Next() {
		var r SeenRow
		var dev, ino int64
		if err := rows.Scan(&r.Path, &r.File, &dev, &ino, &r.BirthNS, &r.Size, &r.MTimeNS); err != nil {
			return nil, err
		}
		r.Dev, r.Ino = uint64(dev), uint64(ino)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SeenWrite applies one batch in one transaction: put rows are replaced (the
// row's generation is read from the file's current one), del paths removed.
func (s *Store) SeenWrite(put []SeenRow, del []string) error {
	ctx := context.Background()
	if len(put) == 0 && len(del) == 0 {
		return nil
	}
	tx, err := s.wdb.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, p := range del {
		if _, err := tx.ExecContext(ctx, `DELETE FROM seen WHERE path=?`, p); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	for _, r := range put {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO seen(path,file_id,dev,ino,birth_ns,size,mtime_ns,gen)
SELECT ?,id,?,?,?,?,?,cur_gen FROM files WHERE id=?`, r.Path, int64(r.Dev), int64(r.Ino), r.BirthNS, r.Size, r.MTimeNS, r.File); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// SeenPrune deletes the rows whose path is not in keep (the paths a complete
// walk found) and returns how many it removed.
func (s *Store) SeenPrune(keep map[string]bool) (int, error) {
	ctx := context.Background()
	rows, err := s.wdb.QueryContext(ctx, `SELECT path FROM seen`)
	if err != nil {
		return 0, err
	}
	var gone []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if !keep[p] {
			gone = append(gone, p)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	return len(gone), s.SeenWrite(nil, gone)
}

var _ = sql.ErrNoRows
