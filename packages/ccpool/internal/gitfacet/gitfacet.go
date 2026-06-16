// Package gitfacet resolves the git-dependent session-location facets reported
// by ccpool list --json (repo root, worktree, branch). Every facet fails SOFT:
// outside a git work tree (or when git is absent / errors) the corresponding
// field is nil, never an error, so a single bad cwd can't fail the whole list.
package gitfacet

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// Facets are the git-dependent location facets for a cwd. A nil field means the
// facet is unavailable (cwd is not inside a git work tree, or git failed).
type Facets struct {
	// RepoRoot is the MAIN repository root: for a normal checkout it equals
	// Worktree; for a linked worktree it is the directory containing the shared
	// .git (parent of --git-common-dir), NOT the linked worktree root.
	RepoRoot *string
	// Worktree is the working-tree root for cwd (git rev-parse --show-toplevel).
	Worktree *string
	// Branch is the checked-out branch; nil when detached (rev-parse reports
	// "HEAD") or unavailable.
	Branch *string
}

// Resolve returns the git facets for cwd, querying git with -C cwd. It never
// returns an error: any failed sub-query leaves that facet nil. A cwd outside a
// git work tree yields an all-nil Facets.
func Resolve(cwd string) Facets {
	var f Facets

	// Worktree root = the toplevel of the working tree containing cwd. This also
	// serves as the cheap "are we in a repo at all?" probe: if it fails, leave
	// everything nil.
	top, ok := git(cwd, "rev-parse", "--show-toplevel")
	if !ok {
		return f
	}
	f.Worktree = &top

	// Repo root = parent of the shared git common dir. For a normal checkout the
	// common dir is "<root>/.git", so its parent is the worktree root; for a
	// linked worktree it points at the MAIN repo's .git, so its parent is the
	// main repo root (which differs from the linked worktree).
	if commonDir, ok := git(cwd, "rev-parse", "--path-format=absolute", "--git-common-dir"); ok {
		root := filepath.Dir(commonDir)
		f.RepoRoot = &root
	}

	// Branch: nil on detached HEAD (rev-parse --abbrev-ref reports the literal
	// "HEAD") so consumers can distinguish "on a branch" from "detached".
	if branch, ok := git(cwd, "rev-parse", "--abbrev-ref", "HEAD"); ok && branch != "HEAD" {
		f.Branch = &branch
	}

	return f
}

// git runs `git -C cwd <args...>` and returns the trimmed stdout. ok is false
// when git is missing, cwd is not a repo, or the command errors.
func git(cwd string, args ...string) (string, bool) {
	full := append([]string{"-C", cwd}, args...)
	out, err := exec.Command("git", full...).Output()
	if err != nil {
		return "", false
	}
	s := strings.TrimRight(string(out), "\n")
	if s == "" {
		return "", false
	}
	return s, true
}
