package changes

import (
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// OriginSweep is the change_log origin of a sweep hydration [design 8.4].
const OriginSweep = "sweep"

// ActiveCount is the number of entities of entityType that are active
// (Inactive is false). status, /metrics and doctor all use it so they report
// the same number.
func ActiveCount(entities []store.Entity, entityType string) int {
	n := 0
	for _, e := range entities {
		if e.EntityType == entityType && !e.Inactive {
			n++
		}
	}
	return n
}

// DueBacklog is the number of ACTIVE entities of entityType the sweep would
// re-hydrate at now: never hydrated (empty or unparseable HydratedAt) or
// hydrated longer than maxAge ago.
func DueBacklog(entities []store.Entity, entityType string, now time.Time, maxAge time.Duration) int {
	n := 0
	for _, e := range entities {
		if e.EntityType == entityType && !e.Inactive && sweepDue(e, now, maxAge) {
			n++
		}
	}
	return n
}

// SweepBoundHolds evaluates the sweep sizing bound of design 8.4:
// activeCount / maxPerPoll x pollInterval <= maxAge, i.e. every active entity
// is re-hydrated within maxAge. A non-positive maxPerPoll holds only for an
// empty active set.
func SweepBoundHolds(activeCount, maxPerPoll int, maxAge, pollInterval time.Duration) (holds bool) {
	if maxPerPoll <= 0 {
		return activeCount <= 0
	}
	return float64(activeCount)/float64(maxPerPoll)*float64(pollInterval) <= float64(maxAge)
}

// hydratedTime parses an entity's hydrated_at; ok is false for "" (never
// hydrated) and for a value that is not RFC3339.
func hydratedTime(e store.Entity) (time.Time, bool) {
	if e.HydratedAt == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, e.HydratedAt)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// sweepDue reports whether an entity's last hydration is older than maxAge
// (or it was never hydrated).
func sweepDue(e store.Entity, now time.Time, maxAge time.Duration) bool {
	t, ok := hydratedTime(e)
	if !ok {
		return true
	}
	return now.Sub(t) > maxAge
}

// selectSweep picks up to limit ACTIVE entities of entityType that are due at
// now, OLDEST FIRST (a never-hydrated entity is oldest of all; ties break by
// entity id), skipping every id in exclude.
func selectSweep(entities []store.Entity, entityType string, now time.Time, maxAge time.Duration, limit int, exclude map[string]bool) []string {
	var due []store.Entity
	for _, e := range entities {
		if e.EntityType != entityType || e.Inactive || exclude[e.EntityID] || !sweepDue(e, now, maxAge) {
			continue
		}
		due = append(due, e)
	}
	sort.Slice(due, func(i, j int) bool {
		ti, _ := hydratedTime(due[i])
		tj, _ := hydratedTime(due[j])
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return due[i].EntityID < due[j].EntityID
	})
	if limit >= 0 && len(due) > limit {
		due = due[:limit]
	}
	ids := make([]string, len(due))
	for i, e := range due {
		ids[i] = e.EntityID
	}
	return ids
}
