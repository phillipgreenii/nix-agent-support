package parknote

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Found is one existing worktree checked out on drain/<bead>.
type Found struct {
	// Repo is the configured checkout whose `git worktree list` reported it.
	Repo string
	// Path is the worktree directory.
	Path string
}

// Location is where a bead's drain work lives across a set of repositories.
type Location struct {
	// Worktrees are the distinct existing worktrees on drain/<bead>, in the
	// order the repositories were searched. The same directory reported through
	// two configured checkouts of one repository counts once.
	Worktrees []Found
	// BranchOnly are the searched repositories that have the local branch
	// drain/<bead> but no worktree on it.
	BranchOnly []string
}

// Locate finds the worktree(s) a drain worker left on drain/<bead> in dirs
// (bead pg2-b49ydt). It is read-only: only `git worktree list --porcelain` and
// `git rev-parse --verify` run, through g, each under gitTimeout and the whole
// search under discoverTimeout.
//
// Unlike Build's best-effort discovery, a repository that cannot be searched is
// an ERROR: the caller picks a working directory from the answer, and "absent"
// cannot be told from "could not look". A worktree whose directory is gone
// (git still lists it until pruned) does not count as found.
func Locate(ctx context.Context, g Git, bead string, dirs []string) (Location, error) {
	var loc Location
	if bead == "" {
		return loc, fmt.Errorf("no bead id")
	}
	if g == nil {
		g = OSGit{}
	}
	dctx, cancel := context.WithTimeout(ctx, discoverTimeout)
	defer cancel()
	seenDir := map[string]bool{}
	seenWT := map[string]bool{}
	for _, dir := range dirs {
		if dir == "" || seenDir[dir] {
			continue
		}
		seenDir[dir] = true
		out, err := gitOut(dctx, g, dir, "worktree", "list", "--porcelain")
		if err != nil {
			return Location{}, fmt.Errorf("git worktree list in %s: %w", dir, err)
		}
		haveWT := false
		for _, e := range parseWorktrees(out) {
			if e.branch != drainBranchPrefix+bead {
				continue
			}
			if _, err := os.Stat(e.path); err != nil {
				continue // registered but gone (prunable)
			}
			haveWT = true
			key := e.path
			if r, err := filepath.EvalSymlinks(e.path); err == nil {
				key = r
			}
			if seenWT[key] {
				continue
			}
			seenWT[key] = true
			loc.Worktrees = append(loc.Worktrees, Found{Repo: dir, Path: e.path})
		}
		if !haveWT && branchExists(dctx, g, dir, bead) {
			loc.BranchOnly = append(loc.BranchOnly, dir)
		}
	}
	return loc, nil
}

// Describe renders loc.Worktrees for an error message.
func (l Location) Describe() string {
	parts := make([]string, 0, len(l.Worktrees))
	for _, w := range l.Worktrees {
		parts = append(parts, fmt.Sprintf("%s (repo %s)", w.Path, w.Repo))
	}
	return strings.Join(parts, "; ")
}
