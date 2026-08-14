package query

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Staleness describes how far the index lags the corpus on disk.
//
// The query path REPORTS this and stops (T-13). It never triggers an ingest,
// bounded or otherwise. That mirrors this machine's standing posture against
// tools that transparently start their own background work: the caller decides,
// and a query that silently kicked off a multi-minute sweep is exactly the
// surprise that posture exists to prevent.
type Staleness struct {
	Root          string
	FilesOnDisk   int
	FilesIndexed  int
	FilesMissing  int
	FilesBehind   int
	OpenFiles     int
	BytesBehind   int64
	NewestOnDisk  time.Time
	NewestIndexed time.Time
	LinesBad      int64
	// RootErr records a corpus root that could not be walked. The index is still
	// queryable; only the staleness comparison is unavailable.
	RootErr error
}

// Stale reports whether anything on disk is not yet in the index.
func (s Staleness) Stale() bool {
	return s.FilesMissing > 0 || s.FilesBehind > 0 || s.BytesBehind > 0
}

// Note is the one-line advisory printed alongside every query result. It goes to
// STDERR so machine-readable stdout stays pure.
func (s Staleness) Note() string {
	if s.RootErr != nil {
		return fmt.Sprintf("staleness: UNKNOWN — corpus root %s unreadable (%v); index holds %d file(s)",
			s.Root, s.RootErr, s.FilesIndexed)
	}
	var sb strings.Builder
	if !s.Stale() {
		fmt.Fprintf(&sb, "staleness: index current — %d file(s) indexed, %d still open",
			s.FilesIndexed, s.OpenFiles)
	} else {
		fmt.Fprintf(&sb,
			"staleness: index BEHIND — %d file(s) on disk, %d indexed (%d never indexed, %d changed since), %d byte(s) unparsed",
			s.FilesOnDisk, s.FilesIndexed, s.FilesMissing, s.FilesBehind, s.BytesBehind)
	}
	if !s.NewestOnDisk.IsZero() {
		fmt.Fprintf(&sb, "; newest transcript %s", s.NewestOnDisk.UTC().Format(time.RFC3339))
		if !s.NewestIndexed.IsZero() {
			fmt.Fprintf(&sb, ", newest indexed %s", s.NewestIndexed.UTC().Format(time.RFC3339))
		} else {
			sb.WriteString(", nothing indexed")
		}
	}
	if s.LinesBad > 0 {
		fmt.Fprintf(&sb, "; %d malformed line(s) skipped", s.LinesBad)
	}
	if s.Stale() {
		sb.WriteString(". Results below EXCLUDE the unindexed remainder. " +
			"This command does NOT ingest — run `pg-ccaudit ingest` (or wait for the scheduled sweep) if that matters.")
	}
	return sb.String()
}

// Measure compares the index against the corpus on disk. It only READS.
func Measure(ctx context.Context, db *sql.DB, root string) Staleness {
	s := Staleness{Root: root}

	indexed := map[string]struct {
		size  int64
		mtime int64
	}{}
	rows, err := db.QueryContext(ctx, `SELECT path, size, mtime, complete FROM files`)
	if err != nil {
		s.RootErr = fmt.Errorf("read files table: %w", err)
		return s
	}
	func() {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var p string
			var size, mtime, complete int64
			if err := rows.Scan(&p, &size, &mtime, &complete); err != nil {
				continue
			}
			indexed[p] = struct {
				size  int64
				mtime int64
			}{size, mtime}
			if complete == 0 {
				s.OpenFiles++
			}
			if t := time.Unix(0, mtime); t.After(s.NewestIndexed) {
				s.NewestIndexed = t
			}
		}
	}()
	s.FilesIndexed = len(indexed)

	_ = db.QueryRowContext(ctx, `SELECT COALESCE(SUM(lines_bad), 0) FROM files`).Scan(&s.LinesBad)

	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil
		}
		s.FilesOnDisk++
		if t := st.ModTime(); t.After(s.NewestOnDisk) {
			s.NewestOnDisk = t
		}
		prev, ok := indexed[p]
		if !ok {
			s.FilesMissing++
			s.BytesBehind += st.Size()
			return nil
		}
		if st.Size() != prev.size || st.ModTime().UnixNano() != prev.mtime {
			s.FilesBehind++
			if d := st.Size() - prev.size; d > 0 {
				s.BytesBehind += d
			}
		}
		return nil
	})
	if walkErr != nil {
		s.RootErr = walkErr
	}
	return s
}
