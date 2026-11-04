package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/executor"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
	"github.com/phillipgreenii/pg-router-handlers/internal/worktree"
)

// Dispatch admission control (bead pg2-fx5x0l.5, INV-CCH-41, ADR 0117): the
// pieces a dispatch runs INSTEAD of runSweeps when the launch config's
// inlineSweeps is false. The janitor owns the rest of the hygiene.
//
//   - runOrphanUnit: this role's orphans, as ONE unit before the plan and the
//     capacity check, over the ONE pool listing the plan then reuses.
//   - poolReconciler: the executor's Reconciler seam, built here because the
//     passes live in this package.

// closedReconcileStampPrefix names the per-pool negative-cache stamp in the lock
// directory. The suffix is ".stamp", never ".lock", so the lock-file collection
// (sessionlock.SweepStale) leaves it alone.
const closedReconcileStampPrefix = "closed-reconcile--"

// runOrphanUnit handles every orphan of role (INV-CCH-18), unchanged in
// semantics, over one listing of the role's pool. When it found no orphan the
// listing is still the pool as it is, so it is returned in the Deps for the
// executor's prior-session plan to reuse (one list call, not two); otherwise the
// plan reads the pool afresh, which is the post-reclaim pool it saw before.
func runOrphanUnit(ctx context.Context, role roles.Role, cfg config.Config, deps executor.Deps) executor.Deps {
	if role.CCPool == nil || deps.CC == nil {
		return deps
	}
	t0 := time.Now()
	if deps.LockDir == "" {
		slog.Warn("orphan reconcile skipped: no lock directory", "role", role.Name)
		return deps
	}
	sessions, err := deps.CC.List(ctx)
	if err != nil {
		slog.Warn("orphan reconcile: list failed", "role", role.Name, "err", err)
		return deps
	}
	reclaimed, stopped, candidates := reconcileOrphanSessionsFrom(ctx, role, deps, orphanEnv{
		open:    worktree.GitOpener,
		quiet:   newTranscriptQuietCheck(cfg.WorktreeQuietWindow, nil, nil),
		lockDir: deps.LockDir,
	}, sessions)
	recordSweepPass("dispatch", role.Name, "orphan_reconcile", reclaimed+stopped, time.Since(t0), deps)
	if reclaimed+stopped > 0 {
		slog.Info("dispatch: reconciled orphaned sessions", "reclaimed", reclaimed, "hard_stopped", stopped)
	}
	if candidates == 0 {
		deps.PoolListing, deps.PoolListed = sessions, true
	}
	return deps
}

// poolReconciler implements executor.Reconciler over this package's passes.
type poolReconciler struct {
	role roles.Role
	cfg  config.Config
	deps executor.Deps
	// cwds overrides the live-process probe on the dispatch path (tests only);
	// nil means the production probe, processCWDs. It never means "no probe": a
	// failed or unavailable probe keeps the directory.
	cwds cwdProbe
}

// probe returns the live-process probe for the dispatch path.
func (p poolReconciler) probe() cwdProbe {
	if p.cwds != nil {
		return p.cwds
	}
	return processCWDs
}

func newPoolReconciler(role roles.Role, cfg config.Config, deps executor.Deps) executor.Reconciler {
	if role.CCPool == nil {
		return nil
	}
	return poolReconciler{role: role, cfg: cfg, deps: deps}
}

// stampPath is the pool's negative-cache stamp: one file per pool, named by a
// hash of the pool key so any pool path is a safe file name.
func (p poolReconciler) stampPath() string {
	if p.deps.LockDir == "" {
		return ""
	}
	pool := p.role.CCPool.PoolDir
	if pool == "" {
		pool = os.Getenv("CCPOOL_POOL")
	}
	sum := sha256.Sum256([]byte(pool))
	return filepath.Join(p.deps.LockDir, closedReconcileStampPrefix+hex.EncodeToString(sum[:8])+".stamp")
}

func (p poolReconciler) now() time.Time { return orphanNow(p.deps) }

// recentlyFoundNothing reports whether a reconcile of this pool found nothing
// within the configured interval.
func (p poolReconciler) recentlyFoundNothing() bool {
	path, ttl := p.stampPath(), p.cfg.DeclineReconcileMinInterval
	if path == "" || ttl <= 0 {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, string(raw))
	if err != nil {
		return false
	}
	age := p.now().Sub(at)
	return age >= 0 && age < ttl
}

// stampNothingFound records that a reconcile of this pool just found nothing.
// Best effort.
func (p poolReconciler) stampNothingFound() {
	path := p.stampPath()
	if path == "" || p.cfg.DeclineReconcileMinInterval <= 0 {
		return
	}
	if err := os.WriteFile(path, []byte(p.now().UTC().Format(time.RFC3339Nano)), 0o644); err != nil {
		slog.Warn("capacity-pressure reconcile: could not write the negative-cache stamp", "path", path, "err", err)
	}
}

// ReconcileClosedBeads implements executor.Reconciler: the closed-bead pass over
// the role's own pool, unless one found nothing within the interval.
func (p poolReconciler) ReconcileClosedBeads(ctx context.Context) (executor.ReconcileOutcome, error) {
	if p.recentlyFoundNothing() {
		return executor.ReconcileOutcome{Skipped: true}, nil
	}
	t0 := time.Now()
	useRemoveTimeout(p.cfg)
	closed := reconcileClosedBeadSessions(ctx, p.deps.CC, worktree.GitOpener, p.deps.BD, p.cfg.SessionPrefix, p.cfg.RepoRoot, p.cfg.WorktreeDir, newTranscriptQuietCheck(p.cfg.WorktreeQuietWindow, nil, nil))
	recordSweepPass("dispatch", p.role.Name, "closed_bead_reconcile", closed, time.Since(t0), p.deps)
	if closed > 0 {
		slog.Info("dispatch: reconciled sessions with closed beads under capacity pressure", "closed", closed)
		return executor.ReconcileOutcome{Closed: closed}, nil
	}
	p.stampNothingFound()
	return executor.ReconcileOutcome{}, nil
}

// ReclaimLeakedWorktree implements executor.Reconciler: the worktree-keyed
// sweep's cheap pre-filter and guarded reclaim (reclaimLeakedWorktree re-checks
// everything under the exclusive per-bead lock) applied to ONE bead's worktree.
func (p poolReconciler) ReclaimLeakedWorktree(ctx context.Context, beadID string) error {
	dir := p.cfg.WorktreeDir
	if dir == "" || p.role.CCPool == nil || !executor.UsesWorktreeIsolation(p.role.CCPool.Isolation) {
		return nil
	}
	if beadID == "" || filepath.Base(beadID) != beadID || beadID == "." || beadID == ".." {
		return nil
	}
	if p.deps.LockDir == "" {
		return errors.New("no lock directory")
	}
	c, ok := sweepCandidate(filepath.Join(dir, beadID), beadID, p.now(), worktreeSweepGrace(p.deps))
	if !ok {
		// Not a linked worktree: heal the unregistered leftover of a killed
		// removal, which would otherwise fail this dispatch's `git worktree add`
		// with "already exists" (leftoverdir.go).
		if l, isLeftover := leftoverCandidate(filepath.Join(dir, beadID), beadID, p.cfg.RepoRoot, p.now(), worktreeSweepGrace(p.deps)); isLeftover {
			reclaimLeftoverDir(ctx, p.role, p.deps, orphanEnv{open: worktree.GitOpener, cwds: p.probe(), lockDir: p.deps.LockDir}, l)
		}
		return nil
	}
	sessions, err := p.deps.CC.List(ctx)
	if err != nil {
		return err
	}
	if worktreeHeldByRow(sessions, c) {
		return nil
	}
	env := orphanEnv{open: worktree.GitOpener, cwds: p.probe(), lockDir: p.deps.LockDir}
	if env.cwds != nil {
		cwds, err := env.cwds(ctx)
		if err != nil {
			return err
		}
		if worktreeHeldByProcess(cwds, c) {
			return nil
		}
	}
	reclaimLeakedWorktree(ctx, p.role, p.deps, env, c)
	return nil
}
