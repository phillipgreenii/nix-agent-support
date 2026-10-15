package lease

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// LockTimeout is the longest any taker of leases/.lock waits.
const LockTimeout = 2 * time.Second

// lockPoll is the retry interval while the lock is held elsewhere.
const lockPoll = 10 * time.Millisecond

// ErrLockTimeout means leases/.lock could not be taken within the bound. A
// client treats the state directory as unwritable (exit 3); the keeper skips
// that tick's sweep and still touches its heartbeat.
var ErrLockTimeout = errors.New("timed out waiting for leases/.lock")

// Lock is a held flock on leases/.lock. Mutating lease operations that must
// be atomic with each other (create, remove, reap) run through it.
type Lock struct {
	d *Dir
	f *os.File
}

// TakeLock takes leases/.lock, waiting at most LockTimeout.
func (d *Dir) TakeLock() (*Lock, error) { return d.takeLock(LockTimeout) }

func (d *Dir) takeLock(timeout time.Duration) (*Lock, error) {
	path := filepath.Join(d.LeasesPath(), ".lock")
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	deadline := time.Now().Add(timeout)
	for {
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &Lock{d: d, f: f}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			f.Close()
			return nil, fmt.Errorf("flock %s: %w", path, err)
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, ErrLockTimeout
		}
		time.Sleep(lockPoll)
	}
}

// Unlock releases the lock. It is safe to call more than once.
func (l *Lock) Unlock() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	return f.Close() // closing the descriptor drops the flock
}
