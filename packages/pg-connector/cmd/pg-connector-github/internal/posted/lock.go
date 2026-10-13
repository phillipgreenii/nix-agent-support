package posted

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// LockWait is the bounded wait for the per-PR lock.
const LockWait = 60 * time.Second

// defaultPoll is how often a waiting Acquire retries the non-blocking flock.
const defaultPoll = 50 * time.Millisecond

// ErrUnavailable is wrapped by Acquire when the lock could not be taken within
// the bounded wait. It is retryable: the caller maps it to the taxonomy code
// "unavailable" (INV-ERR-1 in packages/pg-connector/docs/behavior/invariants.md).
var ErrUnavailable = errors.New("posted: per-PR lock unavailable")

// Locker takes advisory flock(2) locks at <Dir>/<owner>__<repo>__<n>.lock.
type Locker struct {
	// Dir is the locks/ directory.
	Dir string
	// Wait bounds how long Acquire waits; zero means LockWait.
	Wait time.Duration
	// Poll is the retry interval; zero means 50ms.
	Poll time.Duration
}

// LockerFromEnv builds the production locker at <state home>/locks with the
// 60-second bounded wait.
func LockerFromEnv(getenv func(string) string) Locker {
	home := StateHomeFromEnv(getenv)
	if home == "" {
		return Locker{Wait: LockWait}
	}
	return Locker{Dir: filepath.Join(home, LocksDirName), Wait: LockWait}
}

// Lock is a held per-PR lock.
type Lock struct {
	f *os.File
}

// Acquire takes the lock for a PR, waiting up to Wait. On timeout it returns
// an error wrapping ErrUnavailable. A crash releases the lock with the
// process; lock files are tiny and are never cleaned up.
func (l Locker) Acquire(owner, repo string, pr int) (*Lock, error) {
	if l.Dir == "" {
		return nil, errors.New("posted: no state directory could be determined (set " + EnvStateDir + ")")
	}
	stem, err := fileStem(owner, repo, pr)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("posted: create lock directory: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(l.Dir, stem+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("posted: open lock file: %w", err)
	}
	wait, poll := l.Wait, l.Poll
	if wait <= 0 {
		wait = LockWait
	}
	if poll <= 0 {
		poll = defaultPoll
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &Lock{f: f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, fmt.Errorf("posted: flock: %w", err)
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("%w: %s/%s#%d held elsewhere for %s", ErrUnavailable, owner, repo, pr, wait)
		}
		time.Sleep(poll)
	}
}

// Release drops the lock. It is safe to call more than once.
func (k *Lock) Release() error {
	if k == nil || k.f == nil {
		return nil
	}
	f := k.f
	k.f = nil
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return f.Close()
}
