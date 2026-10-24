package prchange

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ErrLockTimeout is returned by AcquireLock when the per-PR lock was not free
// within the wait.
var ErrLockTimeout = errors.New("per-PR lock is held by another run")

// lockPoll is how often a waiting caller retries the non-blocking lock. The
// wait is bounded by the caller's wait, so this is not a retry of any work,
// only the lock acquisition the design requires to be bounded.
const lockPoll = 25 * time.Millisecond

// Lock is a held per-PR lock: an exclusive flock on a file keyed by the
// normalized PR id. The kernel drops it when the holder exits, so a killed
// holder releases it.
type Lock struct {
	f *os.File
}

// LockPath is the lock file of a normalized PR id under dir. The key is the
// lower-cased id (the forge treats repository names case-insensitively),
// hashed so it is one safe file name whatever the id holds.
func LockPath(dir, id string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(id)))
	return filepath.Join(dir, fmt.Sprintf("%x.lock", sum[:12]))
}

// AcquireLock takes the exclusive per-PR lock for id under dir, creating dir
// when needed, and waits at most wait for a holder to let go. It returns
// ErrLockTimeout when the wait ran out (or ctx ended first); any other error
// means the lock file could not be created or opened.
func AcquireLock(ctx context.Context, dir, id string, wait time.Duration) (*Lock, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create lock directory %s: %w", dir, err)
	}
	f, err := os.OpenFile(LockPath(dir, id), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file for %s: %w", id, err)
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(lockPoll)
	defer tick.Stop()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &Lock{f: f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", id, err)
		}
		select {
		case <-deadline.C:
			_ = f.Close()
			return nil, ErrLockTimeout
		case <-ctx.Done():
			_ = f.Close()
			return nil, ErrLockTimeout
		case <-tick.C:
		}
	}
}

// Release drops the lock. It is safe to call on a nil Lock.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}
