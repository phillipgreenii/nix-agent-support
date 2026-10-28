package changes

import (
	"hash/fnv"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// OriginSweep is the change_log origin of a REMOTE-tier sweep hydration
// [design 8.4].
const OriginSweep = "sweep"

// OriginLocalReconcile is the change_log origin of a LOCAL-tier reconcile
// record: re-emitted from the store with no remote call and no hydration.
const OriginLocalReconcile = "local-reconcile"

// EntityTypeIssue is the issue entity type, the only type with an owning
// backend.
const EntityTypeIssue = "issue"

// OwningSet returns the owning backend instances of entityType as a set (nil
// when none, which every consumer treats as "no entity is owned").
func OwningSet(cfg *config.Config, entityType string) map[string]bool {
	if cfg == nil {
		return nil
	}
	insts := cfg.OwningInstances(entityType)
	if len(insts) == 0 {
		return nil
	}
	out := make(map[string]bool, len(insts))
	for _, inst := range insts {
		out[inst] = true
	}
	return out
}

// OwnedByBackend reports whether e is an issue entity whose backend column is
// one of the owning instances: an entity no age-driven count includes. A NULL
// backend ("") is never owned here.
func OwnedByBackend(e store.Entity, entityType string, owning map[string]bool) bool {
	return entityType == EntityTypeIssue && e.Backend != "" && owning[e.Backend]
}

// ActiveCount is the number of entities of entityType that are active
// (Inactive is false). status, /metrics and doctor all use it so they report
// the same number.
//
// An entity of the issue type whose backend column is an owning instance is
// not counted (see OwnedByBackend): the age-driven sweep never reaches it.
func ActiveCount(entities []store.Entity, entityType string, owning map[string]bool) int {
	n := 0
	for _, e := range entities {
		if e.EntityType == entityType && !e.Inactive && !OwnedByBackend(e, entityType, owning) {
			n++
		}
	}
	return n
}

// EntityTypePR is the pr entity type, whose rows no sweep re-hydrates (see
// DueBacklog).
const EntityTypePR = "pr"

// DueBacklog is the number of ACTIVE entities of entityType the sweep would
// re-hydrate at now: never hydrated (empty or unparseable HydratedAt) or
// hydrated longer than maxAge ago.
//
// The pr type is always 0 (PR lane refactor, packet P3b2): the router sweeps
// that re-listed every open pull request are gone, so a quiet PR is never
// re-hydrated on a clock, and counting it as due would age every quiet PR past
// maxAge and mark the daily focus draft incomplete for a backlog nothing is
// going to work off. A PR that was never hydrated is reported by its own
// gauge (the unhydrated count), not by this one.
//
// An owning-backend issue entity is never counted: the old tiers skip it, so
// its hydrated_at never advances and it would otherwise be due for ever.
func DueBacklog(entities []store.Entity, entityType string, now time.Time, maxAge time.Duration, owning map[string]bool) int {
	if entityType == EntityTypePR {
		return 0
	}
	n := 0
	for _, e := range entities {
		if e.EntityType == entityType && !e.Inactive && !OwnedByBackend(e, entityType, owning) && sweepDue(e, now, maxAge) {
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

// BoundVerdict is the outcome of evaluating the sweep sizing bound for one
// (type, tier). BoundUnknown means the poll interval is not known, so there is
// NO verdict: callers MUST NOT treat it as a violation.
type BoundVerdict int

const (
	// BoundUnknown: the poll interval is unknown (pg-desk does not own it).
	BoundUnknown BoundVerdict = iota
	// BoundHolds: active_count / max_per_poll x poll_interval <= max_age.
	BoundHolds
	// BoundViolated: the sweep falls behind its tier's max age.
	BoundViolated
)

// SweepTiers lists the capped age tiers whose bound is checked: the local
// reconcile tier and the remote re-hydration tier.
var SweepTiers = []string{TierLocal, TierRemote}

// EvaluateSweepBound is the ONE evaluation of the sizing bound for a tier,
// shared by `doctor` and the /metrics sweep-bound gauge so the doctor line and
// the alert always agree. known=false (the poll interval is not supplied)
// yields BoundUnknown, never a violation.
func EvaluateSweepBound(in SweepInputs, pollInterval time.Duration, known bool) BoundVerdict {
	if !known {
		return BoundUnknown
	}
	if SweepBoundHolds(in.ActiveCount, in.MaxPerPoll, in.MaxAge, pollInterval) {
		return BoundHolds
	}
	return BoundViolated
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
