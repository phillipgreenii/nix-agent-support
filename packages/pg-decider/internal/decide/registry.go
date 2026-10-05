package decide

import (
	"fmt"
	"sort"
	"sync"
)

type entry struct {
	ordinal int
	rule    Rule
}

var (
	mu       sync.RWMutex
	registry = map[string][]entry{}
)

// Register adds r to entityType's rule set at ordinal. Rules run in ascending
// ordinal order; equal ordinals keep registration order. It is meant for
// init() and panics on a nil rule, an empty rule id, or a rule id already
// registered for the same entity type (a programmer error, never data).
func Register(entityType string, ordinal int, r Rule) {
	if r == nil {
		panic(fmt.Sprintf("decide: Register(%q): nil rule", entityType))
	}
	id := r.ID()
	if id == "" {
		panic(fmt.Sprintf("decide: Register(%q): rule with empty id", entityType))
	}
	mu.Lock()
	defer mu.Unlock()
	for _, e := range registry[entityType] {
		if e.rule.ID() == id {
			panic(fmt.Sprintf("decide: Register(%q): duplicate rule id %q", entityType, id))
		}
	}
	es := append(registry[entityType], entry{ordinal: ordinal, rule: r})
	sort.SliceStable(es, func(i, j int) bool { return es[i].ordinal < es[j].ordinal })
	registry[entityType] = es
}

// RulesFor returns entityType's rules sorted by ordinal. The slice is a copy.
func RulesFor(entityType string) []Rule {
	mu.RLock()
	defer mu.RUnlock()
	es := registry[entityType]
	out := make([]Rule, len(es))
	for i, e := range es {
		out[i] = e.rule
	}
	return out
}

// HasDecider reports whether entityType has a decider: the PR decider always
// exists (even before any rule registers, so an empty plan is a valid plan);
// any other type has one only once a rule registers for it.
func HasDecider(entityType string) bool {
	if entityType == EntityTypePR {
		return true
	}
	mu.RLock()
	defer mu.RUnlock()
	return len(registry[entityType]) > 0
}
