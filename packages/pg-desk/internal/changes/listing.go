package changes

import (
	"encoding/json"
	"sort"
	"strings"

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
