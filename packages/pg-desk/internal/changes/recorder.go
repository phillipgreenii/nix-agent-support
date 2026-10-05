package changes

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The hydration recorder persists best-effort running totals in the store's
// meta key/value table (no schema change) because serve, status and doctor
// run in other processes than the verbs that hydrate. A lost concurrent
// update is acceptable: the counters are observability, not state.
const (
	metaHydrationsPrefix        = "change_flow.hydrations."
	metaHydrationFailuresPrefix = "change_flow.hydration_failures."
	metaOCCRetriesPrefix        = "change_flow.occ_retries."
	metaDegradedPrefix          = "change_flow.degraded."
)

// RepeatedDegradedThreshold is the number of consecutive degraded or failed
// hydrations of one entity at which it counts as "repeatedly degraded".
const RepeatedDegradedThreshold = 2

// DegradedEntity is one entity's run of consecutive degraded or failed
// hydrations: how many, and when the first happened (RFC3339).
type DegradedEntity struct {
	Count int    `json:"count"`
	Since string `json:"since"`
}

// HydrationStats are one entity type's persisted hydration totals.
type HydrationStats struct {
	// Hydrations counts every hydration call.
	Hydrations int64
	// Failures counts calls that returned an error or a degraded result.
	Failures int64
	// OCCRetries is the sum of lost-race retries over all calls.
	OCCRetries int64
	// Degraded maps entity id to its current degraded run; an entity leaves
	// it on its next successful hydration. Never nil.
	Degraded map[string]DegradedEntity
}

// recorderNow is the clock the degraded-run "since" stamp uses; tests
// replace it.
var recorderNow = func() time.Time { return time.Now().UTC() }

// RecordHydration records the outcome of one RunEntityChange call for
// entityID: it bumps the per-type hydration, failure and OCC-retry totals and
// maintains the per-entity degraded set (an entry grows on every failed or
// degraded call, and is removed on the next successful one). A call failed
// when err != nil or res.Degraded != "". It is best-effort and never fails
// the caller: a meta read or write error is dropped.
func RecordHydration(st *store.Store, entityType, entityID string, res pipeline.EntityChangeResult, err error) {
	failed := err != nil || res.Degraded != ""
	addInt(st, metaHydrationsPrefix+entityType, 1)
	if failed {
		addInt(st, metaHydrationFailuresPrefix+entityType, 1)
	}
	if res.Retries > 0 {
		addInt(st, metaOCCRetriesPrefix+entityType, int64(res.Retries))
	}

	key := metaDegradedPrefix + entityType
	degraded := readDegraded(st, key)
	if failed {
		e := degraded[entityID]
		if e.Count == 0 {
			e.Since = recorderNow().UTC().Format(time.RFC3339)
		}
		e.Count++
		degraded[entityID] = e
	} else if _, ok := degraded[entityID]; ok {
		delete(degraded, entityID)
	} else {
		return // nothing to write: a clean hydration of a clean entity
	}
	if b, merr := json.Marshal(degraded); merr == nil {
		_ = st.SetMeta(key, string(b))
	}
}

// ReadHydrationStats reads one entity type's persisted totals; a missing key
// reads as zero (or an empty Degraded map).
func ReadHydrationStats(st *store.Store, entityType string) (HydrationStats, error) {
	var out HydrationStats
	var err error
	if out.Hydrations, err = readInt(st, metaHydrationsPrefix+entityType); err != nil {
		return HydrationStats{}, err
	}
	if out.Failures, err = readInt(st, metaHydrationFailuresPrefix+entityType); err != nil {
		return HydrationStats{}, err
	}
	if out.OCCRetries, err = readInt(st, metaOCCRetriesPrefix+entityType); err != nil {
		return HydrationStats{}, err
	}
	raw, found, err := st.GetMeta(metaDegradedPrefix + entityType)
	if err != nil {
		return HydrationStats{}, err
	}
	out.Degraded = map[string]DegradedEntity{}
	if found {
		if jerr := json.Unmarshal([]byte(raw), &out.Degraded); jerr != nil || out.Degraded == nil {
			out.Degraded = map[string]DegradedEntity{}
		}
	}
	return out, nil
}

// RepeatedDegraded returns the entities with Count >= RepeatedDegradedThreshold:
// the ONE definition of "repeated degraded hydrations" that /metrics, status
// and doctor all use.
func RepeatedDegraded(stats HydrationStats) map[string]DegradedEntity {
	out := map[string]DegradedEntity{}
	for id, e := range stats.Degraded {
		if e.Count >= RepeatedDegradedThreshold {
			out[id] = e
		}
	}
	return out
}

func readInt(st *store.Store, key string) (int64, error) {
	v, found, err := st.GetMeta(key)
	if err != nil || !found {
		return 0, err
	}
	n, perr := strconv.ParseInt(v, 10, 64)
	if perr != nil {
		return 0, nil // a corrupt counter reads as zero rather than failing status
	}
	return n, nil
}

func addInt(st *store.Store, key string, delta int64) {
	n, err := readInt(st, key)
	if err != nil {
		return
	}
	_ = st.SetMeta(key, strconv.FormatInt(n+delta, 10))
}

func readDegraded(st *store.Store, key string) map[string]DegradedEntity {
	out := map[string]DegradedEntity{}
	if raw, found, err := st.GetMeta(key); err == nil && found {
		if jerr := json.Unmarshal([]byte(raw), &out); jerr != nil || out == nil {
			out = map[string]DegradedEntity{}
		}
	}
	return out
}
