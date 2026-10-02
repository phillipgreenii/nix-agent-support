package classify

import (
	"bytes"
	"fmt"
	"sort"
	"sync"
)

var (
	registryMu sync.RWMutex
	registry   = map[string]Classifier{}
)

// Register installs the Classifier for an entity type. It panics on a
// duplicate entity type (like database/sql.Register); per-type packets call
// it from an init() in their own file.
func Register(entityType string, c Classifier) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[entityType]; dup {
		panic(fmt.Sprintf("classify: Register called twice for entity type %q", entityType))
	}
	registry[entityType] = c
}

// Lookup returns the Classifier registered for an entity type.
func Lookup(entityType string) (Classifier, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	c, ok := registry[entityType]
	return c, ok
}

// Classify applies the invariants common to all entity types, then delegates
// to the per-type classifier. Evaluation order:
//
//  1. no previous snapshot, or a previous snapshot of an inactive entity:
//     exactly [reconcile] (before the identical rule, so --reset, a new
//     watched query and the cutover wave are never swallowed);
//  2. identical snapshots (byte-equal Payload and Decorations): nothing;
//  3. dispatch on new.Type (an unregistered type yields nothing). new.Exists
//     and new.Active are ignored: the caller builds new as an observed,
//     active snapshot.
//
// A degraded new snapshot never yields removed or closed. The result is
// sorted by kind and free of duplicates.
func Classify(old, new Snapshot) []Record {
	if !old.Exists || !old.Active {
		return []Record{{Kind: KindReconcile}}
	}
	if identical(old, new) {
		return nil
	}
	c, ok := Lookup(new.Type)
	if !ok {
		return nil
	}
	return normalize(c.Classify(old, new), new.Degraded)
}

func identical(old, new Snapshot) bool {
	return bytes.Equal(old.Payload, new.Payload) && bytes.Equal(old.Decorations, new.Decorations)
}

// normalize drops removed/closed when degraded, de-duplicates by kind and
// sorts by kind string. It never mutates its input.
func normalize(in []Record, degraded bool) []Record {
	seen := make(map[Kind]struct{}, len(in))
	var out []Record
	for _, r := range in {
		if degraded && (r.Kind == KindRemoved || r.Kind == KindClosed) {
			continue
		}
		if _, dup := seen[r.Kind]; dup {
			continue
		}
		seen[r.Kind] = struct{}{}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}
