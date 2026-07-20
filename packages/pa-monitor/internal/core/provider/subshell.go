package provider

import (
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/subshell"
)

// Subshell returns the direct-child shell count for the session's pid, keyed by
// session-id and invalidated by the resolved transcript's (path,mtime) — the
// exact reuse the poller's countSubshellCached had, so moving it here does NOT
// turn subshell counting into a per-tick pgrep storm. No wall-clock TTL is
// added (that would recount idle sessions). Records the subshell metric only on
// an actual pgrep fetch (cache miss). A path=="" (transcript-less) session is
// never cached (matches prior behavior).
func (c *Cache) Subshell(sessionID string, pid int, path string, mtime time.Time) int {
	fetch := c.FetchSubshell
	if fetch == nil {
		counter := &subshell.Counter{}
		fetch = counter.Count
	}

	c.mu.Lock()
	node := c.bySession[sessionID]
	if node == nil {
		node = &sessionNode{pid: pid}
		c.bySession[sessionID] = node
	}
	if node.subValid && node.subPath == path && node.subMtime.Equal(mtime) {
		count := node.subCount
		c.mu.Unlock()
		return count
	}
	c.mu.Unlock()

	// Fetch without holding c.mu — Count spawns pgrep.
	start := c.now()
	n, _ := fetch(pid)
	c.record("subshell", start)

	c.mu.Lock()
	if path != "" {
		node.subPath = path
		node.subMtime = mtime
		node.subCount = n
		node.subValid = true
	}
	c.mu.Unlock()
	return n
}
