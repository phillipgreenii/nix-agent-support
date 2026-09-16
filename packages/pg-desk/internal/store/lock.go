// Locker is pg-desk's cross-process, per-(repo, type, id) mutual-exclusion
// lock, ported from packages/pg-pr/internal/prlock (this packet's Contract
// section: "port internal/prlock's semantics exactly, do not reinvent the
// locking algorithm"). Built on BSD file locks (flock): the kernel releases
// the lock when the holding file descriptor closes or the process dies for
// ANY reason, so there is no crash-recovery protocol to build.
//
// Ported rather than imported: packages/pg-desk and packages/pg-pr are
// separate Go modules in this repo (packages/pg-desk/go.mod declares no
// dependency on packages/pg-pr), and prlock's own package doc already
// states it is a standalone primitive not wired to any call site — the
// same holds here. The design doc's section 7.6 additionally keys the lock
// by repo ("Rows are keyed by repo so a second repository is additive"),
// so the key here is (repo, entityType, entityID) rather than prlock's bare
// per-PR string.
package store

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// LockDefaultTimeout is how long Lock waits for a contended key before
// giving up, when LockerOptions.Timeout is unset. Matches prlock's
// DefaultTimeout, which itself matches this store's busy_timeout(5000) so
// pg-desk's two "how long do we wait for something else to finish" bounds
// agree.
const LockDefaultTimeout = 5 * time.Second

// lockDefaultPollInterval is the NB-flock retry spacing while Lock waits
// for a contended key. Not exposed on LockerOptions: nothing outside this
// package's own tests needs to tune it.
const lockDefaultPollInterval = 25 * time.Millisecond

// ErrLockTimeout is (wrapped and) returned by Lock when the lock could not
// be obtained within the configured Timeout. Use errors.Is(err,
// ErrLockTimeout) to test for it.
var ErrLockTimeout = errors.New("store: timed out waiting for lock")

// LockerOptions configures a Locker. Zero values are accepted.
type LockerOptions struct {
	// LockDir is the directory under which one lock file per sanitised key
	// is created (directory mode 0o700, file mode 0o600 — matching
	// prlock's own conventions). Empty means
	// $XDG_RUNTIME_DIR/pg-desk/locks, falling back to os.TempDir when
	// $XDG_RUNTIME_DIR is unset.
	//
	// Tests MUST inject a t.TempDir() value here instead of relying on the
	// default.
	LockDir string

	// Timeout bounds how long Lock waits for a contended key before
	// giving up with an error wrapping ErrLockTimeout. <=0 means
	// LockDefaultTimeout.
	Timeout time.Duration

	// pollInterval overrides the NB-flock retry spacing. Unexported:
	// production callers get lockDefaultPollInterval; only this package's
	// own tests (same package, so they can reach the field) drive it down
	// further, keeping the timeout tests fast.
	pollInterval time.Duration
}

// Locker acquires cross-process, per-(repo, type, id) mutual-exclusion
// locks. Two Lock calls for DIFFERENT keys never contend; two calls for
// the SAME key serialize — the second blocks (bounded) until the first
// releases, then either succeeds or gives up with ErrLockTimeout.
//
// A Locker is safe for concurrent use by multiple goroutines.
type Locker struct {
	lockDir      string
	timeout      time.Duration
	pollInterval time.Duration
}

// NewLocker returns a Locker configured by opts.
func NewLocker(opts LockerOptions) *Locker {
	lockDir := opts.LockDir
	if lockDir == "" {
		lockDir = filepath.Join(xdgRuntimeDir(), "pg-desk", "locks")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = LockDefaultTimeout
	}
	poll := opts.pollInterval
	if poll <= 0 {
		poll = lockDefaultPollInterval
	}
	return &Locker{lockDir: lockDir, timeout: timeout, pollInterval: poll}
}

// Lock blocks until the per-(repo, entityType, entityID) lock is held, or
// the Locker's configured Timeout elapses — whichever happens first. It
// NEVER returns success without actually holding the lock, and it never
// silently proceeds unlocked.
//
// On success, unlock is non-nil and the caller MUST call it exactly once
// (typically via defer) to release the lock and close its file descriptor.
// On failure, unlock is nil and err wraps ErrLockTimeout.
func (l *Locker) Lock(repo, entityType, entityID string) (unlock func(), err error) {
	key := repo + "/" + entityType + "/" + entityID
	if err := os.MkdirAll(l.lockDir, 0o700); err != nil {
		return nil, fmt.Errorf("store: create lock dir %s: %w", l.lockDir, err)
	}
	path := filepath.Join(l.lockDir, sanitizeLockKey(key)+".lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: open lock %s: %w", path, err)
	}

	deadline := time.Now().Add(l.timeout)
	for {
		ferr := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if ferr == nil {
			return func() {
				_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(ferr, unix.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("store: flock %s: %w", path, ferr)
		}

		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("%w: key %q after %s", ErrLockTimeout, key, l.timeout)
		}

		time.Sleep(l.pollInterval)
	}
}

// sanitizeLockKey maps an opaque per-entity key (containing filesystem-
// meaningful "/" characters) to a single filesystem-safe path element
// suitable for use as a lock filename. Percent-encoding (url.QueryEscape)
// is a REVERSIBLE, injective mapping — no two distinct keys can ever
// sanitise to the same string — mirroring prlock.SanitizeKey.
func sanitizeLockKey(key string) string {
	return url.QueryEscape(key)
}

// xdgRuntimeDir returns $XDG_RUNTIME_DIR or os.TempDir() if unset. Mirrors
// prlock's helper of the same name; duplicated rather than imported so
// this leaf package has no cross-module dependency.
func xdgRuntimeDir() string {
	if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" {
		return v
	}
	return os.TempDir()
}
