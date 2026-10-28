package metrics

import (
	"fmt"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The outcome series of an owning backend (INV-ICD-21 items 2 and 3). Both
// are derived from the store at scrape time and carry the label backend, the
// owning instance name.
const (
	// MetricWatchedSourceObservedAge is the age in seconds of the OLDEST
	// change_flow.feed_observed.<instance>.<query> value across the instance's
	// owning watched queries (0 while none has been recorded yet).
	MetricWatchedSourceObservedAge = "pg_desk_watched_source_observed_age_seconds"
	// MetricIssueUnflaggedMembers is the stored count of members of the
	// instance's watched queries whose pg-desk entity is not flagged watched
	// (change_flow.unflagged.<instance>; 0 until the first poll stores one).
	MetricIssueUnflaggedMembers = "pg_desk_issue_unflagged_members"
)

// OutcomeBackend is one owning instance's value of an outcome series.
type OutcomeBackend struct {
	Backend string
	Value   int64
}

// OutcomeFunc supplies one outcome series' values at collect time.
type OutcomeFunc func() ([]OutcomeBackend, error)

// OwningQueriesByInstance maps every owning backend instance of the issue
// type to its owning watched queries, in watch order.
func OwningQueriesByInstance(cfg *config.Config) map[string][]string {
	out := map[string][]string{}
	for _, q := range cfg.OwningWatchQueries(changes.EntityTypeIssue) {
		b, _ := cfg.WatchQueryBackend(changes.EntityTypeIssue, q)
		out[b.Instance] = append(out[b.Instance], q)
	}
	return out
}

func sortedInstances(by map[string][]string) []string {
	out := make([]string, 0, len(by))
	for inst := range by {
		out = append(out, inst)
	}
	sort.Strings(out)
	return out
}

// emptyOutcome is the one backendless series a deployment with no owning
// instance emits, valued 0: the series exists whenever pg-desk serves, so its
// absence always means the store could not answer (the rules use noDataState
// Alerting).
func emptyOutcome() []OutcomeBackend { return []OutcomeBackend{{}} }

// WatchedSourceObservedFromStore reads, per owning instance, the age of the
// oldest feed_observed value across its queries. An instance with no value yet
// reads 0 (a fresh deployment never pages). An unparseable value fails the
// scrape rather than reading as 0.
func WatchedSourceObservedFromStore(st *store.Store, queriesByInstance map[string][]string, now func() time.Time) OutcomeFunc {
	return func() ([]OutcomeBackend, error) {
		if len(queriesByInstance) == 0 {
			return emptyOutcome(), nil
		}
		at := now()
		var out []OutcomeBackend
		for _, inst := range sortedInstances(queriesByInstance) {
			var oldest time.Time
			have := false
			for _, q := range queriesByInstance[inst] {
				v, found, err := st.GetMeta(changes.MetaFeedObservedPrefix + inst + "." + q)
				if err != nil {
					return nil, err
				}
				if !found {
					continue
				}
				t, err := time.Parse(time.RFC3339Nano, v)
				if err != nil {
					return nil, fmt.Errorf("metrics: %s%s.%s: parse %q: %w", changes.MetaFeedObservedPrefix, inst, q, v, err)
				}
				if !have || t.Before(oldest) {
					oldest, have = t, true
				}
			}
			var age int64
			if have {
				if a := int64(at.Sub(oldest).Seconds()); a > 0 {
					age = a
				}
			}
			out = append(out, OutcomeBackend{Backend: inst, Value: age})
		}
		return out, nil
	}
}

// UnflaggedMembersFromStore reads, per owning instance, the stored unflagged
// count (0 when none has been stored).
func UnflaggedMembersFromStore(st *store.Store, queriesByInstance map[string][]string) OutcomeFunc {
	return func() ([]OutcomeBackend, error) {
		if len(queriesByInstance) == 0 {
			return emptyOutcome(), nil
		}
		var out []OutcomeBackend
		for _, inst := range sortedInstances(queriesByInstance) {
			rec, _, err := changes.ReadUnflagged(st, inst)
			if err != nil {
				return nil, err
			}
			out = append(out, OutcomeBackend{Backend: inst, Value: int64(rec.Count)})
		}
		return out, nil
	}
}
