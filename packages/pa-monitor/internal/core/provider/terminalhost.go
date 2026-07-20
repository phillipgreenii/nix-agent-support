package provider

// TerminalHost returns the bare terminal-host name (e.g. "tmux", "cmux",
// "ghostty", "unknown") for the session's pid, keyed by session-id with
// WhilePIDAlive freshness and the "re-probe unknown each tick" exception the
// poller had (a transient "unknown" is not locked in for the PID's life). The
// cmux/bridge refinement stays in the poller (in-memory), applied to this bare
// value. Session-id keying is the PID-reuse fix: a reused PID is a new
// session-id → its own fresh detection, never a dead session's value; a
// dead-not-GC'd session keeps its own frozen host. Records the terminal_host
// metric only on an actual detection fetch.
func (c *Cache) TerminalHost(sessionID string, pid int) string {
	c.mu.Lock()
	node := c.bySession[sessionID]
	if node == nil {
		node = &sessionNode{pid: pid}
		c.bySession[sessionID] = node
	}
	if node.terminalHost != "" && node.terminalHost != "unknown" {
		host := node.terminalHost
		c.mu.Unlock()
		return host
	}
	fetch := c.FetchTerminalHost
	c.mu.Unlock()

	if fetch == nil {
		return "unknown" // bare Cache / unset boundary: never panic, never record
	}

	// Detect without holding c.mu — signalers may spawn ps/tmux/cmux.
	start := c.now()
	host := fetch(pid)
	c.record("terminal_host", start)

	c.mu.Lock()
	node.terminalHost = host
	c.mu.Unlock()
	return host
}
