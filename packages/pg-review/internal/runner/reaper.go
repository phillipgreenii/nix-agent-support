package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
)

// Reaped describes one run the reaper ended.
type Reaped struct {
	RunID  int64
	Reason string
}

// Reap fails stale runs. A Running run whose recorded pid is gone, or whose
// process start time differs from the stored one, is set to Failed; its
// session is closed through the registered hook and its staging directory is
// deleted. An AwaitingAnalyst run is untouched until its interactive TTL
// (interactive_ttl_minutes) expires, then it is Failed and its staging is
// deleted. Locks whose run has ended are released. maintain calls this too.
func Reap(ctx context.Context, d *Deps) ([]Reaped, error) {
	d.defaults()
	runs, err := d.Ledger.RunsInStates(ctx, ledger.RunRunning, ledger.RunAwaitingAnalyst)
	if err != nil {
		return nil, err
	}
	ttl := time.Duration(d.Cfg.Limits.InteractiveTTLMinutes) * time.Minute
	var out []Reaped
	for _, r := range runs {
		var reason string
		switch r.State {
		case ledger.RunRunning:
			if alive(d.Proc, r.PID, r.StartedAt) {
				continue
			}
			reason = "reaped: the run's process is gone"
		case ledger.RunAwaitingAnalyst:
			if ttl <= 0 || d.Now().Before(r.CreatedAt.Add(ttl)) {
				continue
			}
			reason = "interactive TTL expired"
		}
		if err := d.cleanupRun(ctx, r); err != nil {
			reason += "; " + err.Error()
		}
		if err := endRun(ctx, d, r.ID, ledger.RunFailed, reason); err != nil {
			return out, err
		}
		out = append(out, Reaped{RunID: r.ID, Reason: reason})
	}
	locks, err := d.Ledger.Locks(ctx)
	if err != nil {
		return out, err
	}
	for _, k := range locks {
		run, err := d.Ledger.GetRun(ctx, k.RunID)
		if err != nil && !errors.Is(err, ledger.ErrNotFound) {
			return out, err
		}
		if err == nil && (run.State == ledger.RunRunning || run.State == ledger.RunAwaitingAnalyst) {
			continue
		}
		if err := d.Ledger.WithTx(ctx, func(t *ledgerTx) error { return t.ReleaseLock(ctx, k.Lens, k.Scope) }); err != nil {
			return out, err
		}
	}
	return out, nil
}

// cleanupRun closes the run's session and deletes its staging directory. A
// staging path that RemoveStaging refuses is left in place and returned as an
// error for the caller to record.
func (d *Deps) cleanupRun(ctx context.Context, r ledger.Run) error {
	if r.SessionID != "" && d.CloseSession != nil {
		_ = d.CloseSession(ctx, r.SessionID)
	}
	if r.StagingPath != "" {
		if err := d.RemoveStaging(r.StagingPath); err != nil {
			return fmt.Errorf("staging not removed: %w", err)
		}
	}
	return nil
}

// endRun moves a run to a terminal state with a reason, and releases its lock
// when it holds it.
func endRun(ctx context.Context, d *Deps, id int64, state, reason string) error {
	return d.Ledger.WithTx(ctx, func(t *ledgerTx) error {
		r, err := t.GetRun(ctx, id)
		if err != nil {
			return err
		}
		if r.State != ledger.RunRunning && r.State != ledger.RunAwaitingAnalyst {
			return nil
		}
		r.State = state
		r.SkipReason = reason
		r.EndedAt = d.Now().UTC()
		if !r.CreatedAt.IsZero() {
			r.ElapsedMinutes = r.EndedAt.Sub(r.CreatedAt).Minutes()
		}
		if err := t.UpdateRun(ctx, r); err != nil {
			return err
		}
		return releaseOwnLock(ctx, t, r)
	})
}

func releaseOwnLock(ctx context.Context, t *ledgerTx, r ledger.Run) error {
	owner, err := t.LockRunID(ctx, r.Lens, r.Scope)
	if err != nil || owner != r.ID {
		return nil
	}
	return t.ReleaseLock(ctx, r.Lens, r.Scope)
}

// evidenceRoot is the only tree the reaper and the TTL sweep may delete from.
func evidenceRoot(cacheHome string) string {
	return filepath.Join(cacheHome, "pg-review", "evidence")
}

// isStagingDirName reports whether p is a direct child of the literal staging
// root named like the directories makeStaging creates.
func isStagingDirName(p string) bool {
	if filepath.Dir(p) != stagingRoot {
		return false
	}
	ok, _ := filepath.Match("pg-review-*", filepath.Base(p))
	return ok
}

// defaultRemoveStaging is the ONLY way to obtain a staging remover: every
// default or fallback RemoveStaging (the pipeline, the reaper, the TTL sweep,
// FinishInteractive) MUST come from here, so no caller can wire the bare
// removeStaging. The remover it returns is bound to the evidence root under
// cacheHome and permits only that tree and direct /tmp/pg-review-* directories.
// Staging lives at the literal /tmp/pg-review-* (not $TMPDIR) because the
// analyst sandbox cannot read $TMPDIR; evidence lives under the cache home.
func defaultRemoveStaging(cacheHome string) func(string) error {
	root := evidenceRoot(cacheHome)
	return func(path string) error { return removeStaging(root, path) }
}

// removeStaging deletes a staging directory. It permits exactly two kinds of
// path: strictly inside root (the evidence root) once symlinks are resolved,
// or a direct child of the literal staging root (/tmp) named pg-review-* that
// is a real directory, not a symlink. Anything else is refused. A path that
// does not exist is already gone and is not an error.
func removeStaging(root, path string) error {
	p := filepath.Clean(path)
	if !filepath.IsAbs(p) {
		return fmt.Errorf("refusing to remove staging path %q: not absolute", path)
	}
	fi, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err == nil && isStagingDirName(p) {
		if !fi.IsDir() {
			return fmt.Errorf("refusing to remove staging path %q: not a directory (symlinks are refused)", path)
		}
		return os.RemoveAll(p)
	}
	rr, err := filepath.EvalSymlinks(root)
	if errors.Is(err, os.ErrNotExist) {
		// No evidence root yet, so nothing that exists can be inside it.
		return fmt.Errorf("refusing to remove staging path %q: outside the evidence root %q and not a direct %s/pg-review-* directory", path, root, stagingRoot)
	}
	if err != nil {
		return fmt.Errorf("refusing to remove staging path %q: evidence root unusable: %w", path, err)
	}
	rp, err := filepath.EvalSymlinks(p)
	if err != nil {
		return fmt.Errorf("refusing to remove staging path %q: %w", path, err)
	}
	if !within(rr, rp) {
		return fmt.Errorf("refusing to remove staging path %q: outside the evidence root %q and not a direct %s/pg-review-* directory", path, root, stagingRoot)
	}
	return os.RemoveAll(rp)
}
