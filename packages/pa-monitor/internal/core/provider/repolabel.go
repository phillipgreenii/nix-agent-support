package provider

// RepoLabel returns the workspace.repo label for cwd (canonical git origin, or a
// stable local hash), cached per-cwd with LongLived freshness — a cwd's origin
// is stable. Returns ("", false) when cwd is not a repo / has no label, and that
// negative is cached too (a non-repo cwd stays non-repo; the daemon's per-session
// labelCache remains the authoritative retry layer). No subprocess metric
// (repo-label never had one).
func (c *Cache) RepoLabel(cwd string) (string, bool) {
	c.mu.Lock()
	node := c.byCwd[cwd]
	if node == nil {
		node = &cwdNode{}
		c.byCwd[cwd] = node
	}
	if node.repoKnown {
		v := node.repoLabel
		c.mu.Unlock()
		return v, v != ""
	}
	fetch := c.FetchRepoLabel
	c.mu.Unlock()

	if fetch == nil {
		return "", false // bare Cache / unset boundary
	}

	// Fetch without holding c.mu — git config is a subprocess.
	v, ok := fetch(cwd)
	if !ok {
		v = ""
	}

	c.mu.Lock()
	node.repoLabel = v
	node.repoKnown = true
	c.mu.Unlock()
	return v, v != ""
}
