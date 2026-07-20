package provider

import (
	"context"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// PR returns the PR for (cwd, branch) via the wired PRBackend (the bounded
// file-backed session.PRCache). It records the (cwd,branch) key as live this
// scan so Reconcile can prune vanished keys. The pr_lookup subprocess metric is
// NOT recorded here: it fires inside the PRCache.LookupFn timing wrapper
// (buildPoller), the only layer that knows a cache hit from a real gh spawn — so
// the metric fires only on an actual spawn (single cache, no double-caching). A
// nil PRBackend (bare/test poller) yields no PR, matching the old nil-PRLookupFn
// behavior.
func (c *Cache) PR(ctx context.Context, cwd, branch string) (*session.PRInfo, error) {
	backend := c.PRBackend
	if backend == nil {
		return nil, nil
	}
	c.mu.Lock()
	c.prLiveKeys[session.PRCacheKey(cwd, branch)] = true
	c.mu.Unlock()
	return backend(ctx, cwd, branch)
}
