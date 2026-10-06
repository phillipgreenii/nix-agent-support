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

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/executor"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/sessionlock"
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
//   - git does not hold it locked (<admin dir>/locked);
//   - it is older than the grace window (the launch wait plus the lease TTL), a
//     second guard behind the lock for a handler of an older build that does not
//     take it;
//   - no open or live session row in this role's pool names it (by working
//     directory or by pgrouter.bead), read again under the lock;
//   - its working tree is clean (so nothing uncommitted is lost) and the branch
//     holds no commit the canonical clone's HEAD lacks (so no work is lost); an
//     unreadable status or count keeps it;
//   - nobody holds the per-bead worktree lock (sessionlock.WorktreeKey): every
//     live dispatch holds it shared from before it creates the worktree until it
//     returns, and the sweep takes it exclusive and non-blocking. The kernel
//     drops it when a handler dies, so a killed handler's worktree is exactly one
//     whose lock is free.
//
// Known residual: only this role's own pool is visible, and a per-bead worktree
// is shared by every role's session for the bead. A session of another role,
// idle or orphaned in ITS pool, whose handler is gone and whose worktree is
// clean and commit-free is therefore removable here; this is the same blind spot
// cleanupWorktree and the closed-bead reconcile already have, and the guards
// above bound what can be lost to a clean tree.
//
// Best effort: every failure is logged and leaves the worktree for the next
// dispatch; it never fails the dispatch.

const (
	// worktreeSweepMaxPerPass bounds how many worktrees one dispatch removes, so a
	// large backlog is drained over several dispatches rather than in one.
	worktreeSweepMaxPerPass = 5
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
func sweepLeakedWorktrees(ctx context.Context, role roles.Role, deps executor.Deps, env orphanEnv) (removed int) {
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
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		c, ok := sweepCandidate(filepath.Join(dir, e.Name()), e.Name(), now, grace)
		if !ok || worktreeHeldByRow(sessions, c) {
			continue
		}
		candidates = append(candidates, c)
	}
	for _, c := range candidates {
		if removed >= worktreeSweepMaxPerPass {
			break
		}
		if reclaimLeakedWorktree(ctx, role, deps, env, c) {
			removed++
		}
	}
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
// removes it (worktree, then anchor branch) when every guard holds.
func reclaimLeakedWorktree(ctx context.Context, role roles.Role, deps executor.Deps, env orphanEnv, c leakedWorktree) bool {
	lock, err := sessionlock.TryLock(env.lockDir, sessionlock.WorktreeKey(c.bead))
	if err != nil {
		if errors.Is(err, sessionlock.ErrHeld) {
			slog.Info("worktree sweep: worktree in use by a live dispatch; skipping", "bead", c.bead, "worktree", c.path)
		} else {
			slog.Warn("worktree sweep: could not lock the worktree; skipping", "bead", c.bead, "worktree", c.path, "err", err)
		}
		return false
	}
	defer lock.Unlock()

	// Re-read everything under the lock: a dispatch that started after the
	// caller's list now either holds the shared lock (we would not be here) or
	// has already created its row.
	sessions, err := deps.CC.List(ctx)
	if err != nil {
		slog.Warn("worktree sweep: re-list under lock failed; no action", "bead", c.bead, "err", err)
		return false
	}
	if worktreeHeldByRow(sessions, c) {
		slog.Info("worktree sweep: a session row now names the worktree; keeping it", "bead", c.bead, "worktree", c.path)
		return false
	}
	if _, err := os.Lstat(filepath.Join(c.admin, "locked")); err == nil {
		slog.Info("worktree sweep: worktree is locked by git; keeping it", "bead", c.bead, "worktree", c.path)
		return false
	} else if !errors.Is(err, os.ErrNotExist) {
		slog.Warn("worktree sweep: cannot read the git lock; keeping the worktree", "bead", c.bead, "worktree", c.path, "err", err)
		return false
	}

	branch := "pg-router/" + c.bead
	wm, err := env.open(ctx, c.path)
	if err != nil {
		slog.Warn("worktree sweep: open failed; keeping the worktree", "bead", c.bead, "worktree", c.path, "err", err)
		return false
	}
	loc, okLoc := wm.(gitclient.Locator)
	st, okSt := wm.(gitclient.StatusReader)
	if !okLoc || !okSt {
		slog.Warn("worktree sweep: git client cannot read branch/status; keeping the worktree", "bead", c.bead, "worktree", c.path)
		return false
	}
	cur, err := loc.CurrentBranch(ctx)
	if err != nil || cur != branch {
		slog.Info("worktree sweep: checked-out branch is not the anchor branch; keeping the worktree",
			"bead", c.bead, "worktree", c.path, "branch", cur, "err", err)
		return false
	}
	dirty, err := st.Status(ctx)
	if err != nil {
		slog.Warn("worktree sweep: status unreadable; keeping the worktree", "bead", c.bead, "worktree", c.path, "err", err)
		return false
	}
	if len(dirty) > 0 {
		slog.Warn("worktree sweep: worktree has uncommitted changes; keeping it", "bead", c.bead, "worktree", c.path, "entries", len(dirty))
		return false
	}
	root, err := env.open(ctx, deps.Cfg.RepoRoot)
	if err != nil {
		slog.Warn("worktree sweep: open repo root failed; keeping the worktree", "bead", c.bead, "err", err)
		return false
	}
	refs, ok := root.(gitclient.RefReader)
	if !ok {
		slog.Warn("worktree sweep: git client cannot count commits; keeping the worktree", "bead", c.bead, "worktree", c.path)
		return false
	}
	ahead, err := refs.CommitsAhead(ctx, "HEAD", branch)
	if err != nil {
		slog.Warn("worktree sweep: commit count unreadable; keeping the worktree", "bead", c.bead, "worktree", c.path, "err", err)
		return false
	}
	if ahead > 0 {
		slog.Warn("worktree sweep: branch has commits the repo HEAD lacks; keeping the worktree",
			"bead", c.bead, "worktree", c.path, "branch", branch, "unique_commits", ahead)
		return false
	}

	if err := wm.RemoveWorktree(ctx, c.path, false); err != nil {
		slog.Warn("worktree sweep: removal refused; keeping the worktree", "bead", c.bead, "worktree", c.path, "err", err)
		return false
	}
	deleteAnchorBranch(ctx, env.open, deps.Cfg.RepoRoot, "worktree-sweep:"+c.bead, c.bead)
	slog.Info("worktree sweep: leaked worktree reclaimed", "bead", c.bead, "worktree", c.path, "role", role.Name, "age", c.age)
	emitOrphan(deps, "info", worktreeEventReclaimed, "leaked worktree reclaimed", map[string]any{
		"bead": c.bead, "role": role.Name, "worktree": c.path, "branch": branch, "age_seconds": c.age.Seconds(),
	})
	return true
}
