package settle

import (
	"sync"
	"testing"
	"time"
)

// hashCounter counts the full-content hashes a settler computes, per path.
type hashCounter struct {
	mu sync.Mutex
	n  map[string]int
}

func (h *hashCounter) hook(p string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.n == nil {
		h.n = map[string]int{}
	}
	h.n[p]++
}

func (h *hashCounter) snapshot() map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]int{}
	for k, v := range h.n {
		out[k] = v
	}
	return out
}

// A settle hashes each copy, original and clone at most once.
func TestSettleHashesEachFileOnce(t *testing.T) {
	for _, clone := range []bool{true, false} {
		name := "bytes"
		if clone {
			name = "clone"
		}
		t.Run(name, func(t *testing.T) {
			hc := &hashCounter{}
			e := newEnvWith(t, clone, func(e *env) {
				e.extra = func(c *Config) { c.Hooks.OnHash = hc.hook }
			})
			e.seed(4, 48*time.Hour)
			e.tick()
			if e.stage() != StageSettled {
				t.Fatalf("stage %s", e.stage())
			}
			counts := hc.snapshot()
			if len(counts) == 0 {
				t.Fatal("no hash was observed")
			}
			for p, n := range counts {
				t.Logf("hashed %d x %s", n, p)
				if n > 1 {
					t.Errorf("%s hashed %d times in one settle", p, n)
				}
			}
		})
	}
}
