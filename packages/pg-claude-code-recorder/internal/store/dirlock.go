package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// LockName is the single-instance lock file inside the state directory.
const LockName = "store.lock"

// ErrStateDirLocked is returned by Open when another Store already holds the
// state directory.
var ErrStateDirLocked = errors.New("state directory is held by another recorder instance")

// lockDir takes an exclusive, non-blocking flock on the state directory's lock
// file. The lock lives as long as the returned file stays open, so it is
// released when the holder exits or crashes.
//
// Why this exists: on 2026-11-05 a second daemon started on the same state
// directory while the first was still inside its startup ParseAll. The second
// daemon's openDB waited out busy_timeout(5000), took the busy error for a
// corrupt index.db, renamed the live database aside to index.db.corrupt and
// rebuilt a fresh one, while the first process kept writing the renamed file
// and reaching the new one by path. The first process then logged
// parse_all_failed with UNIQUE constraint failed on records(file_id, gen, off,
// item). Two instances on one state directory must never coexist.
func lockDir(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, LockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrStateDirLocked, dir)
		}
		return nil, err
	}
	return f, nil
}

// isCorruptDBError reports whether an openDB error means the database file
// itself is damaged (SQLITE_CORRUPT or SQLITE_NOTADB). A busy or locked error
// is not corruption: the file is alive and another connection holds it.
func isCorruptDBError(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	for _, k := range []string{"file is not a database", "database disk image is malformed", "SQLITE_CORRUPT", "SQLITE_NOTADB"} {
		if strings.Contains(m, k) {
			return true
		}
	}
	return false
}
