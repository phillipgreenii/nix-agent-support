package changes

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

var sweepTestNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func sweepEntities() []store.Entity {
	return []store.Entity{
		{EntityType: "pr", EntityID: "fresh", HydratedAt: "2026-10-01T11:00:00Z"},
		// Days apart, so the per-entity jitter (under 72m at max_age 6h) can
		// never reorder them.
		{EntityType: "pr", EntityID: "old-b", HydratedAt: "2026-09-30T00:00:00Z"},
		{EntityType: "pr", EntityID: "old-a", HydratedAt: "2026-09-29T00:00:00Z"},
		{EntityType: "pr", EntityID: "oldest", HydratedAt: "2026-09-01T00:00:00Z"},
		{EntityType: "pr", EntityID: "never", HydratedAt: ""},
		{EntityType: "pr", EntityID: "garbled", HydratedAt: "not a time"},
		{EntityType: "pr", EntityID: "gone", HydratedAt: "2026-09-01T00:00:00Z", Inactive: true},
		{EntityType: "issue", EntityID: "other-type", HydratedAt: ""},
		{EntityType: "pr", EntityID: "edge", HydratedAt: "2026-10-01T06:00:00Z"}, // exactly 6h: not older than
	}
}

func TestActiveCount(t *testing.T) {
	es := sweepEntities()
	if got := ActiveCount(es, "pr", nil); got != 7 {
		t.Errorf("pr active = %d, want 7 (inactive and other-type excluded)", got)
	}
	if got := ActiveCount(es, "issue", nil); got != 1 {
		t.Errorf("issue active = %d, want 1", got)
	}
	if got := ActiveCount(nil, "pr", nil); got != 0 {
		t.Errorf("empty = %d", got)
	}
}

// asType returns the entities with every pr row retyped to entityType, so a
// test of the age rule can run it on a type that still has one (pr has none).
func asType(es []store.Entity, entityType string) []store.Entity {
	out := make([]store.Entity, len(es))
	for i, e := range es {
		if e.EntityType == "pr" {
			e.EntityType = entityType
		}
		out[i] = e
	}
	return out
}

func TestDueBacklog(t *testing.T) {
	// due: old-b, old-a, oldest, never, garbled; not due: fresh, edge (6h is
	// not older than 6h), gone (inactive), other-type. The age rule is shown on
	// the thread type: pr has no due backlog (TestDueBacklogOfPRIsAlwaysZero).
	es := asType(sweepEntities(), "thread")
	if got := DueBacklog(es, "thread", sweepTestNow, 6*time.Hour, nil); got != 5 {
		t.Errorf("DueBacklog = %d, want 5", got)
	}
	if got := DueBacklog(es, "thread", sweepTestNow, 100*24*time.Hour, nil); got != 2 {
		t.Errorf("DueBacklog with a huge max age = %d, want 2 (never hydrated and unparseable)", got)
	}
}

// PR lane refactor, packet P3b2: with the router sweeps gone every quiet PR ages
// past the sweep max age, so counting PRs as due would mark the daily focus draft
// incomplete for a backlog nothing works off. The same rows of another type are
// counted (the control), so the zero is the PR exclusion and not an empty fixture.
func TestDueBacklogOfPRIsAlwaysZero(t *testing.T) {
	es := sweepEntities()
	for _, maxAge := range []time.Duration{time.Hour, 6 * time.Hour, 100 * 24 * time.Hour} {
		if got := DueBacklog(es, "pr", sweepTestNow, maxAge, nil); got != 0 {
			t.Errorf("DueBacklog(pr, max age %s) = %d, want 0", maxAge, got)
		}
	}
	if got := DueBacklog(asType(es, "thread"), "thread", sweepTestNow, 6*time.Hour, nil); got != 5 {
		t.Errorf("control: the same rows as thread are %d due, want 5", got)
	}
	if got := ActiveCount(es, "pr", nil); got != 7 {
		t.Errorf("pr active = %d, want 7 (the exclusion must not touch the active count)", got)
	}
}

func TestSweepBoundHolds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		active   int
		perPoll  int
		maxAge   time.Duration
		interval time.Duration
		want     bool
	}{
		{"comfortably inside", 100, 20, 6 * time.Hour, time.Hour, true}, // 5 x 1h <= 6h
		{"exactly on the bound", 120, 20, 6 * time.Hour, time.Hour, true},
		{"just past the bound", 121, 20, 6 * time.Hour, time.Hour, false},
		{"slow polling", 100, 20, 6 * time.Hour, 2 * time.Hour, false},
		{"no active entities", 0, 20, 6 * time.Hour, time.Hour, true},
		{"zero per poll with entities", 5, 0, 6 * time.Hour, time.Hour, false},
		{"zero per poll without entities", 0, 0, 6 * time.Hour, time.Hour, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SweepBoundHolds(tc.active, tc.perPoll, tc.maxAge, tc.interval); got != tc.want {
				t.Errorf("SweepBoundHolds = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSelectSweepOldestFirstCapAndExclusions(t *testing.T) {
	es := sweepEntities()
	got := selectSweep(es, "pr", sweepTestNow, 6*time.Hour, 4, nil)
	// never-hydrated and unparseable rank oldest of all (ties by id), then
	// the oldest dated row.
	if want := []string{"garbled", "never", "oldest", "old-a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("selectSweep = %v, want %v", got, want)
	}
	got = selectSweep(es, "pr", sweepTestNow, 6*time.Hour, 10, map[string]bool{"never": true, "garbled": true})
	if want := []string{"oldest", "old-a", "old-b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("selectSweep with exclusions = %v, want %v", got, want)
	}
	if got := selectSweep(es, "pr", sweepTestNow, 6*time.Hour, 0, nil); len(got) != 0 {
		t.Errorf("zero cap selected %v", got)
	}
}

// --- jitter and the two age-sweep tiers ---

func TestJitterOffsetIsDeterministicBoundedAndSpread(t *testing.T) {
	age := 30 * time.Minute
	span := age / 5
	seen := map[time.Duration]bool{}
	for i := 0; i < 88; i++ {
		id := fmt.Sprintf("<owner>/<repo>#%d", i)
		off := jitterOffset(id, age)
		if off < 0 || off >= span {
			t.Fatalf("jitterOffset(%q) = %v, want in [0, %v)", id, off, span)
		}
		if again := jitterOffset(id, age); again != off {
			t.Fatalf("jitterOffset(%q) differs across calls: %v vs %v", id, off, again)
		}
		seen[off] = true
	}
	if len(seen) < 40 {
		t.Errorf("only %d distinct offsets over 88 entities, want them spread", len(seen))
	}
	// The hash is FNV-1a over the id, so the value is stable across processes
	// and releases: pin one so a hash change is a deliberate edit.
	if got, want := jitterOffset("a", time.Hour), time.Duration(0xaf63dc4c8601ec8c%uint64(time.Hour/5)); got != want {
		t.Errorf("jitterOffset(a, 1h) = %v, want %v (FNV-1a 64)", got, want)
	}
	if got := jitterOffset("a", 3*time.Nanosecond); got != 0 {
		t.Errorf("a sub-5ns age must carry no jitter, got %v", got)
	}
}

// twoIDsByOffset returns two entity ids whose jitter offsets at age differ,
// the smaller first, plus the offsets.
func twoIDsByOffset(t *testing.T, age time.Duration) (lo, hi string, oLo, oHi time.Duration) {
	t.Helper()
	lo, hi = "e0", "e0"
	oLo, oHi = jitterOffset("e0", age), jitterOffset("e0", age)
	for i := 1; i < 50; i++ {
		id := fmt.Sprintf("e%d", i)
		o := jitterOffset(id, age)
		if o < oLo {
			lo, oLo = id, o
		}
		if o > oHi {
			hi, oHi = id, o
		}
	}
	if oHi-oLo < 2*time.Second {
		t.Fatalf("no two ids with distinct offsets at %v", age)
	}
	return lo, hi, oLo, oHi
}

func TestSelectSweepAppliesJitterToTheDueTime(t *testing.T) {
	maxAge := 6 * time.Hour
	lo, hi, oLo, oHi := twoIDsByOffset(t, maxAge)
	// Hydrated maxAge + the midpoint of the two offsets ago: past the lower
	// due time, not yet past the higher one.
	hydrated := sweepTestNow.Add(-(maxAge + (oLo+oHi)/2)).Format(time.RFC3339Nano)
	es := []store.Entity{
		{EntityType: "pr", EntityID: lo, HydratedAt: hydrated},
		{EntityType: "pr", EntityID: hi, HydratedAt: hydrated},
	}
	if got := selectSweep(es, "pr", sweepTestNow, maxAge, 10, nil); !reflect.DeepEqual(got, []string{lo}) {
		t.Errorf("selectSweep = %v, want only %s (offset %v) due, not %s (offset %v)", got, lo, oLo, hi, oHi)
	}
	// Past the larger offset both are due, the earlier due time first.
	later := sweepTestNow.Add(-(maxAge + oHi + time.Second))
	for i := range es {
		es[i].HydratedAt = later.Format(time.RFC3339Nano)
	}
	if got := selectSweep(es, "pr", sweepTestNow, maxAge, 10, nil); !reflect.DeepEqual(got, []string{lo, hi}) {
		t.Errorf("selectSweep = %v, want %v (lower jitter falls due first)", got, []string{lo, hi})
	}
}

func TestSelectReconcileUsesLatestChangeTimeJitterAndFallbacks(t *testing.T) {
	age := 30 * time.Minute
	lo, hi, oLo, oHi := twoIDsByOffset(t, age)
	logged := sweepTestNow.Add(-(age + (oLo+oHi)/2)).Format(time.RFC3339)
	// RFC3339 is second resolution: re-derive the offsets' window from the
	// truncated stamp so the midpoint stays strictly between them.
	if parsed, _ := time.Parse(time.RFC3339, logged); sweepTestNow.Sub(parsed) < age+oLo || sweepTestNow.Sub(parsed) >= age+oHi {
		t.Fatalf("test setup: %v not inside [%v, %v)", sweepTestNow.Sub(parsed), age+oLo, age+oHi)
	}
	es := []store.Entity{
		{EntityType: "pr", EntityID: lo, HydratedAt: "2026-10-01T11:59:00Z"},
		{EntityType: "pr", EntityID: hi, HydratedAt: "2026-10-01T11:59:00Z"},
		{EntityType: "pr", EntityID: "gone", HydratedAt: "2020-01-01T00:00:00Z", Inactive: true},
		{EntityType: "issue", EntityID: "other-type", HydratedAt: "2020-01-01T00:00:00Z"},
	}
	latest := map[string]string{lo: logged, hi: logged, "gone": "2020-01-01T00:00:00Z", "other-type": "2020-01-01T00:00:00Z"}
	if got := selectReconcile(es, latest, "pr", sweepTestNow, age, 10, nil); !reflect.DeepEqual(got, []string{lo}) {
		t.Errorf("selectReconcile = %v, want only %s (inactive and other-type excluded, %s still inside its jitter)", got, lo, hi)
	}
	if got := selectReconcile(es, latest, "pr", sweepTestNow, age, 10, map[string]bool{lo: true}); len(got) != 0 {
		t.Errorf("selectReconcile with %s excluded = %v, want none", lo, got)
	}

	// An entity with no change_log row falls back to hydrated_at; with
	// neither it is due now and oldest of all.
	es = []store.Entity{
		{EntityType: "pr", EntityID: "fresh-no-log", HydratedAt: "2026-10-01T11:59:00Z"},
		{EntityType: "pr", EntityID: "old-no-log", HydratedAt: "2026-10-01T09:00:00Z"},
		{EntityType: "pr", EntityID: "logged-recent", HydratedAt: "2026-10-01T09:00:00Z"},
		{EntityType: "pr", EntityID: "nothing-at-all"},
		{EntityType: "pr", EntityID: "bad-log", HydratedAt: "2026-10-01T08:00:00Z"},
	}
	latest = map[string]string{"logged-recent": "2026-10-01T11:59:00Z", "bad-log": "not a time"}
	want := []string{"nothing-at-all", "bad-log", "old-no-log"}
	if got := selectReconcile(es, latest, "pr", sweepTestNow, age, 10, nil); !reflect.DeepEqual(got, want) {
		t.Errorf("selectReconcile fallbacks = %v, want %v", got, want)
	}
	if got := selectReconcile(es, latest, "pr", sweepTestNow, age, 2, nil); !reflect.DeepEqual(got, want[:2]) {
		t.Errorf("cap 2 selected %v, want the two oldest %v", got, want[:2])
	}
}

// After a --reset of 88 entities every entity carries a record stamped within
// the same second, so only the jitter spreads them across polls and the cap
// bounds each poll [T-13].
func TestSelectReconcileAfterResetOf88IsCappedOldestFirstAndSpread(t *testing.T) {
	age := 30 * time.Minute
	resetAt := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	var es []store.Entity
	latest := map[string]string{}
	for i := 0; i < 88; i++ {
		id := fmt.Sprintf("<owner>/<repo>#%d", i)
		es = append(es, store.Entity{EntityType: "pr", EntityID: id, HydratedAt: resetAt.Format(time.RFC3339)})
		latest[id] = resetAt.Format(time.RFC3339)
	}
	dues := map[time.Time]bool{}
	for _, e := range es {
		dues[localDueTime(e, latest, age)] = true
	}
	if len(dues) < 40 {
		t.Fatalf("only %d distinct due times for 88 reset entities, want them spread by jitter", len(dues))
	}

	// Late enough that all 88 are due: the cap alone limits the poll.
	late := resetAt.Add(2 * time.Hour)
	got := selectReconcile(es, latest, "pr", late, age, 20, nil)
	if len(got) != 20 {
		t.Fatalf("selected %d, want the cap 20", len(got))
	}
	byOffset := make([]string, len(es))
	for i, e := range es {
		byOffset[i] = e.EntityID
	}
	sort.Slice(byOffset, func(i, j int) bool {
		oi, oj := jitterOffset(byOffset[i], age), jitterOffset(byOffset[j], age)
		if oi != oj {
			return oi < oj
		}
		return byOffset[i] < byOffset[j]
	})
	if !reflect.DeepEqual(got, byOffset[:20]) {
		t.Errorf("selected %v, want the 20 smallest-offset (earliest due) ids %v", got, byOffset[:20])
	}

	// Just past the window start only the earliest-due entities are due, not all 88.
	early := resetAt.Add(age + 3*time.Minute)
	n := len(selectReconcile(es, latest, "pr", early, age, 1000, nil))
	if n == 0 || n >= 88 {
		t.Errorf("%d of 88 due at the window's midpoint, want some but not all", n)
	}
}

func TestEvaluateSweepBound(t *testing.T) {
	// The live count of 88 active PRs: 88 / 20 x 1m = 4.4m, far below 6h.
	live := SweepInputs{ActiveCount: 88, MaxPerPoll: 20, MaxAge: 6 * time.Hour}
	cases := []struct {
		name  string
		in    SweepInputs
		poll  time.Duration
		known bool
		want  BoundVerdict
	}{
		{"live count holds against the remote tier", live, time.Minute, true, BoundHolds},
		{"the boundary holds (<=)", SweepInputs{ActiveCount: 6, MaxPerPoll: 1, MaxAge: 3 * time.Minute}, 30 * time.Second, true, BoundHolds},
		{"over the bound is violated", SweepInputs{ActiveCount: 7, MaxPerPoll: 1, MaxAge: 3 * time.Minute}, 30 * time.Second, true, BoundViolated},
		{"local tier violated while remote holds", SweepInputs{ActiveCount: 88, MaxPerPoll: 20, MaxAge: 4 * time.Minute}, time.Minute, true, BoundViolated},
		{"unknown poll interval is no verdict, never a violation", SweepInputs{ActiveCount: 1000, MaxPerPoll: 1, MaxAge: time.Second}, 0, false, BoundUnknown},
	}
	for _, c := range cases {
		if got := EvaluateSweepBound(c.in, c.poll, c.known); got != c.want {
			t.Errorf("%s: EvaluateSweepBound = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSweepTiersCoverLocalAndRemote(t *testing.T) {
	if len(SweepTiers) != 2 || SweepTiers[0] != TierLocal || SweepTiers[1] != TierRemote {
		t.Errorf("SweepTiers = %v, want [local remote]", SweepTiers)
	}
}

// --- owning-backend entities are outside the old age-sweep tiers ---

var owningJira = map[string]bool{"jira-a": true}

// owningTierEntities is one age-stale beads issue, one age-stale owning-backend
// issue, an age-stale owning-backend PR-typed id (the exclusion is for the
// issue type only), and a fresh beads issue.
func owningTierEntities() []store.Entity {
	return []store.Entity{
		{EntityType: "issue", EntityID: "bd-stale", Backend: "beads-a", HydratedAt: "2026-09-01T00:00:00Z"},
		{EntityType: "issue", EntityID: "PROJ-1", Backend: "jira-a", HydratedAt: "2026-09-01T00:00:00Z"},
		{EntityType: "issue", EntityID: "bd-null", HydratedAt: "2026-09-01T00:00:00Z"},
		{EntityType: "issue", EntityID: "bd-fresh", Backend: "beads-a", HydratedAt: "2026-10-01T11:00:00Z"},
		{EntityType: "pr", EntityID: "pr-1", Backend: "jira-a", HydratedAt: "2026-09-01T00:00:00Z"},
	}
}

func TestActiveCountAndDueBacklogExcludeOwningBackendIssues(t *testing.T) {
	es := owningTierEntities()
	if got := ActiveCount(es, "issue", nil); got != 4 {
		t.Errorf("ActiveCount(no owning set) = %d, want 4", got)
	}
	if got := ActiveCount(es, "issue", owningJira); got != 3 {
		t.Errorf("ActiveCount(owning jira-a) = %d, want 3 (the owning entity left out)", got)
	}
	if got := DueBacklog(es, "issue", sweepTestNow, 6*time.Hour, nil); got != 3 {
		t.Errorf("DueBacklog(no owning set) = %d, want 3", got)
	}
	if got := DueBacklog(es, "issue", sweepTestNow, 6*time.Hour, owningJira); got != 2 {
		t.Errorf("DueBacklog(owning jira-a) = %d, want 2 (bd-stale and bd-null; the owning entity is never due)", got)
	}
	// The exclusion is for the issue type: a pr row carrying an owning
	// backend value is counted as before.
	if got := ActiveCount(es, "pr", owningJira); got != 1 {
		t.Errorf("ActiveCount(pr) = %d, want 1", got)
	}
}

func TestWithoutOwnedKeepsOtherTypesAndNullBackends(t *testing.T) {
	es := owningTierEntities()
	got := withoutOwned(es, "issue", owningJira)
	var ids []string
	for _, e := range got {
		ids = append(ids, e.EntityID)
	}
	sort.Strings(ids)
	if want := []string{"bd-fresh", "bd-null", "bd-stale", "pr-1"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("withoutOwned = %v, want %v", ids, want)
	}
	if got := withoutOwned(es, "issue", nil); len(got) != len(es) {
		t.Errorf("an empty owning set dropped rows: %d of %d", len(got), len(es))
	}
}

func TestOwningSetReadsTheConfiguredOwningInstances(t *testing.T) {
	cfg := &config.Config{}
	cfg.Watch.Issue.Queries = []string{"mine", "tracked"}
	cfg.Watch.Issue.Backends = map[string]config.WatchBackend{
		"mine":    {Instance: "beads-a"},
		"tracked": {Instance: "jira-a", OwnsChanges: true},
	}
	if got := OwningSet(cfg, "issue"); !reflect.DeepEqual(got, owningJira) {
		t.Errorf("OwningSet(issue) = %v, want %v", got, owningJira)
	}
	if got := OwningSet(cfg, "pr"); got != nil {
		t.Errorf("OwningSet(pr) = %v, want nil", got)
	}
	if got := OwningSet(nil, "issue"); got != nil {
		t.Errorf("OwningSet(nil cfg) = %v, want nil", got)
	}
}

// owningEngine is a list engine whose config has one owning instance next to
// the beads one, with a stale beads entity and a stale owning-backend entity.
func owningEngine(t *testing.T) (*Engine, *store.Store, *storeHydrator) {
	t.Helper()
	e, st, h := newListEngine(t, "open", "tracked")
	e.Cfg.Watch.Issue.Backends = map[string]config.WatchBackend{
		"open":    {Instance: "beads-a"},
		"tracked": {Instance: "jira-a", OwnsChanges: true},
	}
	// Both rows: hydrated 12h ago (past sweep.max_age) with a 3h-old log row
	// (past reconcile_age), so each tier would select either.
	seedTierRow(t, st, "bd-1", "2026-10-01T00:00:00Z", "F1", "2026-10-01T09:00:00Z")
	seedTierRow(t, st, "PROJ-1", "2026-10-01T00:00:00Z", "F2", "2026-10-01T09:00:00Z")
	jira := "jira-a"
	if err := st.SetEntityColumns("o/r", "issue", "PROJ-1", &jira, nil); err != nil {
		t.Fatal(err)
	}
	beads := "beads-a"
	if err := st.SetEntityColumns("o/r", "issue", "bd-1", &beads, nil); err != nil {
		t.Fatal(err)
	}
	return e, st, h
}

func TestOldTiersTouchOnlyTheNonOwningEntity(t *testing.T) {
	t.Run("remote tier", func(t *testing.T) {
		e, st, h := owningEngine(t)
		owningBefore, _, _ := st.GetEntity("o/r", "issue", "PROJ-1")
		if err := e.sweep(context.Background(), "issue", &pollState{hydrated: map[string]error{}}, &budget{left: 10}, nil); err != nil {
			t.Fatal(err)
		}
		if want := []string{"bd-1"}; !reflect.DeepEqual(h.calls, want) {
			t.Errorf("remote tier hydrated %v, want %v", h.calls, want)
		}
		assertOwningUntouched(t, st, owningBefore)
	})
	t.Run("local tier", func(t *testing.T) {
		e, st, h := owningEngine(t)
		owningBefore, _, _ := st.GetEntity("o/r", "issue", "PROJ-1")
		beadsBefore, _, _ := st.GetEntity("o/r", "issue", "bd-1")
		if err := e.reconcileLocal(context.Background(), "issue", &pollState{hydrated: map[string]error{}}); err != nil {
			t.Fatal(err)
		}
		if len(h.calls) != 0 {
			t.Errorf("local tier hydrated %v, want none", h.calls)
		}
		beadsAfter, _, _ := st.GetEntity("o/r", "issue", "bd-1")
		if beadsAfter.Version != beadsBefore.Version+1 {
			t.Errorf("beads entity version = %d, want %d (one local reconcile)", beadsAfter.Version, beadsBefore.Version+1)
		}
		assertOwningUntouched(t, st, owningBefore)
	})
}

func assertOwningUntouched(t *testing.T, st *store.Store, before store.Entity) {
	t.Helper()
	after, found, err := st.GetEntity("o/r", "issue", "PROJ-1")
	if err != nil || !found {
		t.Fatalf("GetEntity PROJ-1 = %v, %v", found, err)
	}
	if after.Version != before.Version || after.HydratedAt != before.HydratedAt {
		t.Errorf("owning entity changed: before %+v after %+v", before, after)
	}
	if got := len(historyOf(t, st, "PROJ-1")); got != 1 {
		t.Errorf("owning entity has %d change_log rows, want only its seed row", got)
	}
}

func TestObserveExcludesOwningBackendEntitiesFromActiveAndDue(t *testing.T) {
	_, st, _ := owningEngine(t)
	flow, err := Observe(st, sweepTestNow, 6*time.Hour, owningJira)
	if err != nil {
		t.Fatal(err)
	}
	var issue *TypeFlow
	for i := range flow.Types {
		if flow.Types[i].Type == "issue" {
			issue = &flow.Types[i]
		}
	}
	if issue == nil {
		t.Fatal("no issue type in the flow")
	}
	if issue.Active != 1 || issue.Due != 1 {
		t.Errorf("issue Active/Due = %d/%d, want 1/1 (only the beads entity)", issue.Active, issue.Due)
	}
	// The local-tier sweep-bound input is built from the same Active.
	cfg := &config.Config{}
	if in := SweepInputsForTier(cfg, *issue, TierLocal); in.ActiveCount != 1 {
		t.Errorf("local-tier ActiveCount = %d, want 1", in.ActiveCount)
	}
	flow, err = Observe(st, sweepTestNow, 6*time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tf := range flow.Types {
		if tf.Type == "issue" && (tf.Active != 2 || tf.Due != 2) {
			t.Errorf("without the owning set Active/Due = %d/%d, want 2/2", tf.Active, tf.Due)
		}
	}
}

// --- WatchedIDs for an owning backend reads the watched column ---

func TestWatchedIDsOwningReadsTheEntityColumnAndNoWatchsetKey(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	seed := func(id, backend string, watched *bool) {
		seedIssue(t, st, id, &backend)
		if watched != nil {
			if err := st.SetEntityColumns("o/r", "issue", id, nil, watched); err != nil {
				t.Fatal(err)
			}
		}
	}
	yes, no := true, false
	seed("PROJ-1", "jira-a", &yes)
	seed("PROJ-2", "jira-a", &no)
	seed("PROJ-3", "jira-a", nil)
	seed("PROJ-4", "jira-b", &yes) // another, non-owning instance
	seed("bd-1", "beads-a", &yes)  // watched column is meaningless off an owning backend
	if err := saveWatchSet(st, "issue", "mine", map[string]bool{"bd-1": true, "bd-2": true}); err != nil {
		t.Fatal(err)
	}

	ids, known := WatchedIDs(st, []string{"mine", "tracked"}, "issue", owningJira)
	if !known {
		t.Error("known = false with an owning instance, want true")
	}
	want := map[string]bool{"PROJ-1": true, "bd-1": true, "bd-2": true}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want %v (the owning watched entity plus the persisted beads set)", ids, want)
	}
	if _, found, _ := st.GetMeta(watchSetKey("issue", "tracked")); found {
		t.Error("a change_flow.watchset key was written for the owning query")
	}

	// A non-owning read is exactly the persisted set.
	ids, known = WatchedIDs(st, []string{"mine"}, "issue", nil)
	if !known || !reflect.DeepEqual(ids, map[string]bool{"bd-1": true, "bd-2": true}) {
		t.Errorf("non-owning ids = %v known=%v", ids, known)
	}

	watched, all := OwningEntityIDs(st, "issue", owningJira)
	if !reflect.DeepEqual(watched, map[string]bool{"PROJ-1": true}) ||
		!reflect.DeepEqual(all, map[string]bool{"PROJ-1": true, "PROJ-2": true, "PROJ-3": true}) {
		t.Errorf("OwningEntityIDs = %v / %v", watched, all)
	}
}
