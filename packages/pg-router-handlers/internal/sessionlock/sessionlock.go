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
//
// Lock files are garbage-collected by SweepStale (bead pg2-bjhoq): without it
// one empty file per session id and per bead accumulated forever. Removal is
// safe against a concurrent opener only because (1) the sweeper unlinks while
// holding the exclusive flock and (2) every opener, after taking its flock,
// re-verifies that the path still names the inode it locked and retries
// otherwise. The "removal would race a concurrent TryLock" caveat that used to
// forbid any deletion was exactly the missing step (2).
package sessionlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
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

// afterOpenHook is a test seam: it runs between a tryFlock's open and its flock,
// the exact window in which SweepStale can unlink the file. nil in production.
var afterOpenHook func()

func tryFlock(dir, externalID string, how int) (*Lock, error) {
	if externalID == "" {
		return nil, errors.New("sessionlock: empty external id")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("sessionlock: %w", err)
	}
	p := path(dir, externalID)
	for {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return nil, fmt.Errorf("sessionlock: %w", err)
		}
		if afterOpenHook != nil {
			afterOpenHook()
		}
		if err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB); err != nil {
			_ = f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, ErrHeld
			}
			return nil, fmt.Errorf("sessionlock: flock: %w", err)
		}
		// Re-verify that the path still names the inode we just locked: SweepStale
		// may have unlinked it between our open and our flock (see the package
		// comment). A lock on an unlinked inode excludes nobody, so retry on the
		// live file rather than report a lock we do not effectively hold.
		held, herr := f.Stat()
		onDisk, perr := os.Lstat(p)
		if herr != nil || perr != nil || !os.SameFile(held, onDisk) {
			_ = f.Close()
			continue
		}
		// Record the use, so a lock file's mtime is its LAST acquisition and the
		// sweep's age gate measures idleness. Best effort.
		now := time.Now()
		_ = os.Chtimes(p, now, now)
		return &Lock{f: f}, nil
	}
}

// DefaultStaleAge is how long a lock file must have gone unused before
// SweepStale may remove it.
const DefaultStaleAge = 7 * 24 * time.Hour

// SweepStale removes lock files under dir that nobody has acquired for longer
// than olderThan and that nobody holds right now; it returns how many it
// removed. Best effort: any file it cannot prove safe is left for a later pass.
//
// A file is removed only when ALL hold: the name ends in ".lock" and it is a
// regular file; its mtime (stamped by every TryLock/TryRLock acquisition made by
// this build) is older than olderThan; a NON-BLOCKING EXCLUSIVE flock on it
// succeeds, which means no shared (worktree dispatch) or exclusive holder exists;
// the path still names the locked inode; and the mtime is re-read after the lock
// is held. The unlink happens while the exclusive flock is held.
//
// Why this does not race a concurrent TryLock/TryRLock (the caveat Unlock's
// comment used to cite): an opener that has the file open but has not yet taken
// its flock either (a) loses to the sweeper's exclusive flock and gets ErrHeld,
// the same answer as any other holder, or (b) gets its flock after the sweeper
// released, on an inode that is already unlinked; tryFlock then sees that the
// path no longer names its inode and retries on a fresh file. An opener that
// verified first holds the flock, so the sweeper cannot take it and cannot unlink
// that inode. Mixed builds: a handler process of an OLDER build does not
// re-verify, so its only exposure is an open that straddles the unlink of a file
// idle for over olderThan; the age gate (days, against a dispatch lasting
// minutes) makes that practically unreachable.
func SweepStale(dir string, olderThan time.Duration, now time.Time) int {
	des, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, de := range des {
		name := de.Name()
		if !strings.HasSuffix(name, ".lock") || len(name) == len(".lock") {
			continue
		}
		fi, err := de.Info()
		if err != nil || !fi.Mode().IsRegular() || now.Sub(fi.ModTime()) <= olderThan {
			continue
		}
		if removeIfIdle(filepath.Join(dir, name), olderThan, now) {
			removed++
		}
	}
	return removed
}

// removeIfIdle is the locked half of SweepStale for one file.
func removeIfIdle(p string, olderThan time.Duration, now time.Time) bool {
	f, err := os.OpenFile(p, os.O_RDWR, 0) // no O_CREATE: never resurrect a vanished file
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }() // closing drops the flock, after the unlink
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return false // held (shared or exclusive), or unlockable: leave it
	}
	held, err := f.Stat()
	if err != nil || !held.Mode().IsRegular() || now.Sub(held.ModTime()) <= olderThan {
		return false
	}
	onDisk, err := os.Lstat(p)
	if err != nil || !os.SameFile(held, onDisk) {
		return false
	}
	return os.Remove(p) == nil
}

// Unlock releases the lock. Closing the descriptor drops the flock; the lock
// file itself is left in place. Only SweepStale ever removes one, and only
// under the exclusive flock with openers re-verifying the inode (see its
// comment), so a plain Unlock-then-unlink would still race a concurrent TryLock.
func (l *Lock) Unlock() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}
