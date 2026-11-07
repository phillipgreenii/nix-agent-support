package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/x/gitclient"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
	"github.com/phillipgreenii/pg-router-handlers/internal/executor"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
	"github.com/phillipgreenii/pg-router-handlers/internal/sessionlock"
	"github.com/phillipgreenii/pg-router-handlers/internal/worktree"
)

// Worktree-keyed sweep (bead pg2-ganjb, INV-CCH-19).
//
// Every other reclaim of a per-bead worktree is keyed on a session row: the
// dispatch's own cleanup, the closed-bead reconcile, the orphan reconcile
// (orphan.go) and the shutdown sweep. A handler SIGKILLed or crashed between
// isolation.Ensure and CC.Ensure leaves a worktree, and its pg-router/<bead>
// anchor branch, with NO row, so none of them can find it; so does a worktree
// whose row was closed (idle_ttl, cap eviction, the operator) before anyone
// reclaimed it. sweepLeakedWorktrees finds those by scanning WorktreeDir itself.
//
// It runs at dispatch time, after the orphan reconcile, for a role with worktree
// isolation, and removes a worktree only when ALL of these hold:
//
//   - it is a linked worktree directly under WorktreeDir whose checked-out branch
//     is exactly pg-router/<dir name>;
//   - git does not hold it locked (<admin dir>/locked); every checkout-pool
//     slot is locked and detached, so the sweep never touches one;
//   - it is older than the grace window (the launch wait plus the lease TTL), a
//     second guard behind the lock for a handler of an older build that does not
//     take it;
//   - no open or live session row in this role's pool names it (by working
//     directory or by pgrouter.bead), read again under the lock;
//   - no live process anywhere has it as its working directory (bead pg2-e5yw3,
//     processCWDs), probed again under the lock; a failed probe keeps it;
//   - its working tree is clean (so nothing uncommitted is lost), unless it is an
//     interrupted checkout (a zero-byte index.lock in its admin directory older than
//     the grace window, pg2-f6lxmp; then its status entries are the half-finished
//     checkout and it is removed with force), and the branch
//     holds no commit the canonical clone's HEAD lacks (so no work is lost); an
//     unreadable status or count keeps it;
//   - nobody holds the per-bead worktree lock (sessionlock.WorktreeKey): every
//     live dispatch holds it shared from before it creates the worktree until it
//     returns, and the sweep takes it exclusive and non-blocking. The kernel
//     drops it when a handler dies, so a killed handler's worktree is exactly one
//     whose lock is free.
//
// Other roles' pools: only this role's own pool is visible through the rows, and
// a per-bead worktree is shared by every role's session for the bead. A session
// of another role whose handler is gone (spared at a daemon restart, so its flock
// died with the handler) is invisible to the row and lock guards even though
// claude is still running in the worktree (a spared escalation-triager reclaimed from under a live claude, left running
// in a deleted directory). The live-process guard closes it without needing to
// know any other pool: a session that is alive has a claude process whose
// working directory is the worktree, whichever pool or handler owns it. A
// session whose process is gone is not protected, and rightly so; the clean-tree
// and no-unique-commits guards still bound what is lost then. The guard is shared (internal/procprobe) with the dispatch-time cleanup and the
// closed-bead teardown, which keep a worktree a live process holds in the same
// way.
//
// Best effort: every failure is logged and leaves the worktree for the next
// dispatch; it never fails the dispatch.

const (
	// worktreeSweepMaxPerPass bounds how many worktrees one dispatch removes, so a
	// large backlog is drained over several dispatches rather than in one.
	worktreeSweepMaxPerPass = 5
	// worktreeSweepMaxExamined bounds how many candidates one pass examines with
	// git (status and commit count), kept or removed, so a backlog of kept
	// worktrees cannot make one dispatch pay for all of them. A candidate skipped
	// by the keep cache costs no git work and does not count.
	worktreeSweepMaxExamined = 20
	// worktreeEventReclaimed is the eventlog kind recorded per removed worktree.
	worktreeEventReclaimed = "worktree_reclaimed"
)

// worktreeSweepGrace is how old a worktree must be before the sweep may remove
// it: the whole launch wait plus one lease TTL.
func worktreeSweepGrace(deps executor.Deps) time.Duration {
	return ccpool.EnsureTimeout + deps.Cfg.LeaseTTL
}

// leakedWorktree is a sweep candidate.
type leakedWorktree struct {
	bead  string
	path  string
	admin string // <common dir>/worktrees/<name>
	age   time.Duration
}

// sweepLeakedWorktrees reclaims leaked per-bead worktrees; it returns how many
// it removed.
//
// The whole pass runs under deps.Cfg.SweepDeadline (<= 0: none). On expiry it
// logs and returns what it had done, never an error.
func sweepLeakedWorktrees(ctx context.Context, role roles.Role, deps executor.Deps, env orphanEnv) (removed int) {
	if d := deps.Cfg.SweepDeadline; d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	defer func() {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			slog.Warn("worktree sweep: deadline expired; returning the partial result", "role", role.Name, "removed", removed, "deadline", deps.Cfg.SweepDeadline)
		}
	}()
	if role.CCPool == nil || !executor.UsesWorktreeIsolation(role.CCPool.Isolation) {
		return 0
	}
	dir := deps.Cfg.WorktreeDir
	if dir == "" || env.open == nil {
		return 0
	}
	if env.lockDir == "" {
		slog.Warn("worktree sweep skipped: no lock directory", "role", role.Name)
		return 0
	}
	counts := countsFrom(ctx)
	listStart := time.Now()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("worktree sweep: cannot read the worktree directory", "dir", dir, "err", err)
		}
		return 0
	}
	if len(entries) == 0 {
		return 0
	}
	sessions, err := deps.CC.List(ctx)
	if err != nil {
		slog.Warn("worktree sweep: session list failed; no action", "role", role.Name, "err", err)
		return 0
	}
	now := orphanNow(deps)
	grace := worktreeSweepGrace(deps)
	var candidates []leakedWorktree
	var leftovers []leftoverDir
	purgeTrash(ctx, dir, now, grace)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		c, ok := sweepCandidate(filepath.Join(dir, e.Name()), e.Name(), now, grace)
		if !ok {
			// Not a linked worktree: it may be the unregistered leftover of a killed
			// removal (leftoverdir.go).
			if l, isLeftover := leftoverCandidate(filepath.Join(dir, e.Name()), e.Name(), deps.Cfg.RepoRoot, now, grace); isLeftover {
				leftovers = append(leftovers, l)
			}
			continue
		}
		if worktreeHeldByRow(sessions, c) {
			clearFirstSeen(env.lockDir, c)
			continue
		}
		candidates = append(candidates, c)
	}
	counts.setPhase(phaseList, time.Since(listStart))
	probeStart := time.Now()
	if len(candidates) > 0 && env.cwds != nil {
		// One process listing for the whole pass, taken only when something is a
		// candidate; reclaim re-probes under the lock. A failed probe means the
		// sweep cannot prove any worktree unused, so it acts on none.
		cwds, err := env.cwds(ctx)
		if err != nil {
			slog.Warn("worktree sweep: live-process probe failed; no action", "role", role.Name, "err", err)
			trackUnexamined(counts, env.lockDir, orphanNow(deps), candidates, nil)
			return 0
		}
		kept := candidates[:0]
		for _, c := range candidates {
			if worktreeHeldByProcess(cwds, c) {
				slog.Info("worktree sweep: a live process has the worktree as its working directory; keeping it", "bead", c.bead, "worktree", c.path)
				clearFirstSeen(env.lockDir, c)
				continue
			}
			kept = append(kept, c)
		}
		candidates = kept
	}
	counts.setPhase(phaseProbe, time.Since(probeStart))
	scanStart := time.Now()
	reached := map[string]bool{}
	examined := 0
	// Oldest-examined-first (design D5), so kept candidates that sort first by
	// name cannot starve a later one behind the examination cap.
	orderOldestExamined(env.lockDir, candidates)
	for _, c := range candidates {
		if removed >= worktreeSweepMaxPerPass || examined >= worktreeSweepMaxExamined || ctx.Err() != nil {
			break
		}
		ok, didGit, cached := reclaimLeakedWorktree(ctx, role, deps, env, c)
		reached[c.bead] = true
		if cached {
			counts.cached()
		} else {
			counts.examined()
		}
		if ok {
			removed++
		}
		if didGit {
			examined++
			if !ok {
				noteExamined(env.lockDir, c, orphanNow(deps))
			}
		}
	}
	for _, l := range leftovers {
		if removed >= worktreeSweepMaxPerPass || ctx.Err() != nil {
			break
		}
		if reclaimLeftoverDir(ctx, role, deps, env, l) {
			removed++
		}
	}
	counts.setPhase(phaseScan, time.Since(scanStart))
	trackUnexamined(counts, env.lockDir, orphanNow(deps), candidates, reached)
	return removed
}

// sweepCandidate reports whether path is a linked-worktree directory old enough
// to consider; it is a cheap, lock-free pre-filter, and everything it passes is
// re-checked under the lock.
func sweepCandidate(path, bead string, now time.Time, grace time.Duration) (leakedWorktree, bool) {
	gitFile := filepath.Join(path, ".git")
	info, err := os.Lstat(gitFile)
	if err != nil || !info.Mode().IsRegular() {
		return leakedWorktree{}, false // not a linked worktree (no .git file)
	}
	age := now.Sub(info.ModTime())
	if age < grace {
		return leakedWorktree{}, false
	}
	admin, ok := worktreeAdminDir(path)
	if !ok {
		return leakedWorktree{}, false
	}
	return leakedWorktree{bead: bead, path: path, admin: admin, age: age}, true
}

// worktreeAdminDir resolves path's git admin directory (<common dir>/worktrees/
// <name>) from its .git file ("gitdir: <dir>"), and confirms the registration is
// mutual: the admin directory's gitdir file must point back at path/.git. A
// worktree git no longer knows (pruned) or one pointing elsewhere is not ours to
// remove.
func worktreeAdminDir(path string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(raw))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	admin := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if !filepath.IsAbs(admin) {
		admin = filepath.Join(path, admin)
	}
	admin = filepath.Clean(admin)
	if filepath.Base(filepath.Dir(admin)) != "worktrees" {
		return "", false
	}
	back, err := os.ReadFile(filepath.Join(admin, "gitdir"))
	if err != nil {
		return "", false
	}
	want, err1 := filepath.EvalSymlinks(filepath.Join(path, ".git"))
	got, err2 := filepath.EvalSymlinks(strings.TrimSpace(string(back)))
	if err1 != nil || err2 != nil || want != got {
		return "", false
	}
	return admin, true
}

// worktreeHeldByRow reports whether an open or live session row names c's
// worktree, by working directory or by its pgrouter.bead meta. A closed,
// not-live row never protects a worktree: that is the idle_ttl-closed case this
// sweep exists for.
func worktreeHeldByRow(sessions []ccpool.Session, c leakedWorktree) bool {
	wantCWD := c.path
	if r, err := filepath.EvalSymlinks(c.path); err == nil {
		wantCWD = r
	}
	for _, s := range sessions {
		if s.CloseReason != "" && !s.Live {
			continue
		}
		if s.Meta[ccpool.MetaKeyBead] == c.bead {
			return true
		}
		if s.CWD == "" {
			continue
		}
		cwd := s.CWD
		if r, err := filepath.EvalSymlinks(cwd); err == nil {
			cwd = r
		}
		if cwd == wantCWD || s.CWD == c.path {
			return true
		}
	}
	return false
}

// reclaimLeakedWorktree re-checks c under the exclusive per-bead lock and
// removes it (worktree, then anchor branch) when every guard holds. The second
// result reports whether it spent git work (status / commit count) on c, which
// is what the examined cap counts; the third that the keep cache answered, so
// no guard beyond the cheap ones ran.
func reclaimLeakedWorktree(ctx context.Context, role roles.Role, deps executor.Deps, env orphanEnv, c leakedWorktree) (removed, didGit, cached bool) {
	lock, err := sessionlock.TryLock(env.lockDir, sessionlock.WorktreeKey(c.bead))
	if err != nil {
		if errors.Is(err, sessionlock.ErrHeld) {
			slog.Info("worktree sweep: worktree in use by a live dispatch; skipping", "bead", c.bead, "worktree", c.path)
		} else {
			slog.Warn("worktree sweep: could not lock the worktree; skipping", "bead", c.bead, "worktree", c.path, "err", err)
		}
		return false, didGit, false
	}
	defer lock.Unlock()

	// Re-read everything under the lock: a dispatch that started after the
	// caller's list now either holds the shared lock (we would not be here) or
	// has already created its row.
	sessions, err := deps.CC.List(ctx)
	if err != nil {
		slog.Warn("worktree sweep: re-list under lock failed; no action", "bead", c.bead, "err", err)
		return false, didGit, false
	}
	if worktreeHeldByRow(sessions, c) {
		slog.Info("worktree sweep: a session row now names the worktree; keeping it", "bead", c.bead, "worktree", c.path)
		return false, didGit, false
	}
	if env.cwds != nil {
		cwds, err := env.cwds(ctx)
		if err != nil {
			slog.Warn("worktree sweep: live-process probe failed under lock; keeping the worktree", "bead", c.bead, "worktree", c.path, "err", err)
			return false, didGit, false
		}
		if worktreeHeldByProcess(cwds, c) {
			slog.Info("worktree sweep: a live process has the worktree as its working directory; keeping it", "bead", c.bead, "worktree", c.path)
			return false, didGit, false
		}
	}
	if _, err := os.Lstat(filepath.Join(c.admin, "locked")); err == nil {
		slog.Info("worktree sweep: worktree is locked by git; keeping it", "bead", c.bead, "worktree", c.path)
		return false, didGit, false
	} else if !errors.Is(err, os.ErrNotExist) {
		slog.Warn("worktree sweep: cannot read the git lock; keeping the worktree", "bead", c.bead, "worktree", c.path, "err", err)
		return false, didGit, false
	}

	// Keep cache: a worktree kept recently for uncommitted changes or unpushed
	// commits is not re-examined until the TTL passes or its HEAD, index or branch
	// tip changes. Everything above is cheap and has already run.
	now := orphanNow(deps)
	sig := keepSignature(c)
	if keepCached(env.lockDir, c, sig, now) {
		slog.Info("worktree sweep: kept recently (dirty or unpushed) and unchanged; skipping the re-examination", "bead", c.bead, "worktree", c.path)
		return false, false, true
	}
	didGit = true

	branch := "pg-router/" + c.bead
	wm, err := env.open(ctx, c.path)
	if err != nil {
		slog.Warn("worktree sweep: open failed; keeping the worktree", "bead", c.bead, "worktree", c.path, "err", err)
		return false, didGit, false
	}
	loc, okLoc := wm.(gitclient.Locator)
	st, okSt := wm.(gitclient.StatusReader)
	if !okLoc || !okSt {
		slog.Warn("worktree sweep: git client cannot read branch/status; keeping the worktree", "bead", c.bead, "worktree", c.path)
		return false, didGit, false
	}
	cur, err := loc.CurrentBranch(ctx)
	if err != nil || cur != branch {
		slog.Info("worktree sweep: checked-out branch is not the anchor branch; keeping the worktree",
			"bead", c.bead, "worktree", c.path, "branch", cur, "err", err)
		return false, didGit, false
	}
	dirty, err := st.Status(ctx)
	if err != nil {
		slog.Warn("worktree sweep: status unreadable; keeping the worktree", "bead", c.bead, "worktree", c.path, "err", err)
		return false, didGit, false
	}
	// Interrupted checkout (pg2-f6lxmp): a zero-byte index.lock older than the grace
	// window, with every guard above proving no owner, means a checkout died
	// half-way; its "dirt" is the unfinished checkout, not edits, so it is not kept.
	interrupted := len(dirty) > 0 && worktree.HasStaleIndexLock(c.admin, now, worktreeSweepGrace(deps))
	if interrupted {
		slog.Warn("worktree sweep: interrupted checkout (stale zero-byte index.lock); reclaiming despite status entries",
			"bead", c.bead, "worktree", c.path, "entries", len(dirty))
	}
	if len(dirty) > 0 && !interrupted {
		slog.Warn("worktree sweep: worktree has uncommitted changes; keeping it", "bead", c.bead, "worktree", c.path, "entries", len(dirty))
		keepRemember(env.lockDir, c, sig, now, deps.Cfg.WorktreeKeepRecheck)
		return false, didGit, false
	}
	root, err := env.open(ctx, deps.Cfg.RepoRoot)
	if err != nil {
		slog.Warn("worktree sweep: open repo root failed; keeping the worktree", "bead", c.bead, "err", err)
		return false, didGit, false
	}
	refs, ok := root.(gitclient.RefReader)
	if !ok {
		slog.Warn("worktree sweep: git client cannot count commits; keeping the worktree", "bead", c.bead, "worktree", c.path)
		return false, didGit, false
	}
	ahead, err := refs.CommitsAhead(ctx, "HEAD", branch)
	if err != nil {
		slog.Warn("worktree sweep: commit count unreadable; keeping the worktree", "bead", c.bead, "worktree", c.path, "err", err)
		return false, didGit, false
	}
	if ahead > 0 {
		slog.Warn("worktree sweep: branch has commits the repo HEAD lacks; keeping the worktree",
			"bead", c.bead, "worktree", c.path, "branch", branch, "unique_commits", ahead)
		keepRemember(env.lockDir, c, sig, now, deps.Cfg.WorktreeKeepRecheck)
		return false, didGit, false
	}

	if err := worktree.RemoveWithin(ctx, wm, c.path, interrupted, deps.Cfg.WorktreeRemoveTimeout, worktree.PruneVia(env.open, deps.Cfg.RepoRoot)); err != nil {
		slog.Warn("worktree sweep: removal refused; keeping the worktree", "bead", c.bead, "worktree", c.path, "err", err)
		return false, didGit, false
	}
	deleteAnchorBranch(ctx, env.open, deps.Cfg.RepoRoot, "worktree-sweep:"+c.bead, c.bead)
	slog.Info("worktree sweep: leaked worktree reclaimed", "bead", c.bead, "worktree", c.path, "role", role.Name, "age", c.age)
	emitOrphan(deps, "info", worktreeEventReclaimed, "leaked worktree reclaimed", map[string]any{
		"bead": c.bead, "role": role.Name, "worktree": c.path, "branch": branch, "age_seconds": c.age.Seconds(),
	})
	return true, true, false
}

// sweepStaleLocks garbage-collects the handler's own lock directory (bead
// pg2-bjhoq): one empty <external_id>.lock per session and one
// worktree--<bead>.lock per bead used to accumulate forever. It removes only
// files unused for sessionlock.DefaultStaleAge whose exclusive flock it can take
// without blocking, unlinking under that flock, so it can never remove a lock a
// live dispatch or reclaimer holds (shared or exclusive); the open-then-unlink
// race against a concurrent opener is closed by the opener's inode re-verification
// (see sessionlock.SweepStale). It runs after the worktree sweep, which has
// already released its own per-bead locks. It also garbage-collects the worktree
// keep cache (keepcache.go). Best effort; returns the count removed.
func sweepStaleLocks(deps executor.Deps) int {
	if deps.LockDir == "" {
		return 0
	}
	now := orphanNow(deps)
	return sessionlock.SweepStale(deps.LockDir, sessionlock.DefaultStaleAge, now) + sweepKeepCache(deps.LockDir, now)
}
