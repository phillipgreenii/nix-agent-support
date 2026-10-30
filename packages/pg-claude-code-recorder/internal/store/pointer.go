package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ArchivePath is where a settled generation's bytes live once the settle step
// moves them out of copies/ (phase 5 writes them; the pointer resolution below
// already reads them, so raw, snippets, substring checks and field reads work
// the same for a settled Conversation).
func (s *Store) ArchivePath(file int64, gen int) string {
	return filepath.Join(s.dir, ArchivesDir, fmt.Sprintf("%d.%d", file, gen))
}

// OpenPointer opens the bytes a record pointer (file id, generation) names: the
// copy of an append file's generation, the stored bytes of a snapshot version
// (gen is the version), or the archive holding them. It is the one abstraction
// every reader of stored bytes goes through.
func (s *Store) OpenPointer(file int64, gen int) (*os.File, error) {
	mode := ""
	_ = s.rdb.QueryRow(`SELECT mode FROM files WHERE id=?`, file).Scan(&mode)
	primary := s.CopyPath(file, gen)
	if mode == "snapshot" {
		primary = s.SnapshotPath(file, gen)
	}
	for _, p := range []string{primary, s.ArchivePath(file, gen)} {
		f, err := os.Open(p)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("store: no bytes for file %d generation %d: %w", file, gen, ErrNotFound)
}

// ReadPointer reads up to n bytes at off through OpenPointer.
func (s *Store) ReadPointer(file int64, gen int, off int64, n int) ([]byte, error) {
	f, err := s.OpenPointer(file, gen)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, n)
	m, err := f.ReadAt(buf, off)
	if err != nil && m == 0 && n > 0 {
		return nil, err
	}
	return buf[:m], nil
}
