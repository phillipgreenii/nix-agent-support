// discovery.go: expands repo_search_paths into repo paths.
//
// Search rule: for each configured search path, list its immediate children
// only. A child is a repo when its .git entry is a directory (a clone) or a
// regular file (a submodule, or a worktree checkout sitting directly under the
// search path). Nothing deeper is visited, and the search path itself is not
// treated as a repo. A search path that does not exist or is not a directory
// is skipped with one stderr line, never fatal.
package internal

import (
	"os"
	"path/filepath"
	"sort"
)

// discoverRepos expands searchPaths into the repo paths found exactly one
// level below them. Children are visited in sorted order so the output is
// deterministic.
func (b *Backend) discoverRepos(searchPaths []string) []string {
	var repos []string
	for _, sp := range searchPaths {
		st, err := os.Stat(sp)
		switch {
		case err != nil:
			b.logSkip(sp, "search path does not exist or is not readable")
			continue
		case !st.IsDir():
			b.logSkip(sp, "search path is not a directory")
			continue
		}
		entries, err := os.ReadDir(sp)
		if err != nil {
			b.logSkip(sp, "search path cannot be listed: "+err.Error())
			continue
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, name := range names {
			child := filepath.Join(sp, name)
			if hasGitEntry(child) {
				repos = append(repos, child)
			}
		}
	}
	return repos
}

// hasGitEntry reports whether dir is a directory whose .git entry is a
// directory or a regular file (symlinks are followed).
func hasGitEntry(dir string) bool {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return false
	}
	st, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil {
		return false
	}
	return st.IsDir() || st.Mode().IsRegular()
}

// repoList is the final ordered list of repos a call reads: the configured
// repo_paths entries, then the repos discovered under repo_search_paths. A
// repo reachable more than once is listed once (first occurrence wins),
// de-duplicated by its resolved path.
func (b *Backend) repoList(cfg Config) []string {
	all := append(append([]string{}, cfg.RepoPaths...), b.discoverRepos(cfg.RepoSearchPaths)...)
	seen := make(map[string]bool, len(all))
	out := make([]string, 0, len(all))
	for _, p := range all {
		key := p
		if r, err := filepath.EvalSymlinks(p); err == nil {
			key = r
		}
		key = filepath.Clean(key)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	return out
}
