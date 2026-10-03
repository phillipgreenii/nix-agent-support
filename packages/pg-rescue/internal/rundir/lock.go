package rundir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// lockName is the file the running wrapper holds an flock on.
const lockName = ".lock"

// PruneAge is how old (by the timestamp in its id) a run directory must be
// before pruning may delete it.
const PruneAge = 7 * 24 * time.Hour

// Lock is the running wrapper's claim on its run directory: an exclusive flock
// on <run-dir>/.lock, held until Release (or until the process dies, which the
// kernel treats as a release). Pruning skips a directory whose lock is held.
type Lock struct{ f *os.File }

// Acquire creates <dir>/.lock (mode 0600) and takes an exclusive flock on it.
func Acquire(dir string) (*Lock, error) {
	f, err := os.OpenFile(filepath.Join(dir, lockName), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cannot create the lock file in %s: %w", dir, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("cannot lock %s: %w", dir, err)
	}
	return &Lock{f: f}, nil
}

// Release drops the lock. It is safe to call on a nil Lock and more than once.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

// lockHeld reports whether another process holds the flock on dir's .lock. A
// directory with no readable lock file is treated as not held.
func lockHeld(dir string) bool {
	f, err := os.OpenFile(filepath.Join(dir, lockName), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		return errors.Is(err, syscall.EWOULDBLOCK)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// IDTime parses the timestamp of a run id.
func IDTime(id string) (time.Time, bool) {
	if !IDPattern.MatchString(id) {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("20060102T150405Z", id[:len("20060102T150405Z")], time.UTC)
	return t, err == nil
}

// Prune deletes run directories under <stateRoot>/runs that are old enough to
// be forgotten. It is best effort: nothing it hits is reported. A directory is
// deleted only if ALL of these hold:
//
//   - Lstat shows a real directory directly under runs/ (a symlink, even to a
//     directory, is left alone, and nothing is followed),
//   - its name matches the run-id pattern,
//   - the timestamp in its id is more than PruneAge before now,
//   - its .lock is not held by a running wrapper.
//
// It returns the names it removed.
func Prune(stateRoot string, now time.Time) []string {
	runs := filepath.Join(stateRoot, "runs")
	entries, err := os.ReadDir(runs)
	if err != nil {
		return nil
	}
	var removed []string
	for _, e := range entries {
		name := e.Name()
		ts, ok := IDTime(name)
		if !ok || !now.After(ts.Add(PruneAge)) {
			continue
		}
		path := filepath.Join(runs, name)
		fi, err := os.Lstat(path)
		if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if lockHeld(path) {
			continue
		}
		if os.RemoveAll(path) == nil {
			removed = append(removed, name)
		}
	}
	return removed
}
