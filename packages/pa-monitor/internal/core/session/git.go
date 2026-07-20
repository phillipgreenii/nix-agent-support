package session

import (
	"os"
	"path/filepath"
	"strings"
)

// GitBranch returns the current branch for the git repo containing dir.
// Walks parent directories to find the repo root, and handles git worktrees
// (.git file with gitdir: pointer). Returns "" when not in a git repo.
func GitBranch(dir string) string {
	headPath, ok := ResolveHeadPath(dir)
	if !ok {
		return ""
	}
	return ReadHead(headPath)
}

// ResolveHeadPath walks up from dir to the first ancestor directory that is a
// git repo (or worktree), returning that repo's HEAD file path. Returns
// ("", false) when dir is not inside any git repo. This is the FULL parent walk
// (not the one-level resolveHeadPath); the provider caches on the resolved HEAD
// path's mtime.
func ResolveHeadPath(dir string) (string, bool) {
	for d := dir; ; {
		headPath, ok := resolveHeadPath(d)
		if ok {
			return headPath, true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
		}
		d = parent
	}
}

// resolveHeadPath finds the HEAD file for the repo or worktree rooted at dir.
func resolveHeadPath(dir string) (string, bool) {
	gitPath := filepath.Join(dir, ".git")
	fi, err := os.Stat(gitPath)
	if err != nil {
		return "", false
	}
	if fi.IsDir() {
		return filepath.Join(gitPath, "HEAD"), true
	}
	// Worktree: .git is a file containing "gitdir: <path>"
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return "", false
	}
	ref, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok {
		return "", false
	}
	if !filepath.IsAbs(ref) {
		ref = filepath.Join(dir, ref)
	}
	return filepath.Join(ref, "HEAD"), true
}

// ReadHead reads a git HEAD file and returns the branch name (for a symbolic
// ref) or a 7-char short SHA (for a detached HEAD). Returns "" when unreadable.
func ReadHead(headPath string) string {
	data, err := os.ReadFile(headPath)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	if branch, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
		return branch
	}
	if len(line) >= 7 {
		return line[:7]
	}
	return line
}
