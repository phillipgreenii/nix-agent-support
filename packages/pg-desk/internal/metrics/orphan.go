package metrics

import (
	"fmt"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The watched-entity and orphan series (INV-ICD-17 and INV-ICD-21 item 1). Both
// are derived from the entity table at scrape time, like the sibling outcome
// series, and are registered without _total.
const (
	// MetricWatchedEntities is the count of active entities whose watched
	// column is true, by entity type and backend. The backend label is the
	// entity's backend column, the empty string while that column is NULL (a
	// beads entity pg-desk has not attributed to an instance).
	MetricWatchedEntities = "pg_desk_watched_entities"
	// MetricOldestOrphanReadAge is the age in seconds of the oldest orphan (a
	// watched, active listed-path issue that no watched query holds any longer),
	// counted from its last SUCCESSFUL read: read_ok_at, else hydrated_at, else
	// first_seen_at. A failing orphan therefore keeps ageing. A deployment with
	// no orphan reads one series with the empty backend valued 0, so its absence
	// while pg-desk is up means the store could not answer.
	MetricOldestOrphanReadAge = "pg_desk_oldest_orphan_read_age_seconds"
)

// WatchedEntityCount is one (type, backend) cell of the watched-entity count.
type WatchedEntityCount struct {
	Type    string
	Backend string
	Count   int64
}

// WatchedEntitiesFunc supplies the watched-entity counts at collect time.
type WatchedEntitiesFunc func() ([]WatchedEntityCount, error)

// WatchedEntitiesFromStore counts, at every call, the active entities of st
// whose watched column is true, grouped by type and backend and sorted by both.
// With none watched it returns one issue cell with the empty backend valued 0,
// so the series exists whenever the store answers.
func WatchedEntitiesFromStore(st *store.Store) WatchedEntitiesFunc {
	type key struct{ typ, backend string }
	return func() ([]WatchedEntityCount, error) {
		all, err := st.ListEntities()
		if err != nil {
			return nil, err
		}
		counts := map[key]int64{}
		for _, e := range all {
			if e.Inactive || e.Watched == nil || !*e.Watched {
				continue
			}
			counts[key{e.EntityType, e.Backend}]++
		}
		if len(counts) == 0 {
			return []WatchedEntityCount{{Type: changes.EntityTypeIssue}}, nil
		}
		out := make([]WatchedEntityCount, 0, len(counts))
		for k, n := range counts {
			out = append(out, WatchedEntityCount{Type: k.typ, Backend: k.backend, Count: n})
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Type != out[j].Type {
				return out[i].Type < out[j].Type
			}
			return out[i].Backend < out[j].Backend
		})
		return out, nil
	}
}

// orphanReadTime is the instant an orphan's age counts from: its last
// successful read, else its last hydration, else the time it was first seen.
// ok is false when the entity carries none of them (its age is 0).
func orphanReadTime(e store.Entity) (t time.Time, ok bool, err error) {
	for _, v := range []string{e.ReadOKAt, e.HydratedAt, e.FirstSeenAt} {
		if v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("metrics: orphan %s: parse time %q: %w", e.EntityID, v, err)
		}
		return t, true, nil
	}
	return time.Time{}, false, nil
}

// OldestOrphanReadAgeFromStore reads, per backend label, the age in seconds of
// the oldest orphan (changes.OrphanEntities, the set the poll reads) counted
// from its last successful read. The backend label of an orphan whose backend
// column is NULL is the empty string. With no orphan it returns the single
// empty-label series valued 0. An unparseable time fails the scrape rather than
// reading as 0.
func OldestOrphanReadAgeFromStore(st *store.Store, cfg *config.Config, now func() time.Time) OutcomeFunc {
	return func() ([]OutcomeBackend, error) {
		orphans, err := changes.OrphanEntities(st, cfg)
		if err != nil {
			return nil, err
		}
		if len(orphans) == 0 {
			return emptyOutcome(), nil
		}
		at := now()
		oldest := map[string]int64{}
		for _, e := range orphans {
			var age int64
			t, ok, err := orphanReadTime(e)
			if err != nil {
				return nil, err
			}
			if ok {
				if a := int64(at.Sub(t).Seconds()); a > 0 {
					age = a
				}
			}
			if cur, seen := oldest[e.Backend]; !seen || age > cur {
				oldest[e.Backend] = age
			}
		}
		out := make([]OutcomeBackend, 0, len(oldest))
		for b, v := range oldest {
			out = append(out, OutcomeBackend{Backend: b, Value: v})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Backend < out[j].Backend })
		return out, nil
	}
}
