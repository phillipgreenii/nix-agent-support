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

// WatchSetIDs returns the sorted ids query listed at its last complete
// listing (the persisted watch set); empty for a query never listed.
func WatchSetIDs(st *store.Store, entityType, query string) []string {
	set := loadWatchSet(st, entityType, query)
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
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

// WatchedIDs returns the ids watched for entityType.
//
// For a non-owning backend it is the union of the ids the configured watch
// queries listed at their last complete listing, read from the persisted
// watch-set meta; known=false when none of those queries has a persisted watch
// set (never listed), so a reader can tell "not watched" from "never
// recorded".
//
// For an owning backend (owning is the set of owning instances; nil or empty
// means none) pg-desk keeps NO per-query membership: the ids are the entities
// of entityType whose backend column is in owning and whose watched column is
// set, read from the entity table. Because the column is always readable,
// known is true whenever owning is non-empty. A query that names an owning
// instance has no persisted set, so passing every configured query is safe.
// It only reads the store.
func WatchedIDs(st *store.Store, queries []string, entityType string, owning map[string]bool) (ids map[string]bool, known bool) {
	ids = map[string]bool{}
	for _, q := range queries {
		raw, found, err := st.GetMeta(watchSetKey(entityType, q))
		if err != nil || !found {
			continue
		}
		var list []string
		if json.Unmarshal([]byte(raw), &list) != nil {
			continue
		}
		known = true
		for _, id := range list {
			ids[id] = true
		}
	}
	if len(owning) > 0 {
		watched, _ := OwningEntityIDs(st, entityType, owning)
		for id := range watched {
			ids[id] = true
		}
		known = true
	}
	return ids, known
}

// OwningEntityIDs reads the entity table for the entities of entityType whose
// backend column is in owning: all is every such id and watched the subset whose
// watched column is set (true). A read failure yields empty sets.
func OwningEntityIDs(st *store.Store, entityType string, owning map[string]bool) (watched, all map[string]bool) {
	watched, all = map[string]bool{}, map[string]bool{}
	if len(owning) == 0 {
		return watched, all
	}
	rows, err := st.ListEntities()
	if err != nil {
		return watched, all
	}
	for _, e := range rows {
		if e.EntityType != entityType || e.Backend == "" || !owning[e.Backend] {
			continue
		}
		all[e.EntityID] = true
		if e.Watched != nil && *e.Watched {
			watched[e.EntityID] = true
		}
	}
	return watched, all
}
