// Package sessionlock is the per-session mutual exclusion between a handler
// process reclaiming an orphaned ccpool session (the dispatch-time orphan
// reconcile) and a dispatch absorbing that same session as a duplicate (bead
// pg2-g2u9m, INV-CCH-18).
//
// The lock is a non-blocking exclusive flock on <dir>/<external_id>.lock, where
// dir lives under the handler state directory (the same one as events.jsonl).
// It is deliberately the handler's OWN lock: ccpool's per-external_id lock is
// internal to ccpool and has no CLI, and `ccpool meta set` is an unconditional
// upsert, so the lease value cannot serve as a compare-and-set. flock is per
// open file description, so two holders conflict even inside one process, and
// the kernel releases the lock if the holder dies -- a crashed reclaimer never
// wedges a session.
package sessionlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrHeld is returned by TryLock when another holder has the lock.
var ErrHeld = errors.New("sessionlock: held by another holder")

// Dir returns the lock directory under the handler state directory stateDir.
func Dir(stateDir string) string { return filepath.Join(stateDir, "locks") }

// Lock is a held session lock. Unlock releases it; the zero value and a nil
// *Lock are safe no-ops.
type Lock struct{ f *os.File }

// path maps an external_id to its lock file under dir. Characters outside
// [A-Za-z0-9._-] become "_", so an id can never escape dir.
func path(dir, externalID string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		}
		return '_'
	}, externalID)
	return filepath.Join(dir, name+".lock")
}

// WorktreeKey is the lock key for the per-bead worktree of beadID. It is the
// same flock mechanism as a session lock but a different key space (an
// external_id never starts with "worktree--"), used to serialize the
// worktree-keyed sweep against live dispatches (bead pg2-ganjb, INV-CCH-19): a
// dispatch holds it SHARED from before it creates the worktree until it
// returns, and the sweep takes it EXCLUSIVE.
func WorktreeKey(beadID string) string { return "worktree--" + beadID }

// TryRLock takes a SHARED lock for externalID without blocking: any number of
// shared holders coexist, and they conflict only with an exclusive holder
// (TryLock). It returns ErrHeld when an exclusive holder has it. As with
// TryLock, the kernel drops the lock if the holder dies, which is exactly the
// "this dispatch is alive" signal the worktree sweep reads.
func TryRLock(dir, externalID string) (*Lock, error) {
	return tryFlock(dir, externalID, syscall.LOCK_SH)
}

// TryLock takes the exclusive lock for externalID without blocking. It returns
// ErrHeld when another holder has it, and any other error when the lock file
// cannot be created or locked (the caller MUST treat that as "do not act").
func TryLock(dir, externalID string) (*Lock, error) {
	return tryFlock(dir, externalID, syscall.LOCK_EX)
}

func tryFlock(dir, externalID string, how int) (*Lock, error) {
	if externalID == "" {
		return nil, errors.New("sessionlock: empty external id")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("sessionlock: %w", err)
	}
	f, err := os.OpenFile(path(dir, externalID), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("sessionlock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, fmt.Errorf("sessionlock: flock: %w", err)
	}
	return &Lock{f: f}, nil
}

// Unlock releases the lock. Closing the descriptor drops the flock; the lock
// file itself is left in place (removing it would race a concurrent TryLock on
// the same path).
func (l *Lock) Unlock() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}
