package hygiene

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/pg-router/internal/handlers/config"
	"github.com/phillipgreenii/pg-router/internal/handlers/executor"
	"github.com/phillipgreenii/pg-router/internal/handlers/roles"
	"github.com/phillipgreenii/pg-router/internal/handlers/worktree"
)

// Dispatch admission control (bead pg2-fx5x0l.5, INV-CCH-41, ADR 0117): the
// pieces a dispatch runs before its capacity check. The janitor owns the rest
// of the hygiene.
//
//   - runOrphanUnit: this role's orphans, as ONE unit before the plan and the
//     capacity check, over the ONE pool listing the plan then reuses.
//   - poolReconciler: the executor's Admission seam, built here because the
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
func (s Sweeper) RunOrphanUnit(ctx context.Context, role roles.Role, cfg config.Config, deps executor.Deps) executor.Deps {
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
	reclaimed, stopped, candidates := reconcileOrphanSessionsFrom(ctx, role, deps, s.env(orphanEnv{
		open:    worktree.GitOpener,
		quiet:   newTranscriptQuietCheck(cfg.WorktreeQuietWindow, nil, nil),
		lockDir: deps.LockDir,
	}), sessions)
	recordSweepPass("dispatch", role.Name, "orphan_reconcile", reclaimed+stopped, time.Since(t0), deps)
	if reclaimed+stopped > 0 {
		slog.Info("dispatch: reconciled orphaned sessions", "reclaimed", reclaimed, "hard_stopped", stopped)
	}
	if candidates == 0 {
		deps.PoolListing, deps.PoolListed = sessions, true
	}
	return deps
}

// poolReconciler implements the executor.Admission funcs over this package's passes.
type poolReconciler struct {
	role roles.Role
	cfg  config.Config
	deps executor.Deps
	// sweeper supplies the ports (gauge, sweep bounds, live-process probe,
	// teardown and bulk-delete ports).
	sweeper Sweeper
}

// NewAdmission builds the executor's Admission port over this package's passes
// and the Sweeper's ports, for role's pool; nil when the role has no ccpool block.
func (s Sweeper) NewAdmission(role roles.Role, cfg config.Config, deps executor.Deps) *executor.Admission {
	if role.CCPool == nil {
		return nil
	}
	p := poolReconciler{role: role, cfg: cfg, deps: deps, sweeper: s}
	return &executor.Admission{ReconcileClosedBeads: p.ReconcileClosedBeads}
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

// ReconcileClosedBeads implements executor.Admission.ReconcileClosedBeads: the closed-bead pass over
// the role's own pool, unless one found nothing within the interval.
func (p poolReconciler) ReconcileClosedBeads(ctx context.Context) (executor.ReconcileOutcome, error) {
	if p.recentlyFoundNothing() {
		return executor.ReconcileOutcome{Skipped: true}, nil
	}
	t0 := time.Now()
	closed := p.sweeper.ports.withRemoveTimeout(p.cfg).reconcileClosedBeadSessions(ctx, p.deps.CC, worktree.GitOpener, p.deps.BD, p.cfg.SessionPrefix, p.cfg.RepoRoot, p.cfg.WorktreeDir, newTranscriptQuietCheck(p.cfg.WorktreeQuietWindow, nil, nil))
	recordSweepPass("dispatch", p.role.Name, "closed_bead_reconcile", closed, time.Since(t0), p.deps)
	if closed > 0 {
		slog.Info("dispatch: reconciled sessions with closed beads under capacity pressure", "closed", closed)
		return executor.ReconcileOutcome{Closed: closed}, nil
	}
	p.stampNothingFound()
	return executor.ReconcileOutcome{}, nil
}
