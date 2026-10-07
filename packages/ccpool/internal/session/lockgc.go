package session

import (
	"context"
	"log/slog"
	"regexp"
	"time"

	"github.com/phillipgreenii/ccpool/internal/lock"
)

// Lock-file garbage collection (bead pg2-bjhoq).
//
// The per-external_id lock (Locker) is a flock on <runtime dir>/<id>.lock. The
// file is created on first use and was never deleted, so a pool that mints one
// session id per attempt (the pg-router handler does) accumulates one empty file
// per id forever, including for sessions whose row was long since closed and
// purged or pruned as a phantom. They are harmless to correctness (ids are
// unique, nothing scans the directory) but unbounded.
//
// gcLocks, the last step of Reap, removes a lock file only when ALL of these hold:
//
//  1. the name ends in ".lock" (lock.Flock.List lists nothing else);
//  2. its mtime is older than lockGCMinAge (24h): the file is only ever created,
//     never written, so mtime is its creation time;
//  3. the external id carries a parseable "<yyyymmddThhmmss[.fffffffff]>" stamp
//     as its final "-"-separated token (the handler's per-attempt id shape) and
//     that stamp is older than lockGCMinAge. An id without a stamp is NEVER
//     collected: the age evidence has to come from the name as well as the
//     filesystem, so a hand-named or foreign lock is left alone;
//  4. no session row exists for the id, read AGAIN under the exclusive flock,
//     immediately before the unlink. Every path that inserts a row (Create,
//     Ensure) does so while holding this same per-id lock, so no creator can
//     slip a row in between that read and the unlink;
//  5. a non-blocking exclusive flock succeeds: a lock held by anybody (a
//     running create/send/close of this id) is skipped.
//
// The create/open-flock-then-unlink race is handled in package lock (open-time
// inode re-verification in Lock, unlink-while-holding in RemoveIfFree); see its
// package comment. gcLocks is a no-op when the Locker is nil or cannot sweep
// (tests with fakes).
const lockGCMinAge = 24 * time.Hour

// lockSweeper is the optional capability of a Locker that gcLocks needs;
// lock.Flock implements it.
type lockSweeper interface {
	List() ([]lock.Entry, error)
	RemoveIfFree(name string, confirm func(modTime time.Time) (bool, error)) (bool, error)
}

// idStampRe matches the trailing timestamp token of a per-attempt external id:
// <stem>-<yyyymmddThhmmss> with an optional ".<digits>" fractional second
// (the handler appends nanoseconds).
var idStampRe = regexp.MustCompile(`-(\d{8}T\d{6})(\.\d+)?$`)

// idStamp extracts the embedded UTC timestamp of an external id.
func idStamp(externalID string) (time.Time, bool) {
	m := idStampRe.FindStringSubmatch(externalID)
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("20060102T150405", m[1], time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// lockGCEligibleByAge is the lock-free pre-filter and the under-lock re-check of
// conditions 2 and 3: both the file mtime and the id-embedded stamp must be
// strictly older than lockGCMinAge. A future-dated stamp or mtime is not old.
func lockGCEligibleByAge(externalID string, modTime, now time.Time) bool {
	stamp, ok := idStamp(externalID)
	if !ok {
		return false
	}
	return now.Sub(modTime) > lockGCMinAge && now.Sub(stamp) > lockGCMinAge
}

// gcLocks removes orphaned lock files; it returns how many it removed. Best
// effort: every failure is logged and leaves the file for the next sweep.
func (s *Service) gcLocks(ctx context.Context) int {
	sw, ok := s.d.Lock.(lockSweeper)
	if !ok { // nil Locker, or one without sweep support (test fakes)
		return 0
	}
	entries, err := sw.List()
	if err != nil {
		slog.Warn("ccpool: lock gc: list failed", "err", err)
		return 0
	}
	now := s.now()
	removed := 0
	for _, e := range entries {
		if ctx.Err() != nil {
			break
		}
		if !lockGCEligibleByAge(e.Name, e.ModTime, now) {
			continue
		}
		ok, err := sw.RemoveIfFree(e.Name, func(modTime time.Time) (bool, error) {
			// Re-check under the exclusive flock: the age (a touched file is
			// not old) and, above all, the row.
			if !lockGCEligibleByAge(e.Name, modTime, now) {
				return false, nil
			}
			_, hasRow, err := s.d.Store.GetByExternalID(ctx, e.Name)
			if err != nil {
				return false, err // cannot prove there is no row: keep the file
			}
			return !hasRow, nil
		})
		if err != nil {
			slog.Warn("ccpool: lock gc: could not remove lock file", append([]any{"err", err}, sessionLogArgs(e.Name)...)...)
			continue
		}
		if ok {
			removed++
		}
	}
	if removed > 0 {
		slog.Info("ccpool: lock gc removed orphaned lock files", "removed", removed)
	}
	return removed
}
