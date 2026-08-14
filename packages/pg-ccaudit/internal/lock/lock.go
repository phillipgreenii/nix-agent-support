// Package lock provides the single-instance advisory lock that gates a sweep.
//
// T-12 requires the WRITER to be single-instance and requires a second
// concurrent ingest to DETECT the lock and NO-OP — not to queue, not to contend,
// and not to fail. A ~15 minute launchd tick over a growing corpus can overlap
// with a slow predecessor or with a hand-run `pg-ccaudit ingest`, and two writers
// racing on the same file's resume offset is the one way this design could
// corrupt its own coverage accounting.
//
// The lock is an OS advisory file lock (flock), not a PID file: the kernel drops
// it when the holder dies, so a killed sweep leaves nothing stale behind. That
// matters more here than usual, because T-14 makes abnormally-terminated
// processes an expected case rather than an exception.
package lock

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

// Handle is an acquired lock.
type Handle struct {
	fl *flock.Flock
}

// ErrHeld reports that another writer holds the lock. Callers treat it as a
// NO-OP outcome and exit zero: a skipped tick is correct behaviour, not a
// failure, and surfacing it as an error would make launchd log an alarm every
// time two ticks overlapped.
type ErrHeld struct {
	Path string
}

func (e *ErrHeld) Error() string {
	return fmt.Sprintf("ingest lock held by another process (%s)", e.Path)
}

// DefaultPath places the lock beside the database.
func DefaultPath(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "ingest.lock")
}

// TryAcquire takes the lock without blocking. It returns *ErrHeld when another
// process holds it.
func TryAcquire(path string) (*Handle, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create lock directory %s: %w", dir, err)
		}
	}
	fl := flock.New(path)
	ok, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("acquire lock %s: %w", path, err)
	}
	if !ok {
		return nil, &ErrHeld{Path: path}
	}
	return &Handle{fl: fl}, nil
}

// Release drops the lock.
func (h *Handle) Release() error {
	if h == nil || h.fl == nil {
		return nil
	}
	return h.fl.Unlock()
}
