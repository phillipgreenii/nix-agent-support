package provider

import "github.com/phillipgreenii/pa-monitor/internal/core/session"

// Env returns the process environment for the session's pid, keyed by
// session-id with WhilePIDAlive freshness. A dead pid yields an empty map with
// NO subprocess spawn (matching today's failed `ps` for a defunct process). The
// returned map is a copy, so callers cannot mutate the cache. Session-id keying
// makes it reuse-safe: a recycled PID is a new session-id → a fresh fetch; a
// dead session's node returns empty via the alive gate regardless. Env has no
// subprocess metric (it never had one).
func (c *Cache) Env(sessionID string, pid int) (map[string]string, error) {
	pidAlive := c.PidAlive
	if pidAlive == nil {
		pidAlive = session.DefaultPidAlive
	}
	fetch := c.FetchEnv
	if fetch == nil {
		fetch = session.ReadProcessEnv
	}

	c.mu.Lock()
	node := c.bySession[sessionID]
	if node == nil {
		node = &sessionNode{pid: pid}
		c.bySession[sessionID] = node
	}
	if !pidAlive(pid) {
		c.mu.Unlock()
		return map[string]string{}, nil
	}
	if node.envFetched {
		out := copyEnv(node.env)
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()

	// Fetch without holding c.mu — ReadProcessEnv may spawn `ps`.
	env, err := fetch(pid)
	if env == nil {
		env = map[string]string{}
	}

	c.mu.Lock()
	node.env = env
	node.envFetched = true
	out := copyEnv(node.env)
	c.mu.Unlock()
	return out, err
}

func copyEnv(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
