package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/executor"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
	"github.com/phillipgreenii/pg-router-handlers/internal/sessionlock"
	"github.com/phillipgreenii/pg-router-handlers/internal/worktree"
)

// Unregistered leftover directories.
//
// A `git worktree remove --force` killed at its deadline (a very large
// checkout takes longer than any fixed deadline) deletes the `.git` file first
// and the working files afterwards, and git later prunes the admin directory.
// What remains at <worktree dir>/<bead> is a partial checkout that git no
// longer knows: no `.git` entry, no registration. The linked-worktree sweep
// (sweepCandidate) never sees it, and `git worktree add` refuses the path with
// "already exists" on every later dispatch of the bead.
//
// Choice recorded (item 2 of the fix): the removal paths themselves are NOT
// changed (the removal is x/gitclient's single `git worktree remove` and the
// handler cannot move the tree aside without racing git's own bookkeeping);
// instead the leftover is HEALED on the next dispatch of the bead (the same
// guarded own-bead examination that reclaims a leaked worktree) and on the next
// janitor sweep. The reclaim itself never deletes in place: it first renames
// the directory into <worktree dir>/.trash, so a kill during the slow delete
// leaves a trash entry, never a half-deleted tree at the bead's path, and the
// sweep purges old trash entries.
//
// Deletion is paranoid. The directory is reclaimed only when ALL hold, each
// re-checked under the exclusive per-bead lock:
//   - it is a real, non-empty directory (Lstat; a symlink at the path is
//     refused) older than the sweep grace window;
//   - it has no `.git` entry at the top, and no `.git` entry anywhere inside
//     (a nested repository is real data);
//   - nothing inside it is a symlink or anything but a regular file or
//     directory (nothing is ever followed);
//   - the repository's admin directories under <common dir>/worktrees name no
//     worktree at this path (an unreadable admin directory keeps it);
//   - no open or live session row names the bead or the path, and no live
//     process has it, or a directory inside it, as its working directory (a
//     process listing that cannot be read keeps it).

// trashDirName is the directory under the worktree directory that receives a
// leftover before its slow delete. Its leading dot keeps it out of the
// candidate scan.
const trashDirName = ".trash"

// leftoverDir is a candidate unregistered leftover directory.
type leftoverDir struct {
	bead string
	path string
	age  time.Duration
}

// asLeaked adapts a leftover to the row and process guards, which key on bead
// and path only.
func (l leftoverDir) asLeaked() leakedWorktree {
	return leakedWorktree{bead: l.bead, path: l.path, age: l.age}
}

// leftoverCandidate is the cheap, lock-free pre-filter: path is a real,
// non-empty directory with no `.git` entry, old enough, and git has no
// registration pointing at it. Everything it passes is re-checked under the
// lock with the full tree walk.
func leftoverCandidate(path, bead, repoRoot string, now time.Time, grace time.Duration) (leftoverDir, bool) {
	if strings.HasPrefix(bead, ".") {
		return leftoverDir{}, false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() { // a symlink (even to a directory) is not IsDir under Lstat
		return leftoverDir{}, false
	}
	if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return leftoverDir{}, false
	}
	age := now.Sub(info.ModTime())
	if age < grace {
		return leftoverDir{}, false
	}
	if entries, err := os.ReadDir(path); err != nil || len(entries) == 0 {
		return leftoverDir{}, false // empty is accepted by `git worktree add`
	}
	if registered, err := pathRegisteredAsWorktree(repoRoot, path); err != nil || registered {
		if err != nil {
			slog.Warn("worktree sweep: cannot read git's worktree registrations; keeping the leftover directory", "bead", bead, "path", path, "err", err)
		}
		return leftoverDir{}, false
	}
	return leftoverDir{bead: bead, path: path, age: age}, true
}

// commonGitDir resolves repoRoot's common git directory: repoRoot/.git when it
// is a directory, else (repoRoot is itself a linked worktree) the common dir
// its .git file points through.
func commonGitDir(repoRoot string) (string, error) {
	dotGit := filepath.Join(repoRoot, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return dotGit, nil
	}
	admin, ok := worktreeGitdirOf(repoRoot)
	if !ok || filepath.Base(filepath.Dir(admin)) != "worktrees" {
		return "", fmt.Errorf("%s: not a git directory or linked worktree", repoRoot)
	}
	return filepath.Dir(filepath.Dir(admin)), nil
}

// worktreeGitdirOf parses "gitdir: <dir>" from dir/.git.
func worktreeGitdirOf(dir string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, ".git"))
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
		admin = filepath.Join(dir, admin)
	}
	return filepath.Clean(admin), true
}

// pathRegisteredAsWorktree reports whether any admin directory under the
// repository's <common dir>/worktrees names path (its gitdir file points at
// path/.git). Any read failure other than "no worktrees directory" is an error
// and the caller keeps the directory.
func pathRegisteredAsWorktree(repoRoot, path string) (bool, error) {
	common, err := commonGitDir(repoRoot)
	if err != nil {
		return false, err
	}
	entries, err := os.ReadDir(filepath.Join(common, "worktrees"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	want := resolve(path)
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(common, "worktrees", e.Name(), "gitdir"))
		if err != nil {
			return false, err
		}
		pointed := filepath.Dir(strings.TrimSpace(string(raw)))
		if resolve(pointed) == want || filepath.Clean(pointed) == filepath.Clean(path) {
			return true, nil
		}
	}
	return false, nil
}

// leftoverTreeIsPlain walks path without following anything and reports whether
// it holds only regular files and directories and no `.git` entry at any depth.
func leftoverTreeIsPlain(path string) (bool, string) {
	reason := ""
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			reason = "unreadable entry " + p
			return err
		}
		if d.Name() == ".git" {
			reason = "has a .git entry at " + p
			return errors.New(reason)
		}
		if t := d.Type(); !t.IsDir() && !t.IsRegular() {
			reason = "has a symlink or special file at " + p
			return errors.New(reason)
		}
		return nil
	})
	return err == nil, reason
}

// reclaimLeftoverDir re-checks l under the exclusive per-bead lock and removes
// it (rename into the trash directory, then delete) when every guard holds.
func reclaimLeftoverDir(ctx context.Context, role roles.Role, deps executor.Deps, env orphanEnv, l leftoverDir) bool {
	lock, err := sessionlock.TryLock(env.lockDir, sessionlock.WorktreeKey(l.bead))
	if err != nil {
		if errors.Is(err, sessionlock.ErrHeld) {
			slog.Info("worktree sweep: leftover directory in use by a live dispatch; skipping", "bead", l.bead, "path", l.path)
		} else {
			slog.Warn("worktree sweep: could not lock the leftover directory; skipping", "bead", l.bead, "path", l.path, "err", err)
		}
		return false
	}
	defer lock.Unlock()

	// Re-check the cheap structural guards: the world may have moved since the
	// pre-filter ran.
	now := orphanNow(deps)
	cur, ok := leftoverCandidate(l.path, l.bead, deps.Cfg.RepoRoot, now, worktreeSweepGrace(deps))
	if !ok {
		return false
	}
	c := cur.asLeaked()
	sessions, err := deps.CC.List(ctx)
	if err != nil {
		slog.Warn("worktree sweep: re-list under lock failed; leftover directory kept", "bead", l.bead, "err", err)
		return false
	}
	if worktreeHeldByRow(sessions, c) {
		slog.Info("worktree sweep: a session row names the leftover directory; keeping it", "bead", l.bead, "path", l.path)
		return false
	}
	if env.cwds != nil {
		cwds, err := env.cwds(ctx)
		if err != nil {
			slog.Warn("worktree sweep: live-process probe failed; leftover directory kept", "bead", l.bead, "path", l.path, "err", err)
			return false
		}
		if worktreeHeldByProcess(cwds, c) {
			slog.Info("worktree sweep: a live process is inside the leftover directory; keeping it", "bead", l.bead, "path", l.path)
			return false
		}
	}
	if plain, why := leftoverTreeIsPlain(l.path); !plain {
		slog.Warn("worktree sweep: leftover directory is not plain files; keeping it", "bead", l.bead, "path", l.path, "why", why)
		return false
	}

	trashRoot := filepath.Join(filepath.Dir(l.path), trashDirName)
	if err := os.MkdirAll(trashRoot, 0o700); err != nil {
		slog.Warn("worktree sweep: cannot create the trash directory; leftover directory kept", "bead", l.bead, "err", err)
		return false
	}
	trash := filepath.Join(trashRoot, l.bead+"."+strconv.FormatInt(now.UnixNano(), 10))
	if err := os.Rename(l.path, trash); err != nil {
		slog.Warn("worktree sweep: cannot move the leftover directory aside; keeping it", "bead", l.bead, "path", l.path, "err", err)
		return false
	}
	if err := pacedRemoval.DrainErr(ctx, trash); err != nil {
		slog.Warn("worktree sweep: leftover directory moved aside but not fully deleted (a later sweep purges the trash)", "bead", l.bead, "trash", trash, "err", err)
	}
	slog.Info("worktree sweep: unregistered leftover directory reclaimed", "bead", l.bead, "path", l.path, "role", role.Name, "age", l.age)
	emitOrphan(deps, "info", worktreeEventReclaimed, "unregistered leftover directory reclaimed", map[string]any{
		"bead": l.bead, "role": role.Name, "worktree": l.path, "leftover": true, "age_seconds": l.age.Seconds(),
	})
	return true
}

// pacedRemoval is the rate-limit every bulk delete here uses (a worktree or
// leftover tree is tens of thousands of files, and each unlink is a filesystem
// event for every listener). Tests substitute a recording Sleep.
var pacedRemoval = pacedRemover{worktree.Removal{}}

// pacedRemover adds the error-only drain the leftover reclaim wants.
type pacedRemover struct{ worktree.Removal }

// DrainErr drains path and returns only a failure; an unfinished (budget-cut)
// drain is not a failure, the trash purge finishes it.
func (p pacedRemover) DrainErr(ctx context.Context, path string) error {
	_, err := p.DrainTree(ctx, path)
	return err
}

// purgeTrash deletes trash entries older than grace (a delete that was killed
// part-way or cut short by its budget), paced like the original delete. Everything in the trash was moved there by reclaimLeftoverDir
// after the plain-tree check; RemoveAll never follows a symlink.
func purgeTrash(ctx context.Context, dir string, now time.Time, grace time.Duration) {
	trashRoot := filepath.Join(dir, trashDirName)
	entries, err := os.ReadDir(trashRoot)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := os.Lstat(filepath.Join(trashRoot, e.Name()))
		if err != nil || trashAge(e.Name(), info.ModTime(), now) < grace {
			continue
		}
		st, err := pacedRemoval.DrainTree(ctx, filepath.Join(trashRoot, e.Name()))
		if err != nil {
			slog.Warn("worktree sweep: cannot purge a trash entry", "entry", e.Name(), "err", err)
			continue
		}
		if !st.Done {
			slog.Info("worktree sweep: trash entry only partly purged this pass (paced; the next sweep continues)", "entry", e.Name(), "ops", st.Ops)
			continue
		}
		slog.Info("worktree sweep: purged an interrupted leftover delete", "entry", e.Name(), "ops", st.Ops, "batches", st.Batches)
	}
}

// trashAge is how long ago a trash entry was created. Entries are named
// <bead>.<unix nanos of the move>, and that stamp is used when present because a
// paced delete keeps changing the entry's own mtime (every removed child touches
// it), which would otherwise push a half-drained entry's next purge a full grace
// window out after each pass. An entry without a parseable stamp falls back to
// its mtime.
func trashAge(name string, mtime, now time.Time) time.Duration {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		if ns, err := strconv.ParseInt(name[i+1:], 10, 64); err == nil && ns > 1e17 {
			return now.Sub(time.Unix(0, ns))
		}
	}
	return now.Sub(mtime)
}
