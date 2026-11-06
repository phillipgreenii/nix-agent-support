package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		d.cleanupRun(ctx, r)
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

// cleanupRun closes the run's session and deletes its staging directory.
func (d *Deps) cleanupRun(ctx context.Context, r ledger.Run) {
	if r.SessionID != "" && d.CloseSession != nil {
		_ = d.CloseSession(ctx, r.SessionID)
	}
	if r.StagingPath != "" {
		_ = d.RemoveStaging(r.StagingPath)
	}
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

// removeStaging deletes a staging directory, refusing paths too shallow to be
// a staging directory.
func removeStaging(path string) error {
	p := filepath.Clean(path)
	if !filepath.IsAbs(p) || len(strings.Split(strings.Trim(p, "/"), "/")) < 2 {
		return fmt.Errorf("refusing to remove staging path %q", path)
	}
	return os.RemoveAll(p)
}
