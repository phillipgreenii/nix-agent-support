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
	// owning watched queries, or, for a beads instance, of its queries'
	// change_flow.listing_ok.<query> times and its orphans' last successful
	// read (0 while none has been recorded yet).
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

// ListedQueriesByInstance maps every beads (listed, non-owning) backend
// instance named by watch.issue.backends to its watched queries, in watch
// order. A non-owning query with no backends entry names no instance and is
// left out.
func ListedQueriesByInstance(cfg *config.Config) map[string][]string {
	out := map[string][]string{}
	for _, q := range cfg.NonOwningWatchQueries(changes.EntityTypeIssue) {
		b, ok := cfg.WatchQueryBackend(changes.EntityTypeIssue, q)
		if !ok || b.Instance == "" {
			continue
		}
		out[b.Instance] = append(out[b.Instance], q)
	}
	return out
}

// CombineOutcomes concatenates the series of several readers. The backendless
// placeholder of a reader with nothing to report is dropped when another
// reader has a real series (and kept, once, when none has); two readers that
// report the same backend keep the larger value.
func CombineOutcomes(fns ...OutcomeFunc) OutcomeFunc {
	return func() ([]OutcomeBackend, error) {
		best := map[string]int64{}
		for _, fn := range fns {
			vals, err := fn()
			if err != nil {
				return nil, err
			}
			for _, v := range vals {
				if v.Backend == "" {
					continue
				}
				if cur, seen := best[v.Backend]; !seen || v.Value > cur {
					best[v.Backend] = v.Value
				}
			}
		}
		if len(best) == 0 {
			return emptyOutcome(), nil
		}
		out := make([]OutcomeBackend, 0, len(best))
		for b, v := range best {
			out = append(out, OutcomeBackend{Backend: b, Value: v})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Backend < out[j].Backend })
		return out, nil
	}
}

// ageSince is the whole seconds from t to at, never negative.
func ageSince(at, t time.Time) int64 {
	if a := int64(at.Sub(t).Seconds()); a > 0 {
		return a
	}
	return 0
}

// WatchedListedSourceObservedFromStore reads, per beads instance, the age of
// the OLDEST successful observation of the source of a watched open issue: the
// change_flow.listing_ok.<query> time of each of the instance's queries (the
// last successful complete listing; a query that never listed successfully has
// no key and is not counted) and the last successful read of each of its
// orphans (read_ok_at, else hydrated_at, else first_seen_at, as the orphan-lag
// series counts it). An instance with no value yet reads 0, so a fresh
// deployment never pages. An unparseable value fails the scrape.
func WatchedListedSourceObservedFromStore(st *store.Store, cfg *config.Config, now func() time.Time) OutcomeFunc {
	return func() ([]OutcomeBackend, error) {
		by := ListedQueriesByInstance(cfg)
		if len(by) == 0 {
			return emptyOutcome(), nil
		}
		orphans, err := changes.OrphanEntities(st, cfg)
		if err != nil {
			return nil, err
		}
		at := now()
		var out []OutcomeBackend
		for _, inst := range sortedInstances(by) {
			var oldest time.Time
			have := false
			note := func(t time.Time) {
				if !have || t.Before(oldest) {
					oldest, have = t, true
				}
			}
			for _, q := range by[inst] {
				v, found, err := st.GetMeta(changes.MetaListingOKPrefix + q)
				if err != nil {
					return nil, err
				}
				if !found {
					continue
				}
				t, err := time.Parse(time.RFC3339Nano, v)
				if err != nil {
					return nil, fmt.Errorf("metrics: %s%s: parse %q: %w", changes.MetaListingOKPrefix, q, v, err)
				}
				note(t)
			}
			for _, o := range orphans {
				if o.Backend != inst {
					continue
				}
				t, ok, err := orphanReadTime(o)
				if err != nil {
					return nil, err
				}
				if ok {
					note(t)
				}
			}
			var age int64
			if have {
				age = ageSince(at, oldest)
			}
			out = append(out, OutcomeBackend{Backend: inst, Value: age})
		}
		return out, nil
	}
}

// WatchedSourceObservedForConfig is the whole observed-age series of a
// deployment: the owning instances' feed observations plus the beads
// instances' listings and orphan reads.
func WatchedSourceObservedForConfig(st *store.Store, cfg *config.Config, now func() time.Time) OutcomeFunc {
	return CombineOutcomes(
		WatchedSourceObservedFromStore(st, OwningQueriesByInstance(cfg), now),
		WatchedListedSourceObservedFromStore(st, cfg, now),
	)
}

// UnflaggedMembersForConfig is the whole unflagged-member series of a
// deployment: the stored count of every owning and every beads instance (both
// halves write change_flow.unflagged.<instance>).
func UnflaggedMembersForConfig(st *store.Store, cfg *config.Config) OutcomeFunc {
	return CombineOutcomes(
		UnflaggedMembersFromStore(st, OwningQueriesByInstance(cfg)),
		UnflaggedMembersFromStore(st, ListedQueriesByInstance(cfg)),
	)
}
