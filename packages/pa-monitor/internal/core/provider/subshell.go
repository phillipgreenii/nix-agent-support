package provider

import (
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/subshell"
)

// SubshellTTLDefault is the per-session cache lifetime for transcript-less
// sessions; it matches the recorder feed's 5 s minimum poll interval.
const SubshellTTLDefault = 5 * time.Second

// Subshell returns the direct-child shell count for the session's pid, keyed by
// session-id and invalidated by the resolved transcript's (path,mtime) — the
// exact reuse the poller's countSubshellCached had. Where a transcript path
// exists there is no wall-clock TTL (that would recount idle sessions). A
// path=="" (transcript-less) session has no (path,mtime) key, so it is cached
// per session for SubshellTTL instead (default SubshellTTLDefault, matching the
// 5 s feed floor); a TTL of 0 disables that cache. Records the subshell metric
// only on an actual fetch (cache miss).
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
	if path == "" && c.SubshellTTL > 0 && node.subTTLValid && c.now().Sub(node.subTTLAt) < c.SubshellTTL {
		count := node.subTTLCount
		c.mu.Unlock()
		return count
	}
	c.mu.Unlock()

	// Fetch without holding c.mu.
	start := c.now()
	n, _ := fetch(pid)
	c.record("child_procs", start)

	c.mu.Lock()
	if path != "" {
		node.subPath = path
		node.subMtime = mtime
		node.subCount = n
		node.subValid = true
	} else {
		node.subTTLAt = start
		node.subTTLCount = n
		node.subTTLValid = true
	}
	c.mu.Unlock()
	return n
}
