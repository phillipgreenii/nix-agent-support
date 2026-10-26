package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/executor"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
	"github.com/phillipgreenii/pg-router-handlers/internal/worktree"
)

// runSweeps runs the housekeeping passes a ccpool role's dispatch has always run
// before its executor, and that its postStartup request now runs too (the
// post-startup reconcile): the closed-bead session reconcile, the default-pool
// pass, the role-scoped orphan reconcile, the worktree-keyed sweep and the lock
// file GC. who prefixes the log lines ("dispatch" or "postStartup").
//
// Every pass is best effort: logged, never an error, so neither a dispatch nor a
// postStartup fails because a sweep could not run. They are also idempotent and
// safe to run any number of times: a session or worktree one pass has already
// reclaimed is simply not there for the next, and the orphan reconcile and the
// worktree sweep take the per-session flocks the dispatch itself takes
// (INV-CCH-18/19), so a sweep running while a dispatch of the same role is in
// flight cannot reclaim that dispatch's session. That is the assumption the
// postStartup path relies on: core calls postStartup once per enabled role
// (pg2-12gr4.10.1's routing record), which runs the role-scoped passes once per
// role, and the passes that are not role-scoped (the closed-bead reconcile on
// the role's own pool, the default-pool pass) are cheap to repeat.
//
// A role without a ccpool block has no sessions to sweep and touches neither
// ccpool nor bd.
func runSweeps(ctx context.Context, who string, role roles.Role, cfg config.Config, deps executor.Deps) {
	if role.CCPool == nil {
		return
	}
	// Opportunistic reconciliation (pg2-hrppg): reuses deps.CC/deps.BD (the role's
	// own pool runner) rather than constructing a second pair.
	if closed := reconcileClosedBeadSessions(ctx, deps.CC, worktree.GitOpener, deps.BD, cfg.SessionPrefix, cfg.RepoRoot, cfg.WorktreeDir, newTranscriptQuietCheck(cfg.WorktreeQuietWindow, nil, nil)); closed > 0 {
		slog.Info(who+": reconciled sessions with closed beads", "closed", closed)
	}
	// Default-pool pass (pg2-wqi3e): a role with its own pool (or a process
	// launched against a non-default CCPOOL_POOL) never sweeps ccpool's shared
	// DEFAULT pool, where a not-live needs_input row can sit forever (ccpool's
	// reaper spares needs_input; the orphan reconcile needs a lease). GUARDED:
	// only a long-idle row whose worktree is absent or clean and pushed is
	// closed (stalesession.go). Skipped when the role's own pool already IS the
	// default pool, which the unguarded pass above has just swept.
	if role.CCPool.PoolDir != "" || os.Getenv("CCPOOL_POOL") != "" {
		if closed := reconcileDefaultPool(ctx, cfg); closed > 0 {
			slog.Info(who+": reconciled stale default-pool sessions with closed beads", "closed", closed)
		}
	}
	// Orphan reconcile (pg2-g2u9m, INV-CCH-18): reclaim or budget-stop the
	// sessions of THIS role whose handler died (expired supervision lease), scoped
	// to the role's own pool runner. In a dispatch it runs before the executor's
	// capacity check, so a reclaimed orphan frees its slot for that very dispatch.
	if reclaimed, stopped := reconcileOrphanSessions(ctx, role, deps, orphanEnv{
		open:    worktree.GitOpener,
		quiet:   newTranscriptQuietCheck(cfg.WorktreeQuietWindow, nil, nil),
		lockDir: deps.LockDir,
	}); reclaimed+stopped > 0 {
		slog.Info(who+": reconciled orphaned sessions", "reclaimed", reclaimed, "hard_stopped", stopped)
	}
	// Worktree-keyed sweep (pg2-ganjb, INV-CCH-19): reclaim per-bead worktrees no
	// session row can lead to. After the orphan reconcile, so a worktree that
	// reconcile just released is not double-handled.
	if swept := sweepLeakedWorktrees(ctx, role, deps, orphanEnv{
		open:    worktree.GitOpener,
		cwds:    processCWDs,
		lockDir: deps.LockDir,
	}); swept > 0 {
		slog.Info(who+": reclaimed leaked worktrees", "reclaimed", swept)
	}
	// Lock-file GC (pg2-bjhoq): bound the handler lock directory. After the
	// worktree sweep, so the locks it just used count as freshly used.
	if n := sweepStaleLocks(deps); n > 0 {
		slog.Info(who+": removed stale lock files", "removed", n)
	}
}
