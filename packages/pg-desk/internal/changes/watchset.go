package changes

import (
	"encoding/json"
	"sort"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Watched-set membership and the deferred-hydration queue persist in the
// store's meta key/value table (no schema change). pg-connector reports
// membership change PER QUERY and advances its own ledger when it is asked,
// so pg-desk keeps, per (type, watched query), the ids that query currently
// returns: an entity left the watched set only when NO configured query's set
// holds it. The queue holds the added/changed entities a poll could not
// hydrate (budget, degraded backend): pg-connector will not report them
// again, so without it they would be lost.
const (
	metaWatchSetPrefix = "change_flow.watchset."
	metaDeferredPrefix = "change_flow.deferred."
)

// maxDeferredAttempts bounds how many polls a FAILED deferred hydration is
// retried before it is dropped (the failure stays visible through the
// persisted degraded set). Budget deferrals do not count.
const maxDeferredAttempts = 5

// deferredHydration is one queued added/changed hydration.
type deferredHydration struct {
	ID       string            `json:"id"`
	Change   gather.ChangeKind `json:"change"`
	Attempts int               `json:"attempts,omitempty"`
}

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

func loadDeferred(st *store.Store, entityType string) []deferredHydration {
	raw, found, err := st.GetMeta(metaDeferredPrefix + entityType)
	if err != nil || !found {
		return nil
	}
	var out []deferredHydration
	if json.Unmarshal([]byte(raw), &out) != nil {
		return nil
	}
	return out
}

func saveDeferred(st *store.Store, entityType string, q []deferredHydration) error {
	if q == nil {
		q = []deferredHydration{}
	}
	b, err := json.Marshal(q)
	if err != nil {
		return err
	}
	return st.SetMeta(metaDeferredPrefix+entityType, string(b))
}
