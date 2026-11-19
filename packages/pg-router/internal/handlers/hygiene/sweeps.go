package hygiene

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router/internal/handlers/executor"
	"github.com/phillipgreenii/pg-router/internal/handlers/sessionlock"
)

// recordSweepPass logs one housekeeping pass's wall time and appends a sweep_pass
// row to events.jsonl (role, pass, elapsed_ms, found). who names the caller that
// ran the pass: "dispatch" for the passes a dispatch runs on demand (its role's
// orphans, the capacity-pressure closed-bead reconcile) and "janitor" for the
// scheduled janitor.
// Best effort and behavior-neutral: a nil or failing event log is ignored.
func recordSweepPass(who, role, pass string, found int, elapsed time.Duration, deps executor.Deps) {
	slog.Info("sweeps done", "who", who, "role", role, "pass", pass, "elapsed", elapsed, "found", found)
	if deps.Log != nil {
		_ = deps.Log.Emit("info", "sweep_pass", "sweep pass timed", map[string]any{
			"who":        who,
			"role":       role,
			"pass":       pass,
			"elapsed_ms": elapsed.Milliseconds(),
			"found":      found,
		})
	}
}

// sweepStaleLocks garbage-collects the handler's own lock directory: one empty
// <external_id>.lock per session and one worktree--<bead>.lock per bead would
// otherwise accumulate forever. It removes only files unused for
// sessionlock.DefaultStaleAge whose exclusive flock it can take without
// blocking, unlinking under that flock, so it can never remove a lock a live
// dispatch or reclaimer holds (shared or exclusive); the open-then-unlink race
// against a concurrent opener is closed by the opener's inode re-verification
// (see sessionlock.SweepStale). It also removes the records of the retired
// worktree sweep (obsoleteSweepRecord). Best effort; returns the count removed.
func sweepStaleLocks(deps executor.Deps) int {
	if deps.LockDir == "" {
		return 0
	}
	return sessionlock.SweepStale(deps.LockDir, sessionlock.DefaultStaleAge, orphanNow(deps)) + removeObsoleteSweepRecords(deps.LockDir)
}

// obsoleteSweepRecord reports whether name is a per-bead record the retired
// worktree-keyed sweep kept beside the locks (keep cache, first-seen and
// last-examined records, and their temp files).
func obsoleteSweepRecord(name string) bool {
	for _, p := range []string{"worktree-keep--", "worktree-seen--", "worktree-exam--"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// removeObsoleteSweepRecords deletes those records from lockDir and returns how
// many it removed. Best effort.
func removeObsoleteSweepRecords(lockDir string) int {
	des, err := os.ReadDir(lockDir)
	if err != nil {
		return 0
	}
	n := 0
	for _, de := range des {
		if !de.IsDir() && obsoleteSweepRecord(de.Name()) && os.Remove(filepath.Join(lockDir, de.Name())) == nil {
			n++
		}
	}
	return n
}
