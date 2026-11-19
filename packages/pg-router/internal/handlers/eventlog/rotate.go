package eventlog

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
	"time"
)

// Retention bounds one event log. A log is rotated when it reaches MaxBytes:
// path becomes path.1, the previous path.1 becomes path.2, and so on; the
// generation past Keep is deleted. A rotated generation whose mtime is older
// than MaxAge is deleted too (checked at each rotation). Worst case on disk is
// about (Keep+1) x MaxBytes.
type Retention struct {
	MaxBytes int64         // rotate at this size; <= 0 disables rotation
	Keep     int           // rotated generations kept (path.1 .. path.Keep); < 0 treated as 0
	MaxAge   time.Duration // delete rotated generations older than this; <= 0 keeps them by age
}

// Defaults. A busy router wrote ~85 MB of events.jsonl in weeks, so 25 MiB
// with three kept generations holds weeks of history in at most ~100 MiB.
const (
	DefaultMaxBytes = 25 << 20
	DefaultKeep     = 3
	DefaultMaxAge   = 14 * 24 * time.Hour
)

// Environment overrides read by New (both the router and the ccpool handler
// share them; unset or unparsable values fall back to the defaults).
const (
	EnvMaxBytes = "PG_ROUTER_EVENTS_MAX_BYTES" // bytes; 0 disables rotation
	EnvKeep     = "PG_ROUTER_EVENTS_KEEP"      // rotated generations to keep
	EnvMaxAge   = "PG_ROUTER_EVENTS_MAX_AGE"   // Go duration, e.g. 336h; 0 disables the age bound
)

// DefaultRetention is the retention New uses, after environment overrides.
func DefaultRetention() Retention {
	r := Retention{MaxBytes: DefaultMaxBytes, Keep: DefaultKeep, MaxAge: DefaultMaxAge}
	if v, err := strconv.ParseInt(os.Getenv(EnvMaxBytes), 10, 64); err == nil && v >= 0 {
		r.MaxBytes = v
	}
	if v, err := strconv.Atoi(os.Getenv(EnvKeep)); err == nil && v >= 0 {
		r.Keep = v
	}
	if v, err := time.ParseDuration(os.Getenv(EnvMaxAge)); err == nil && v >= 0 {
		r.MaxAge = v
	}
	return r
}

// gen names rotated generation n (n >= 1) of path.
func gen(path string, n int) string { return fmt.Sprintf("%s.%d", path, n) }

// rotateIfNeeded rotates path when it has reached ret.MaxBytes. Several
// processes (the ccpool handler runs one per dispatch) append to the same
// path, so the check is repeated under a flock on path.lock: whoever lost the
// race sees the fresh small file and does nothing, instead of discarding the
// log the winner just started.
func rotateIfNeeded(path string, ret Retention, now time.Time) error {
	if ret.MaxBytes <= 0 {
		return nil
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() < ret.MaxBytes {
		return nil
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	if fi, err := os.Stat(path); err != nil || fi.Size() < ret.MaxBytes {
		return nil // another process rotated it first
	}
	keep := ret.Keep
	if keep < 0 {
		keep = 0
	}
	if keep == 0 {
		return os.Remove(path)
	}
	_ = os.Remove(gen(path, keep))
	for n := keep - 1; n >= 1; n-- {
		if err := os.Rename(gen(path, n), gen(path, n+1)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(path, gen(path, 1)); err != nil {
		return err
	}
	if ret.MaxAge > 0 {
		for n := 1; n <= keep; n++ {
			if fi, err := os.Stat(gen(path, n)); err == nil && now.Sub(fi.ModTime()) > ret.MaxAge {
				_ = os.Remove(gen(path, n))
			}
		}
	}
	return nil
}

// sameFile reports whether the open file f is still what path names.
func sameFile(f *os.File, path string) bool {
	a, err := f.Stat()
	if err != nil {
		return false
	}
	b, err := os.Stat(path)
	if err != nil {
		return false
	}
	return os.SameFile(a, b)
}
