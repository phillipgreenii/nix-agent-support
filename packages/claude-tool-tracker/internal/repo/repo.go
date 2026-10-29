// Package repo maps a working directory to the name of its repository, so a
// git worktree counts as the repo it was created from.
package repo

import (
	"os"
	"path/filepath"
	"strings"
)

// Name returns the repository name for cwd, or "" when none can be found.
//
// A directory with a `.git` directory is its own repo (the name is that
// directory's base name). A directory with a `.git` file is a linked worktree
// (or submodule): the name comes from the common git directory the file points
// at, which is the main checkout's. When the directory no longer exists (a
// removed worktree), the nearest existing ancestor is used, then a path
// heuristic for the `.worktrees/<name>` and `.claude/worktrees/<name>`
// layouts.
func Name(cwd string) string {
	if cwd == "" || !filepath.IsAbs(cwd) {
		return ""
	}
	cwd = filepath.Clean(cwd)
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if name, found := fromGitEntry(dir); found {
			return name
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return fromPath(cwd)
}

func fromGitEntry(dir string) (string, bool) {
	gitPath := filepath.Join(dir, ".git")
	st, err := os.Lstat(gitPath)
	if err != nil {
		return "", false
	}
	if st.IsDir() {
		return filepath.Base(dir), true
	}
	raw, err := os.ReadFile(gitPath)
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(raw))
	gitdir, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return "", false
	}
	gitdir = strings.TrimSpace(gitdir)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(dir, gitdir)
	}
	common := gitdir
	if c, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
		common = strings.TrimSpace(string(c))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitdir, common)
		}
	}
	common = filepath.Clean(common)
	base := filepath.Base(common)
	if base == ".git" {
		return filepath.Base(filepath.Dir(common)), true
	}
	return strings.TrimSuffix(base, ".git"), true
}

// fromPath handles a worktree directory that is gone: the repo is the
// directory that holds `.worktrees/` (or `.claude/worktrees/`).
func fromPath(cwd string) string {
	parts := strings.Split(cwd, string(filepath.Separator))
	for i := len(parts) - 1; i >= 2; i-- {
		switch {
		case parts[i-1] == ".worktrees" && parts[i-2] != "":
			return parts[i-2]
		case parts[i-1] == "worktrees" && parts[i-2] == ".claude" && i >= 3 && parts[i-3] != "":
			return parts[i-3]
		}
	}
	return ""
}
