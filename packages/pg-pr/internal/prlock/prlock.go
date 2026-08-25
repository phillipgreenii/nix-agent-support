// Package prlock provides a cross-process, per-PR-identity mutual-exclusion
// lock built on BSD file locks (flock), the same mechanism
// internal/sync/daemon.go already uses for its single-instance daemon.lock:
// the kernel releases the lock when the holding file descriptor closes or
// the process dies for ANY reason (including SIGKILL), so there is no
// crash-recovery protocol to build — a PID/lease file would be solving a
// problem flock does not have.
//
// This package is a standalone primitive: it does NOT wire itself into
// internal/beadsbridge's bead-projection path or cmd/pg-pr/pr_write.go's
// `pr create`. That wiring is deliberately left to sibling work.
package prlock

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// DefaultTimeout is how long Acquire waits for a contended key before giving
// up, when Options.Timeout is unset. It matches internal/store's
// busy_timeout(5000) so pg-pr's two "how long do we wait for something else
// to finish" bounds agree.
const DefaultTimeout = 5 * time.Second

// defaultPollInterval is the NB-flock retry spacing while Acquire waits for a
// contended key. It is not exposed on Options: nothing outside this
// package's own tests needs to tune it, and exposing it would be a second
// knob for the same underlying wait.
const defaultPollInterval = 25 * time.Millisecond

// ErrTimeout is (wrapped and) returned by Acquire when the lock could not be
// obtained within the configured Timeout. Use errors.Is(err, ErrTimeout) to
// test for it; ctx cancellation instead returns ctx.Err() directly.
var ErrTimeout = errors.New("prlock: timed out waiting for lock")

// Options configures a Locker. Zero values are accepted.
type Options struct {
	// LockDir is the directory under which one lock file per sanitised key
	// is created (directory mode 0o700, file mode 0o600 — matching
	// internal/sync/daemon.go's daemon.lock conventions). Empty means
	// $XDG_RUNTIME_DIR/pg-pr/locks (a sibling of daemon.lock's own root,
	// falling back to os.TempDir when $XDG_RUNTIME_DIR is unset, mirroring
	// internal/sync's xdgRuntimeDir).
	//
	// Tests MUST inject a t.TempDir() value here instead of relying on the
	// default — never exercise the real XDG runtime dir in a test, where it
	// could contend with a real daemon or a concurrent `go test` run.
	//
	// Wiring an actual production call site to this default is out of scope
	// for this package; only the default value itself is provided.
	LockDir string

	// Timeout bounds how long Acquire waits for a contended key before
	// giving up with an error wrapping ErrTimeout. <=0 means DefaultTimeout.
	//
	// Tests drive this down to milliseconds to exercise the give-up path
	// without waiting out the real default.
	Timeout time.Duration

	// pollInterval overrides the NB-flock retry spacing. Unexported:
	// production callers get defaultPollInterval; only this package's own
	// tests (same package, so they can reach the field) drive it down
	// further, keeping the timeout tests fast.
	pollInterval time.Duration
}

// Locker acquires cross-process, per-PR-identity mutual-exclusion locks.
// Two Acquire calls for DIFFERENT keys never contend; two calls for the SAME
// key serialize — the second blocks (bounded, context-honouring) until the
// first releases, then either succeeds or gives up with ErrTimeout.
//
// A Locker is safe for concurrent use by multiple goroutines.
type Locker struct {
	lockDir      string
	timeout      time.Duration
	pollInterval time.Duration
}

// New returns a Locker configured by opts.
func New(opts Options) *Locker {
	lockDir := opts.LockDir
	if lockDir == "" {
		lockDir = filepath.Join(xdgRuntimeDir(), "pg-pr", "locks")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	poll := opts.pollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}
	return &Locker{lockDir: lockDir, timeout: timeout, pollInterval: poll}
}

// Acquire blocks until the per-PR lock for key is held, the Locker's
// configured Timeout elapses, or ctx is cancelled — whichever happens first.
// It NEVER returns success without actually holding the lock, and it never
// silently proceeds unlocked.
//
// key is an opaque per-PR identity, e.g. "owner/name#123". Acquire sanitises
// it (via SanitizeKey) into a filesystem-safe lock filename under LockDir;
// distinct keys always map to distinct files, so distinct PRs never contend.
//
// On success, release is non-nil and the caller MUST call it exactly once
// (typically via defer) to release the lock and close its file descriptor.
// On failure, release is nil and err is either ctx.Err() (ctx ended first)
// or an error wrapping ErrTimeout (the configured Timeout elapsed first).
func (l *Locker) Acquire(ctx context.Context, key string) (release func(), err error) {
	if err := os.MkdirAll(l.lockDir, 0o700); err != nil {
		return nil, fmt.Errorf("prlock: create lock dir %s: %w", l.lockDir, err)
	}
	path := filepath.Join(l.lockDir, SanitizeKey(key)+".lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("prlock: open lock %s: %w", path, err)
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
			return nil, fmt.Errorf("prlock: flock %s: %w", path, ferr)
		}

		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("%w: key %q after %s", ErrTimeout, key, l.timeout)
		}

		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(l.pollInterval):
		}
	}
}

// SanitizeKey maps an opaque per-PR key (e.g. "owner/name#123", which
// contains filesystem-meaningful "/" and "#" characters) to a single
// filesystem-safe path element suitable for use as a lock filename.
//
// It is percent-encoding (url.QueryEscape), which is a REVERSIBLE mapping —
// so it is injective: no two distinct keys can ever sanitise to the same
// string, which rules out the failure mode a lossy replace-the-separator
// scheme would have (e.g. both "o/r#1" and "o#r/1" collapsing to the same
// "o_r_1"). Exported so it is independently unit-testable and reusable by
// callers that need to predict a lock's on-disk filename.
func SanitizeKey(key string) string {
	return url.QueryEscape(key)
}

// xdgRuntimeDir returns $XDG_RUNTIME_DIR or os.TempDir() if unset. Mirrors
// internal/sync's helper of the same name; duplicated rather than imported
// so this leaf package has no dependency on internal/sync.
func xdgRuntimeDir() string {
	if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" {
		return v
	}
	return os.TempDir()
}
