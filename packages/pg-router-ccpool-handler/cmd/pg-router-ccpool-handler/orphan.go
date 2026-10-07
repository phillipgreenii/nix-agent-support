package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/budget"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/executor"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/sessionlock"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/usage"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/watchdog"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/worktree"
)

// Orphan reconcile (bead pg2-g2u9m, INV-CCH-18).
//
// A handler process alone supervises the session it launched; when it dies
// (daemon restart, crash, giving up at MaxWait) nothing supervises the session
// any more. Every live handler stamps pgrouter.lease_until on its session each
// poll (internal/executor/lease.go), so a lease in the past means "nobody is
// supervising this session". reconcileOrphanSessions runs at dispatch time, in
// the dispatching role's OWN pool, after reconcileClosedBeadSessions and before
// the capacity check, and applies the operator's ruling (Phillip, 2026-10-05,
// bead pg2-g2u9m):
//
//   - idle/errored and transcript-quiet: apply the role's completion rule to
//     the bead (unclaim it iff it is still claimed by this role's actor), then
//     close the session (non-purge, reason handler) and remove its worktree
//     under the dispatch-time cleanup's guards.
//   - starting/ready/working: enforce the TIME budget from the recorded launch
//     time; over budget, run the same hard stop as watchdog/terminal.go, then
//     remove the worktree under the same guards. Under budget (or a role with
//     no time budget): leave it alone.
//   - needs_input: unchanged (ADR 0037 -- a person may still attach).
//
// A session with no lease (launched by an older build) is never an orphan;
// ccpool's idle_ttl reaper bounds it. Only the time budget is enforced for an
// orphan (tokens/cost could be read statelessly from the transcript; a possible
// follow-up). An orphan is reclaimed only when its OWN role next dispatches.
//
// Mutual exclusion with an absorbing dispatch is a non-blocking flock per
// session (internal/sessionlock): the reconcile skips a session whose lock is
// held, and, holding it, re-lists and re-checks the lease before acting.

// orphanEnv carries the ambient dependencies the orphan reconcile needs beyond
// executor.Deps; zero values select production behavior.
type orphanEnv struct {
	// open anchors a gitclient for worktree removal / branch deletion.
	open worktree.Opener
	// quiet is the non-blocking transcript-quiet guard (newTranscriptQuietCheck);
	// nil disables it.
	quiet quietCheck
	// cwds lists the working directory of every live process (processCWDs); nil
	// disables the worktree sweep's live-process guard (bead pg2-e5yw3).
	cwds cwdProbe
	// lockDir is <handler state dir>/locks (sessionlock.Dir). Required: an empty
	// lockDir disables the whole reconcile, because acting without mutual
	// exclusion could race an absorbing dispatch.
	lockDir string
}

// orphanEventReclaimed / orphanEventHardStop are the eventlog kinds recorded
// for the two outcomes.
const (
	orphanEventReclaimed = "orphan_reclaimed"
	orphanEventHardStop  = "orphan_hard_stop"
)

// reconcileOrphanSessions reclaims or budget-stops the orphaned sessions of
// role in deps.CC's pool. It returns how many were reclaimed (idle path) and
// hard-stopped (active path). Best effort: every failure is logged and leaves
// the session for the next dispatch; it never fails the dispatch.
func reconcileOrphanSessions(ctx context.Context, role roles.Role, deps executor.Deps, env orphanEnv) (reclaimed, hardStopped int) {
	if role.CCPool == nil {
		return 0, 0
	}
	if env.lockDir == "" {
		slog.Warn("orphan reconcile skipped: no lock directory", "role", role.Name)
		return 0, 0
	}
	sessions, err := deps.CC.List(ctx)
	if err != nil {
		slog.Warn("orphan reconcile: list failed", "role", role.Name, "err", err)
		return 0, 0
	}
	now := orphanNow(deps)
	for _, s := range sessions {
		if !isOrphanOf(s, role, deps.Cfg.SessionPrefix, now) {
			continue
		}
		switch reconcileOneOrphan(ctx, role, deps, env, s) {
		case orphanReclaimed:
			reclaimed++
		case orphanHardStopped:
			hardStopped++
		}
	}
	return reclaimed, hardStopped
}

type orphanOutcome int

const (
	orphanLeftAlone orphanOutcome = iota
	orphanReclaimed
	orphanHardStopped
)

func orphanNow(deps executor.Deps) time.Time {
	if deps.Now != nil {
		return deps.Now()
	}
	return time.Now()
}

// isOrphanOf reports whether s is an orphan of role: a still-open session in
// this process's prefix, carrying this role's pgrouter meta (and pg-router's
// pool tag), whose supervision lease is in the past. A session with NO lease is
// never an orphan; a closed row never is either (a handler-closed settled row
// legitimately outlives its lease).
func isOrphanOf(s ccpool.Session, role roles.Role, prefix string, now time.Time) bool {
	if !strings.HasPrefix(s.ExternalID, prefix) || s.CloseReason != "" {
		return false
	}
	// EXCLUSIVITY (bead pg2-kqegi, INV-CCH-20): a row carrying purge_pending is
	// owned by the purge_pending retry (reconcile.go), never by this pass. Handling
	// it here too would double-handle one row in a single pass, and the orphan
	// reclaim's own SetMeta/Close(false) would race the retry's. Keep this check
	// first; crashOrphaned (internal/executor) treats the same rows as absent.
	if s.PurgePending() {
		return false
	}
	if s.Meta[ccpool.MetaKeyPool] != ccpool.PoolName || s.Meta[ccpool.MetaKeyRole] != role.Name {
		return false
	}
	lease, ok := s.LeaseUntil()
	return ok && lease.Before(now)
}

// reconcileOneOrphan acts on one candidate under its per-session flock,
// re-reading the row first so a lease refreshed since the caller's list wins.
func reconcileOneOrphan(ctx context.Context, role roles.Role, deps executor.Deps, env orphanEnv, candidate ccpool.Session) orphanOutcome {
	lock, err := sessionlock.TryLock(env.lockDir, candidate.ExternalID)
	if err != nil {
		if errors.Is(err, sessionlock.ErrHeld) {
			slog.Info("orphan reconcile: session locked by another holder; skipping", "session", candidate.ExternalID)
		} else {
			slog.Warn("orphan reconcile: could not lock session; skipping", "session", candidate.ExternalID, "err", err)
		}
		return orphanLeftAlone
	}
	defer lock.Unlock()

	fresh, err := deps.CC.List(ctx)
	if err != nil {
		slog.Warn("orphan reconcile: re-list under lock failed; no action", "session", candidate.ExternalID, "err", err)
		return orphanLeftAlone
	}
	var s ccpool.Session
	found := false
	for _, f := range fresh {
		if f.ExternalID == candidate.ExternalID {
			s, found = f, true
			break
		}
	}
	if !found || !isOrphanOf(s, role, deps.Cfg.SessionPrefix, orphanNow(deps)) {
		slog.Info("orphan reconcile: session no longer an orphan (lease refreshed or closed); no action", "session", candidate.ExternalID)
		return orphanLeftAlone
	}

	switch s.State {
	case ccpool.StateIdle, ccpool.StateErrored:
		return reclaimIdleOrphan(ctx, role, deps, env, s, fresh)
	case ccpool.StateStarting, ccpool.StateReady, ccpool.StateWorking:
		return stopActiveOrphan(ctx, role, deps, env, s)
	}
	return orphanLeftAlone // needs_input (ADR 0037) and anything unrecognized
}

// orphanFields is the shared eventlog payload.
func orphanFields(role roles.Role, s ccpool.Session) map[string]any {
	pool := role.CCPool.PoolDir
	if pool == "" {
		pool = "default"
	}
	return map[string]any{
		"session":      s.ExternalID,
		"bead":         s.Meta[ccpool.MetaKeyBead],
		"role":         role.Name,
		"pool":         pool,
		"state":        string(s.State),
		"lease_until":  s.Meta[ccpool.MetaKeyLeaseUntil],
		"launched_at":  s.Meta[ccpool.MetaKeyLaunchedAt],
		"worktree_cwd": s.CWD,
	}
}

func emitOrphan(deps executor.Deps, level, kind, msg string, fields map[string]any) {
	if deps.Log != nil {
		_ = deps.Log.Emit(level, kind, msg, fields)
	}
}

// claimsBead reports whether the role's completion mode claims the bead it
// dispatches (close-only / close-or-handback). The triage modes work a bead
// they never claim, so a reclaim writes nothing to it.
func claimsBead(c roles.Completion) bool {
	return c == roles.CloseOnly || c == roles.CloseOrHandback
}

// reclaimIdleOrphan handles an idle/errored orphan: bead rule, close, worktree.
func reclaimIdleOrphan(ctx context.Context, role roles.Role, deps executor.Deps, env orphanEnv, s ccpool.Session, sessions []ccpool.Session) orphanOutcome {
	if env.quiet != nil && !env.quiet(s) {
		slog.Info("orphan reconcile: orphan transcript still active; deferring", "session", s.ExternalID)
		return orphanLeftAlone
	}
	cc := role.CCPool
	beadID := s.Meta[ccpool.MetaKeyBead]
	unclaimed := false
	if claimsBead(cc.Completion) && beadID != "" {
		iss, err := beads.ShowObj(ctx, deps.BD, beadID)
		if err != nil {
			// Cannot tell who holds the bead: closing the session now would strand a
			// claim nobody can see. Leave it for the next dispatch.
			slog.Warn("orphan reconcile: bead lookup failed; leaving the orphan for the next dispatch",
				"session", s.ExternalID, "bead", beadID, "err", err)
			return orphanLeftAlone
		}
		if iss.Status == "in_progress" && cc.Actor != "" && iss.Assignee == cc.Actor {
			_ = beads.Comment(ctx, deps.BD, beadID, fmt.Sprintf(
				"orphaned session %s reclaimed after handler loss (lease expired %s)", s.ExternalID, s.Meta[ccpool.MetaKeyLeaseUntil],
			))
			if err := beads.Unclaim(ctx, deps.BD, beadID); err != nil {
				slog.Warn("orphan reconcile: unclaim failed; leaving the orphan for the next dispatch",
					"session", s.ExternalID, "bead", beadID, "err", err)
				return orphanLeftAlone
			}
			unclaimed = true
		}
	}

	// Mark BEFORE closing so a redelivered dispatch never mistakes the abandoned
	// row for a settled duplicate to absorb (executor.crashOrphaned). Best effort.
	if err := deps.CC.SetMeta(ctx, s.ExternalID, ccpool.MetaKeyOrphanReclaimed, ccpool.FormatMetaTime(orphanNow(deps))); err != nil {
		slog.Warn("orphan reconcile: could not mark the row reclaimed", "session", s.ExternalID, "err", err)
	}
	if err := deps.CC.Close(ctx, s.ExternalID, false); err != nil {
		slog.Warn("orphan reconcile: close failed; leaving the orphan for the next dispatch",
			"session", s.ExternalID, "bead", beadID, "err", err)
		return orphanLeftAlone
	}
	removeOrphanWorktree(ctx, role, deps, env, s, sessions)

	f := orphanFields(role, s)
	f["unclaimed"] = unclaimed
	slog.Info("orphan reconcile: orphaned session reclaimed", "session", s.ExternalID, "bead", beadID,
		"role", role.Name, "state", string(s.State), "lease_until", s.Meta[ccpool.MetaKeyLeaseUntil], "unclaimed", unclaimed)
	emitOrphan(deps, "info", orphanEventReclaimed, "orphaned session reclaimed after handler loss", f)
	return orphanReclaimed
}

// stopActiveOrphan enforces the role's TIME budget on a starting/ready/working
// orphan, measured from its recorded launch time.
func stopActiveOrphan(ctx context.Context, role roles.Role, deps executor.Deps, env orphanEnv, s ccpool.Session) orphanOutcome {
	cc := role.CCPool
	limit := cc.Budget.Time
	if limit <= 0 {
		return orphanLeftAlone // a role with no time budget leaves working orphans alone
	}
	launched, ok := s.LaunchedAt()
	if !ok {
		return orphanLeftAlone // cannot measure elapsed time
	}
	elapsed := orphanNow(deps).Sub(launched)
	if _, level := cc.Budget.Evaluate(usage.Snapshot{}, elapsed); level < budget.Hard {
		return orphanLeftAlone
	}
	beadID := s.Meta[ccpool.MetaKeyBead]
	if beadID == "" {
		return orphanLeftAlone
	}
	// The hard stop unclaims (= reopens) the bead, which would undo a bead that
	// finished while its session was still wrapping up. Leave such a session to
	// the closed-bead reconcile once its turn ends.
	iss, err := beads.ShowObj(ctx, deps.BD, beadID)
	if err != nil {
		slog.Warn("orphan reconcile: bead lookup failed; not hard-stopping this pass",
			"session", s.ExternalID, "bead", beadID, "err", err)
		return orphanLeftAlone
	}
	if iss.Status == "closed" {
		slog.Info("orphan reconcile: over-budget orphan's bead is already closed; leaving it to the closed-bead reconcile",
			"session", s.ExternalID, "bead", beadID)
		return orphanLeftAlone
	}

	pool := cc.PoolDir
	if pool == "" {
		pool = "default"
	}
	be := &watchdog.BudgetError{
		Role: role.Name, Pool: pool, Bead: beadID, Session: s.ExternalID,
		Limit: budget.LimitTime, Used: elapsed.Seconds(), Cap: limit.Seconds(), Elapsed: elapsed,
	}
	w := &watchdog.Watchdog{
		CC: deps.CC, BD: deps.BD, Log: deps.Log,
		Role: role.Name, Pool: cc.PoolDir,
		BudgetStopEscalateAfter: cc.BudgetStopEscalateAfter,
		RepoRoot:                deps.Cfg.RepoRoot,
		WorktreeDir:             s.CWD, // the orphan's own worktree, which safeToReset requires
	}
	slog.Info("orphan reconcile: over-budget orphan; running the hard stop", "session", s.ExternalID, "bead", beadID,
		"role", role.Name, "state", string(s.State), "elapsed", elapsed, "budget", limit)
	watchdog.HardStop(ctx, w, s.ExternalID, beadID, be)

	// The hard stop does not remove the worktree.
	after, err := deps.CC.List(ctx)
	if err != nil {
		after = nil
	}
	removeOrphanWorktree(ctx, role, deps, env, s, after)

	f := orphanFields(role, s)
	f["elapsed"] = elapsed.Seconds()
	f["budget_time"] = limit.Seconds()
	emitOrphan(deps, "error", orphanEventHardStop, "orphaned session over its time budget hard-stopped", f)
	return orphanHardStopped
}

// removeOrphanWorktree removes the orphan's per-bead worktree and anchor
// branch under the same guards as the dispatch-time cleanup (cleanupWorktree):
// worktree isolation only; never the repo root; never while a live
// starting/ready/working/needs_input peer still uses the directory (a per-bead
// worktree is shared by every role's session for that bead); an unreadable
// session list keeps it; RemoveWorktree runs with force=false so git itself
// refuses a dirty tree, which is then kept and logged. sessions is the list
// read AFTER the orphan was closed.
func removeOrphanWorktree(ctx context.Context, role roles.Role, deps executor.Deps, env orphanEnv, s ccpool.Session, sessions []ccpool.Session) {
	cc := role.CCPool
	if s.CWD == "" || s.CWD == deps.Cfg.RepoRoot || !executor.UsesWorktreeIsolation(cc.Isolation) {
		return
	}
	if sessions == nil {
		slog.Warn("orphan reconcile: worktree kept -- session list unavailable", "session", s.ExternalID, "worktree", s.CWD)
		return
	}
	for _, o := range sessions {
		if o.ExternalID == s.ExternalID || o.CWD != s.CWD || !o.Live {
			continue
		}
		if o.State != ccpool.StateIdle && o.State != ccpool.StateErrored {
			slog.Info("orphan reconcile: worktree kept -- another live session still uses it",
				"session", s.ExternalID, "worktree", s.CWD, "peer", o.ExternalID, "peer_state", string(o.State))
			return
		}
	}
	wm, err := env.open(ctx, s.CWD)
	if err != nil {
		slog.Warn("orphan reconcile: worktree kept -- open failed", "session", s.ExternalID, "worktree", s.CWD, "err", err)
		return
	}
	if err := wm.RemoveWorktree(ctx, s.CWD, false); err != nil {
		slog.Warn("orphan reconcile: worktree kept -- removal refused (uncommitted changes, or not a linked worktree)",
			"session", s.ExternalID, "worktree", s.CWD, "err", err)
		return
	}
	slog.Info("orphan reconcile: worktree removed", "session", s.ExternalID, "worktree", s.CWD)
	deleteAnchorBranch(ctx, env.open, deps.Cfg.RepoRoot, s.ExternalID, s.Meta[ccpool.MetaKeyBead])
}
