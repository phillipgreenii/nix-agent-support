package report

import (
	"sort"
	"sync"
)

var (
	registryMu sync.RWMutex
	generators = map[string]Generator{}
)

// Register makes g available under g.Kind(). A later registration of the same
// kind replaces the earlier one. It panics on a nil generator or an empty
// kind, which are programming errors.
func Register(g Generator) {
	if g == nil || g.Kind() == "" {
		panic("report: Register of a nil generator or empty kind")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	generators[g.Kind()] = g
}

// Lookup returns the generator registered for kind.
func Lookup(kind string) (Generator, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	g, ok := generators[kind]
	return g, ok
}

// Kinds lists the registered kinds in sorted order.
func Kinds() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(generators))
	for k := range generators {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
