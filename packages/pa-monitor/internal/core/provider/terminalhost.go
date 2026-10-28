package provider

import "time"

// TerminalHost returns the bare terminal-host name (e.g. "tmux", "cmux",
// "ghostty", "unknown") for the session's pid, keyed by session-id with
// WhilePIDAlive freshness. A successful detection is cached for the session's
// life; a transient "unknown" is NOT locked in, but it is re-probed on an
// exponential backoff (UnknownProbeMin doubling to UnknownProbeMax) instead of
// every call, because each probe walks the pid's ancestry with one ps exec per
// ancestor per signaler (pg2-lcohce, spike pg2-57blt). The backoff resets when
// the pid's parent changes (FetchParentPID), when the session id appears with a
// different pid, or when the session reappears (its node was evicted by
// Reconcile). A failed probe is never cached as a host: "" and "unknown" both
// surface as "unknown" and the next probe is due within UnknownProbeMax, so a
// failure can never stick longer than the ceiling. The cmux/bridge refinement
// stays in the poller (in-memory), applied to this bare value. Session-id
// keying is the PID-reuse fix: a reused PID is a new session-id → its own fresh
// detection, never a dead session's value; a dead-not-GC'd session keeps its
// own frozen host. Records the terminal_host metric only on an actual
// detection fetch.
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
	parentOf := c.FetchParentPID
	now := c.now()
	resetBackoff := false
	if node.pid != pid { // same session id, new pid: the old window is void
		node.pid = pid
		resetBackoff = true
	}
	backingOff := !resetBackoff && node.unknownWindow > 0 && now.Before(node.unknownNext)
	seenPPID, seenPPIDOK := node.unknownPPID, node.unknownPPIDOK
	c.mu.Unlock()

	if fetch == nil {
		return "unknown" // bare Cache / unset boundary: never panic, never record
	}

	if backingOff {
		if parentOf == nil || !seenPPIDOK {
			return "unknown"
		}
		// One cheap ppid exec (instead of a multi-hop ancestry walk per
		// signaler): a re-parented pid may now sit under a terminal host.
		if ppid, ok := parentOf(pid); !ok || ppid == seenPPID {
			return "unknown"
		}
		resetBackoff = true
	}

	// Detect without holding c.mu — signalers may spawn ps/tmux/cmux.
	start := c.now()
	host := fetch(pid)
	c.record("terminal_host", start)
	var ppid int
	var ppidOK bool
	if parentOf != nil && (host == "" || host == "unknown") {
		ppid, ppidOK = parentOf(pid)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if host != "" && host != "unknown" {
		node.terminalHost = host
		node.unknownWindow, node.unknownNext, node.unknownPPIDOK = 0, time.Time{}, false
		return host
	}
	node.terminalHost = "unknown" // a failed probe is never cached as a host
	switch {
	case resetBackoff || node.unknownWindow == 0:
		node.unknownWindow = UnknownProbeMin
	default:
		node.unknownWindow = min(node.unknownWindow*2, UnknownProbeMax)
	}
	node.unknownNext = start.Add(node.unknownWindow)
	node.unknownPPID, node.unknownPPIDOK = ppid, ppidOK
	return "unknown"
}
