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
	PR        *PRInfo   `json:"pr"`        // nil = not found
	FetchedAt time.Time `json:"fetchedAt"`
}

// PRCache is a file-backed write-through cache for PR lookups.
// Found entries never expire. Not-found entries expire after prNotFoundTTL.
type PRCache struct {
	Path     string
	LookupFn func(ctx context.Context, cwd, branch string) (PRInfo, bool, error)
	Now      func() time.Time

	mu      sync.Mutex
	entries map[string]cacheEntry
}

// DefaultPRCachePath returns ${XDG_CACHE_HOME}/claude-agents-tui/pr-cache.json,
// falling back to ~/.cache when XDG_CACHE_HOME is unset.
func DefaultPRCachePath() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "claude-agents-tui", "pr-cache.json")
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

// Get returns the cached PRInfo for (cwd, branch), fetching via LookupFn on miss.
// Returns (nil, nil) when no PR exists for the branch.
func (c *PRCache) Get(ctx context.Context, cwd, branch string) (*PRInfo, error) {
	key := prCacheKey(cwd, branch)

	c.mu.Lock()
	e, ok := c.entries[key]
	if ok && e.PR != nil {
		// Found entry — never expires.
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
	if !alreadySet || (existing.PR == nil && entry.PR != nil) {
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
