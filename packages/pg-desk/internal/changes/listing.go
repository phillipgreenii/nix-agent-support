package changes

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The per-query listing status persists in the store's meta key/value table
// (no schema change) because read-only consumers (focus show's coverage
// header, /metrics, doctor) cannot call a tracker to learn whether a watched
// query failed or was truncated. Every non-cached changes call rewrites the
// key of each query it consulted with the envelope's own sources[] status and
// reason; a --cached call and a query the call did not consult write nothing.
// The write is best-effort, like the other change_flow.* recorders: a meta
// write error never fails the call.
//
// Telemetry (D24): the write emits no OpenTelemetry or Prometheus output and
// logs only a stderr warning when the meta write fails.
const metaListingPrefix = "change_flow.listing."

// MetaListingOKPrefix prefixes change_flow.listing_ok.<query>: the RFC3339 UTC
// time of the last successful COMPLETE listing of a watched listed (beads)
// query. The stored listing value above is overwritten at every call, failures
// included, so it cannot say when a query was last observed; this key is
// written only by a listing that succeeded in full (no failure, no degraded
// backend, nothing truncated) and is what the beads half of
// pg_desk_watched_source_observed_age_seconds reads. A query whose listing
// never succeeded has no key.
const MetaListingOKPrefix = "change_flow.listing_ok."

// recordListingOK stamps change_flow.listing_ok.<query> for every listed query
// of an issue call whose listing was complete (the standard that lets a
// listing shrink a watched set, see listComplete). A failed, degraded or
// truncated listing leaves the key untouched. Best-effort: a write error is a
// warning, never a failed call.
func (e *Engine) recordListingOK(set func(key, value string) error, entityType string, runs []queryRun) {
	if entityType != issueEntityType {
		return
	}
	at := e.now().UTC().Format(time.RFC3339)
	for _, r := range runs {
		if r.err != nil || !listComplete(r.res) {
			continue
		}
		if err := set(MetaListingOKPrefix+r.query, at); err != nil {
			e.warnf("changes: persist listing_ok %s: %v\n", r.query, err)
		}
	}
}

func listingKey(entityType, query string) string {
	return metaListingPrefix + entityType + "." + query
}

// ListingStatus is one watched query's last persisted listing status.
type ListingStatus struct {
	Query string
	// Status is StatusOK, StatusDegraded or StatusFailed.
	Status string
	// Reason is the call's envelope sources[] reason text ("" for ok).
	Reason string
	// At is the RFC3339 time of the call that wrote it.
	At string
}

// listingValue is the JSON stored under a listing key.
type listingValue struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
	At     string `json:"at"`
}

// recordListing writes one entry per source via set (meta SetMeta). It stops
// at, and returns, the first write error; the caller treats it as a warning.
func recordListing(set func(key, value string) error, entityType, at string, sources []Source) error {
	for _, s := range sources {
		b, err := json.Marshal(listingValue{Status: s.Status, Reason: s.Reason, At: at})
		if err != nil {
			return err
		}
		if err := set(listingKey(entityType, s.Query), string(b)); err != nil {
			return err
		}
	}
	return nil
}

// ReadListingStatuses returns every persisted listing status of entityType
// sorted by query. A missing key reads as no entry; an undecodable value (or
// one with an empty status) is skipped, never an error, so the caller can
// count the query as unknown.
func ReadListingStatuses(st *store.Store, entityType string) ([]ListingStatus, error) {
	prefix := listingKey(entityType, "")
	rows, err := st.ListMetaPrefix(prefix)
	if err != nil {
		return nil, err
	}
	out := make([]ListingStatus, 0, len(rows))
	for k, raw := range rows {
		var v listingValue
		if json.Unmarshal([]byte(raw), &v) != nil || v.Status == "" {
			continue
		}
		out = append(out, ListingStatus{Query: strings.TrimPrefix(k, prefix), Status: v.Status, Reason: v.Reason, At: v.At})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Query < out[j].Query })
	return out, nil
}

// FlowWired reports whether a change flow is wired for entityType: some
// consumer of that type is registered in the store. Every real
// `pg-desk <type> changes` call registers its consumer before it lists, and
// every cursor read does too, so a type with a consumer has run (or is about
// to run) a flow that records listing statuses, and a type with none has no
// flow at all (a daemon-owned feed that does not go through pg-desk has no
// consumer here either). It is the one signal readers use to tell "not yet
// watched" from "watched, but no status recorded". An unreadable consumer
// table is an error; the caller decides which way to fail.
func FlowWired(st *store.Store, entityType string) (bool, error) {
	consumers, err := st.ListConsumers()
	if err != nil {
		return false, err
	}
	for _, c := range consumers {
		if c.Type == entityType {
			return true, nil
		}
	}
	return false, nil
}
