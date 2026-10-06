package changes

import (
	"hash/fnv"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// OriginSweep is the change_log origin of a REMOTE-tier sweep hydration
// [design 8.4].
const OriginSweep = "sweep"

// OriginLocalReconcile is the change_log origin of a LOCAL-tier reconcile
// record: re-emitted from the store with no remote call and no hydration.
const OriginLocalReconcile = "local-reconcile"

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

// jitterOffset is the deterministic per-entity offset hash(id) mod (age/5)
// added to an entity's due time, so entities hydrated together (a --reset
// replay, the cutover bootstrap) do not all fall due in the same poll.
// pg-router triggers carry no jitter, so the spreading lives here. FNV-1a over
// the entity id is stable across runs and processes; a non-positive span
// (age under 5ns) yields no offset.
func jitterOffset(id string, age time.Duration) time.Duration {
	span := age / 5
	if span <= 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	return time.Duration(h.Sum64() % uint64(span))
}

// dueCandidate is one entity a tier may select: due is when it falls due
// (base time + age + jitter), zero for an entity with no base time, which is
// due now and ranks oldest of all.
type dueCandidate struct {
	id  string
	due time.Time
}

// pickOldest keeps the candidates due at now, orders them oldest first by due
// time (no-base-time first, ties by id) and cuts the list to limit; the cut
// carries the rest forward, since an unselected entity stays due.
func pickOldest(cands []dueCandidate, now time.Time, limit int) []string {
	var due []dueCandidate
	for _, c := range cands {
		if c.due.IsZero() || now.After(c.due) {
			due = append(due, c)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if !due[i].due.Equal(due[j].due) {
			return due[i].due.Before(due[j].due)
		}
		return due[i].id < due[j].id
	})
	if limit >= 0 && len(due) > limit {
		due = due[:limit]
	}
	ids := make([]string, len(due))
	for i, c := range due {
		ids[i] = c.id
	}
	return ids
}

// remoteDueTime is when an entity's REMOTE re-hydration falls due:
// hydrated_at + maxAge + jitter. The zero time means never hydrated (empty or
// unparseable hydrated_at): due now.
func remoteDueTime(e store.Entity, maxAge time.Duration) time.Time {
	t, ok := hydratedTime(e)
	if !ok {
		return time.Time{}
	}
	return t.Add(maxAge + jitterOffset(e.EntityID, maxAge))
}

// localDueTime is when an entity's LOCAL reconcile falls due: the time of its
// latest change_log row (latest, RFC3339, as returned by
// store.LatestChangeAt) + age + jitter. change_log is pruned and a reset or
// backfilled entity may hold no row (or an unparseable one): it then falls
// back to hydrated_at, and with neither it is due now, oldest first.
func localDueTime(e store.Entity, latest map[string]string, age time.Duration) time.Time {
	if raw, ok := latest[e.EntityID]; ok {
		if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return t.Add(age + jitterOffset(e.EntityID, age))
		}
	}
	if t, ok := hydratedTime(e); ok {
		return t.Add(age + jitterOffset(e.EntityID, age))
	}
	return time.Time{}
}

// selectSweep is the REMOTE tier's selector: up to limit ACTIVE entities of
// entityType whose hydrated_at is older than maxAge plus the entity's jitter,
// OLDEST FIRST by due time (a never-hydrated entity is oldest of all; ties
// break by entity id), skipping every id in exclude.
func selectSweep(entities []store.Entity, entityType string, now time.Time, maxAge time.Duration, limit int, exclude map[string]bool) []string {
	var cands []dueCandidate
	for _, e := range entities {
		if e.EntityType != entityType || e.Inactive || exclude[e.EntityID] {
			continue
		}
		cands = append(cands, dueCandidate{id: e.EntityID, due: remoteDueTime(e, maxAge)})
	}
	return pickOldest(cands, now, limit)
}

// selectReconcile is the LOCAL tier's selector: up to limit ACTIVE entities
// of entityType whose latest change_log row (latest, id -> RFC3339) is older
// than age plus the entity's jitter, oldest first by due time, skipping every
// id in exclude. It reads nothing and calls nothing: the caller re-emits a
// reconcile record for each id it returns.
func selectReconcile(entities []store.Entity, latest map[string]string, entityType string, now time.Time, age time.Duration, limit int, exclude map[string]bool) []string {
	var cands []dueCandidate
	for _, e := range entities {
		if e.EntityType != entityType || e.Inactive || exclude[e.EntityID] {
			continue
		}
		cands = append(cands, dueCandidate{id: e.EntityID, due: localDueTime(e, latest, age)})
	}
	return pickOldest(cands, now, limit)
}
