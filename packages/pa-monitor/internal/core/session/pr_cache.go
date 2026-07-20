package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const prNotFoundTTL = 5 * time.Minute

type cacheEntry struct {
	PR        *PRInfo   `json:"pr"` // nil = not found
	FetchedAt time.Time `json:"fetchedAt"`
}

// PRCache is a file-backed write-through cache for PR lookups.
// Found entries re-validate after FoundTTL (0 = never expire). Not-found
// entries expire after prNotFoundTTL.
type PRCache struct {
	Path     string
	LookupFn func(ctx context.Context, cwd, branch string) (PRInfo, bool, error)
	Now      func() time.Time
	// FoundTTL bounds how long a FOUND entry is served before re-validating via
	// LookupFn. 0 preserves the legacy never-expire behavior. Set >0 (e.g. 15m)
	// to catch a PR that is closed/merged/renumbered during a long session and to
	// keep the cache from pinning stale PR data forever.
	FoundTTL time.Duration

	mu      sync.Mutex
	entries map[string]cacheEntry
}

// DefaultPRCachePath returns ${XDG_CACHE_HOME}/pa-monitor/pr-cache.json,
// falling back to ~/.cache when XDG_CACHE_HOME is unset.
func DefaultPRCachePath() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "pa-monitor", "pr-cache.json")
}

// NewPRCache creates a PRCache, loading any existing file. Missing file is not an error.
func NewPRCache(path string) *PRCache {
	c := &PRCache{
		Path:     path,
		LookupFn: LookupPR,
		Now:      time.Now,
		entries:  map[string]cacheEntry{},
	}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &c.entries)
	}
	return c
}

func prCacheKey(cwd, branch string) string {
	return cwd + "\x00" + branch
}

// PRCacheKey returns the cache key for a (cwd, branch) pair. Exported so callers
// (e.g. the provider cache) can build the live-key set passed to Prune.
func PRCacheKey(cwd, branch string) string {
	return prCacheKey(cwd, branch)
}

// Prune drops every entry whose key is not in live, bounding the cache to the
// currently-active (cwd,branch) pairs. Persists the pruned map only when
// something was removed. This is the growth bound for the otherwise-unbounded
// cache; combined with FoundTTL it fixes the never-expire/never-shrink bug.
func (c *PRCache) Prune(live map[string]bool) {
	c.mu.Lock()
	changed := false
	for k := range c.entries {
		if !live[k] {
			delete(c.entries, k)
			changed = true
		}
	}
	if !changed {
		c.mu.Unlock()
		return
	}
	data, _ := json.Marshal(c.entries)
	c.mu.Unlock()
	c.writeFile(data)
}

// Get returns the cached PRInfo for (cwd, branch), fetching via LookupFn on miss.
// Returns (nil, nil) when no PR exists for the branch.
func (c *PRCache) Get(ctx context.Context, cwd, branch string) (*PRInfo, error) {
	key := prCacheKey(cwd, branch)

	c.mu.Lock()
	e, ok := c.entries[key]
	if ok && e.PR != nil && (c.FoundTTL == 0 || c.Now().Sub(e.FetchedAt) < c.FoundTTL) {
		// Found entry within its TTL (FoundTTL == 0 → never expires).
		c.mu.Unlock()
		return e.PR, nil
	}
	if ok && c.Now().Sub(e.FetchedAt) < prNotFoundTTL {
		// Not-found entry within TTL.
		c.mu.Unlock()
		return nil, nil
	}
	c.mu.Unlock()

	// Fetch without holding the lock.
	info, found, err := c.LookupFn(ctx, cwd, branch)
	if err != nil {
		return nil, err
	}

	now := c.Now() // capture after fetch completes
	entry := cacheEntry{FetchedAt: now}
	if found {
		entry.PR = &info
	}

	c.mu.Lock()
	existing, alreadySet := c.entries[key]
	// Overwrite when: no entry yet; a not-found is upgraded to found; or a found
	// entry has aged past FoundTTL (refresh it — otherwise an expired found entry
	// would re-spawn gh every tick forever, never storing the fresh result).
	staleFound := existing.PR != nil && c.FoundTTL > 0 && now.Sub(existing.FetchedAt) >= c.FoundTTL
	if !alreadySet || (existing.PR == nil && entry.PR != nil) || staleFound {
		c.entries[key] = entry
		data, _ := json.Marshal(c.entries)
		c.mu.Unlock()
		c.writeFile(data)
	} else {
		c.mu.Unlock()
	}

	return entry.PR, nil
}

// writeFile writes serialized cache data to disk. Must NOT be called with mu held.
func (c *PRCache) writeFile(data []byte) {
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(c.Path, data, 0o644)
}
