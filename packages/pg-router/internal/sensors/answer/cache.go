package answer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Cache keeps the last answer of each resource. A sensor MAY answer from the
// cache (SEN-2): freshness is judged by the answer's observed_at, never by when
// the router asked, so an answer is served only while observed_at + maxAge is
// still in the future.
type Cache struct {
	path    string
	answers map[string]Answer
}

// NewCache returns an empty cache backed by path ("" keeps it in memory only).
func NewCache(path string) *Cache {
	return &Cache{path: path, answers: map[string]Answer{}}
}

// OpenCache reads the cache file; a missing or malformed file is an empty
// cache (the cache is an optimisation, never a source of truth).
func OpenCache(path string) *Cache {
	c := NewCache(path)
	raw, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	var m map[string]Answer
	if json.Unmarshal(raw, &m) == nil && m != nil {
		c.answers = m
	}
	return c
}

// Put stores an answer, replacing an older one for the same resource. An answer
// observed earlier than the stored one is ignored.
func (c *Cache) Put(a Answer) {
	if old, ok := c.answers[a.Resource]; ok && a.ObservedAt.Before(old.ObservedAt) {
		return
	}
	c.answers[a.Resource] = a
}

// Get returns the cached answer of a resource when it is still fresh at now:
// observed_at + maxAge is after now.
func (c *Cache) Get(resource string, now time.Time, maxAge time.Duration) (Answer, bool) {
	a, ok := c.answers[resource]
	if !ok || !a.ObservedAt.Add(maxAge).After(now) {
		return Answer{}, false
	}
	return a, true
}

// Save writes the cache atomically; an in-memory cache is a no-op.
func (c *Cache) Save() error {
	if c.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(c.answers)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".cache-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, c.path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
