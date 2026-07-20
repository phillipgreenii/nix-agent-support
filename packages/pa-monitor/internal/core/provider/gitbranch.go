package provider

import (
	"os"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// defaultFetchGitBranch resolves cwd's repo HEAD (full parent walk) and reads
// the branch — behaviorally identical to session.GitBranch, but also returns the
// resolved HEAD path so the provider can cache UntilFileChanges(HEAD).
func defaultFetchGitBranch(cwd string) (branch, headPath string, ok bool) {
	hp, found := session.ResolveHeadPath(cwd)
	if !found {
		return "", "", false
	}
	return session.ReadHead(hp), hp, true
}

// GitBranch returns the branch of the repo containing cwd, cached per-cwd with
// UntilFileChanges(.git/HEAD) freshness: a POSITIVE resolution is cached and only
// re-read when the resolved HEAD file's mtime changes (a branch switch). A
// NEGATIVE ("not a repo") resolution is NOT cached — the next tick re-walks, so
// a mid-session `git init` is still detected (behavior-preserving). Records the
// git_branch subprocess metric only on an actual fetch (miss/change/negative).
func (c *Cache) GitBranch(cwd string) string {
	fetch := c.FetchGitBranch
	if fetch == nil {
		fetch = defaultFetchGitBranch
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	node := c.byCwd[cwd]
	if node == nil {
		node = &cwdNode{}
		c.byCwd[cwd] = node
	}

	// Cache hit: positive resolution with an unchanged HEAD mtime.
	if node.branchValid {
		if st, err := os.Stat(node.headPath); err == nil && st.ModTime().Equal(node.branchMtime) {
			return node.branch
		}
	}

	// Miss / HEAD changed / previously-negative: fetch. Local FS ops are allowed
	// under c.mu (the lock invariant only forbids holding it across ps/gh).
	start := c.now()
	branch, headPath, ok := fetch(cwd)
	c.record("git_branch", start)
	if !ok {
		node.branchValid = false // negative not cached — re-walk next tick
		return ""
	}
	node.headPath = headPath
	node.branch = branch
	node.branchValid = true
	if st, err := os.Stat(headPath); err == nil {
		node.branchMtime = st.ModTime()
	}
	return branch
}
