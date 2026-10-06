package changes

import (
	"encoding/json"
	"sort"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Watched-set membership persists in the store's meta key/value table (no
// schema change). pg-desk keeps, per (type, watched query), the ids that query
// listed at its last complete listing: an entity left the watched set only when
// NO configured query's set holds it. There is no deferral queue: a hydration
// that did not happen is simply found again by the next tick's list diff.
const metaWatchSetPrefix = "change_flow.watchset."

func watchSetKey(entityType, query string) string {
	return metaWatchSetPrefix + entityType + "." + query
}

// loadWatchSet reads the ids query currently returns; a missing or corrupt
// key reads as empty.
func loadWatchSet(st *store.Store, entityType, query string) map[string]bool {
	out := map[string]bool{}
	raw, found, err := st.GetMeta(watchSetKey(entityType, query))
	if err != nil || !found {
		return out
	}
	var ids []string
	if json.Unmarshal([]byte(raw), &ids) != nil {
		return out
	}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func saveWatchSet(st *store.Store, entityType, query string, set map[string]bool) error {
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	b, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return st.SetMeta(watchSetKey(entityType, query), string(b))
}
