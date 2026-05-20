package labels

import "sync"

// CardinalityCap enforces a per-key cap on distinct values. Values past
// the cap bucket as "other". Thread-safe.
type CardinalityCap struct {
	limit int
	mu    sync.Mutex
	seen  map[string]map[string]struct{}
}

func NewCardinalityCap(limit int) *CardinalityCap {
	return &CardinalityCap{
		limit: limit,
		seen:  map[string]map[string]struct{}{},
	}
}

// Cap admits values up to the limit per key. Subsequent novel values
// bucket as "other". Already-seen values always pass through.
func (c *CardinalityCap) Cap(key, value string) string {
	if value == "" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	vals, ok := c.seen[key]
	if !ok {
		vals = map[string]struct{}{}
		c.seen[key] = vals
	}
	if _, present := vals[value]; present {
		return value
	}
	if len(vals) < c.limit {
		vals[value] = struct{}{}
		return value
	}
	return "other"
}
