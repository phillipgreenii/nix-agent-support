package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
)

// namedPath is a path the invocation names, with the flag that named it.
type namedPath struct{ flag, path string }

// repoDir resolves the directory the repository is found from: --repo; else
// the clone containing the worktrees named by --target-worktree,
// --source-worktree and --work-worktree (flags or the PGL_* variables); else
// the current directory. Every named path (and --repo, and a current
// directory inside a repository) must be in ONE clone, compared by git
// common directory. A non-empty msg is a usage error.
func repoDir(ctx context.Context, g *gitexec.Git, c *Command, cwd string) (dir, msg string) {
	var wts []namedPath
	if c.Resume == "" && (c.Kind == KindLand || c.Kind == KindRebase) {
		for _, np := range []namedPath{
			{"--target-worktree", c.TargetWorktree}, {"--source-worktree", c.SourceWorktree}, {"--work-worktree", c.WorkWorktree},
		} {
			if np.path != "" {
				wts = append(wts, np)
			}
		}
	}
	if len(wts) == 0 {
		if c.Repo != "" {
			return c.Repo, ""
		}
		return cwd, ""
	}

	abs := func(p string) string {
		if filepath.IsAbs(p) || cwd == "" {
			return p
		}
		return filepath.Join(cwd, p)
	}
	commonOf := func(p string) (string, bool) {
		common, err := g.CommonDir(ctx, p)
		if err != nil {
			return "", false
		}
		if r, err := filepath.EvalSymlinks(common); err == nil {
			common = r
		}
		return common, true
	}

	cands := make([]namedPath, 0, 4)
	if c.Repo != "" {
		cands = append(cands, namedPath{"--repo", c.Repo})
	}
	cands = append(cands, wts...)
	var firstCommon string
	var first namedPath
	for i, np := range cands {
		p := abs(np.path)
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Sprintf("%s %s does not exist", np.flag, np.path)
		}
		common, ok := commonOf(p)
		if !ok {
			return "", fmt.Sprintf("%s %s is not inside a git repository", np.flag, np.path)
		}
		if i == 0 {
			firstCommon, first = common, np
			continue
		}
		if common != firstCommon {
			return "", sameCloneMsg(first, firstCommon, np, common)
		}
	}
	// Worktree flags win over the current directory: a current directory in
	// another clone is refused, one outside any repository is ignored.
	if c.Repo == "" && cwd != "" {
		if common, ok := commonOf(cwd); ok && common != firstCommon {
			return "", sameCloneMsg(first, firstCommon, namedPath{"the current directory", cwd}, common)
		}
	}

	if c.Repo != "" {
		return c.Repo, ""
	}
	if cwd != "" {
		if _, ok := commonOf(cwd); ok {
			return cwd, ""
		}
	}
	// Outside any repository: run from the clone's main worktree, which is
	// never removed by the operation.
	p := abs(first.path)
	if list, err := g.Worktrees(ctx, p); err == nil && len(list) > 0 && !list[0].Bare {
		return list[0].Path, ""
	}
	return p, ""
}

func sameCloneMsg(a namedPath, ac string, b namedPath, bc string) string {
	return fmt.Sprintf("%s %s is in the clone %s but %s %s is in the clone %s: land and rebase work within one git clone, not across repositories or clones",
		a.flag, a.path, ac, b.flag, b.path, bc)
}
