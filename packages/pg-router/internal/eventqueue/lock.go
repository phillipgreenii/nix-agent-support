package eventqueue

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// This file is the write-ahead log's SECOND-OPENER PROTECTION (bead pg2-maxn1,
// the lock half of pg2-8e0m6 item 6).
//
// Nothing else stops two processes from opening the same queue.jsonl: the daemon's
// "already running" refusal (core.Listen's ErrAlreadyRunning) fires only AFTER the
// queue has replayed — and, since startup compaction, rewritten and renamed — the
// log. A double start (launchd beside a hand-run daemon, a run-until-idle beside
// the daemon) would therefore compact and rename under a live writer, which keeps
// appending to the unlinked inode and loses every later record without a sound.
//
// So FileStore takes an exclusive, non-blocking flock before it touches the log.
// The lock lives on a sibling file (queue.jsonl.lock) because compaction renames a
// NEW inode over queue.jsonl; a flock on the log itself would silently stop
// protecting anything after the first compaction. The lock file is created empty
// and left in place (it is never the data); the kernel drops the lock when the
// holder closes it or dies, so a crash never leaves a stale lock.

// lockSuffix names the lock file beside the log.
const lockSuffix = ".lock"

func lockPath(logPath string) string { return logPath + lockSuffix }

// ErrLogLocked is returned by NewFileStore (and the offline compaction built on
// it) when another process — or another FileStore in this one — holds the log.
type ErrLogLocked struct {
	// Path is the log path; LockPath the lock file that is held.
	Path, LockPath string
}

func (e *ErrLogLocked) Error() string {
	return fmt.Sprintf("the event log %s is in use by another pg-router process (lock %s is held); "+
		"a pg-router daemon is probably already running against this log directory", e.Path, e.LockPath)
}

// lockLog creates (if needed) and exclusively flocks the lock file beside logPath,
// without blocking. The returned file holds the lock until it is closed.
func lockLog(logPath string) (*os.File, error) {
	lp := lockPath(logPath)
	f, err := os.OpenFile(lp, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := flockTry(f); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &ErrLogLocked{Path: logPath, LockPath: lp}
		}
		return nil, fmt.Errorf("eventqueue: lock %s: %w", lp, err)
	}
	return f, nil
}

func flockTry(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

// LogLocked reports whether another holder currently has the log at logPath
// locked. It is the read-only probe a dry-run uses: it never creates the lock
// file (an absent one means nothing has ever opened the log, so nobody holds it)
// and never keeps the lock — it takes it and drops it in one step.
func LogLocked(logPath string) (bool, error) {
	f, err := os.OpenFile(lockPath(logPath), os.O_RDONLY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = f.Close() }()
	if err := flockTry(f); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return true, nil
		}
		return false, err
	}
	return false, nil // closing f drops the lock we just took
}
