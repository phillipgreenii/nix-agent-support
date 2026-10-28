package changes

import (
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
	if got := ActiveCount(es, "pr"); got != 7 {
		t.Errorf("pr active = %d, want 7 (inactive and other-type excluded)", got)
	}
	if got := ActiveCount(es, "issue"); got != 1 {
		t.Errorf("issue active = %d, want 1", got)
	}
	if got := ActiveCount(nil, "pr"); got != 0 {
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
	if got := DueBacklog(es, "thread", sweepTestNow, 6*time.Hour, 0); got != 5 {
		t.Errorf("DueBacklog = %d, want 5", got)
	}
	if got := DueBacklog(es, "thread", sweepTestNow, 100*24*time.Hour, 0); got != 2 {
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
		if got := DueBacklog(es, "pr", sweepTestNow, maxAge, 0); got != 0 {
			t.Errorf("DueBacklog(pr, max age %s) = %d, want 0", maxAge, got)
		}
	}
	if got := DueBacklog(asType(es, "thread"), "thread", sweepTestNow, 6*time.Hour, 0); got != 5 {
		t.Errorf("control: the same rows as thread are %d due, want 5", got)
	}
	if got := ActiveCount(es, "pr"); got != 7 {
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

// --- the issue type has no age sweep: its backlog is pending hydrations ---

var owningJira = map[string]bool{"jira-a": true}

func boolPtr(b bool) *bool { return &b }

// issueRows builds n issue rows with the given watched mark and hydrated_at.
func issueRows(prefix string, n int, watched *bool, hydratedAt string) []store.Entity {
	out := make([]store.Entity, n)
	for i := range out {
		out[i] = store.Entity{EntityType: "issue", EntityID: fmt.Sprintf("%s-%d", prefix, i), Watched: watched, HydratedAt: hydratedAt}
	}
	return out
}

// An active issue set that is fully hydrated has nothing pending: however old
// hydrated_at is, the due backlog is 0 (an age never makes an issue due, since
// no sweep re-hydrates it).
func TestDueBacklogOfIssueIsZeroForAFullyHydratedSet(t *testing.T) {
	es := issueRows("bd-old", 50, boolPtr(true), "2026-01-01T00:00:00Z")
	for _, maxAge := range []time.Duration{time.Minute, 6 * time.Hour, 100 * 24 * time.Hour} {
		if got := DueBacklog(es, "issue", sweepTestNow, maxAge, 0); got != 0 {
			t.Errorf("DueBacklog(50 hydrated watched issues, max age %s) = %d, want 0", maxAge, got)
		}
	}
	if got := ActiveCount(es, "issue"); got != 50 {
		t.Errorf("ActiveCount = %d, want 50 (the age rule never touches the active count)", got)
	}
}

// Pending hydrations are the active, watched, never-hydrated issues plus the
// failed-ingest entries the caller passes; everything else is not counted.
func TestDueBacklogOfIssueCountsPendingHydrationsAndFailedIngests(t *testing.T) {
	pending := issueRows("bd-new", 3, boolPtr(true), "")
	hydrated := issueRows("bd-old", 4, boolPtr(true), "2026-01-01T00:00:00Z")
	notPending := append(issueRows("bd-unwatched", 2, nil, ""), issueRows("bd-off", 2, boolPtr(false), "")...)
	gone := issueRows("bd-gone", 2, boolPtr(true), "")
	for i := range gone {
		gone[i].Inactive = true
	}
	garbled := issueRows("bd-garbled", 1, boolPtr(true), "not a time") // hydrated at some point: not pending
	es := append(append(append(append(pending, hydrated...), notPending...), gone...), garbled...)

	if got := DueBacklog(es, "issue", sweepTestNow, 6*time.Hour, 2); got != 5 {
		t.Errorf("3 pending + 2 failed ingests: DueBacklog = %d, want 5", got)
	}
	if got := DueBacklog(es, "issue", sweepTestNow, 6*time.Hour, 0); got != 3 {
		t.Errorf("3 pending, no failed ingests: DueBacklog = %d, want 3", got)
	}
	if got := DueBacklog(nil, "issue", sweepTestNow, 6*time.Hour, 2); got != 2 {
		t.Errorf("failed ingests alone: DueBacklog = %d, want 2", got)
	}
	// An entity of the owning backend is counted like any other: the old tiers
	// that skipped it are gone, so it is pending until it is hydrated.
	owned := store.Entity{EntityType: "issue", EntityID: "PROJ-1", Backend: "jira-a", Watched: boolPtr(true)}
	if got := DueBacklog([]store.Entity{owned}, "issue", sweepTestNow, 6*time.Hour, 0); got != 1 {
		t.Errorf("a watched, never-hydrated owning-backend issue: DueBacklog = %d, want 1", got)
	}
}

// The failed-ingest term is for issue only; pr stays 0 and thread keeps its age
// rule, as they were.
func TestDueBacklogFailedIngestTermIsIssueOnly(t *testing.T) {
	es := sweepEntities()
	if got := DueBacklog(es, "pr", sweepTestNow, 6*time.Hour, 9); got != 0 {
		t.Errorf("pr with failed ingests = %d, want 0", got)
	}
	if got := DueBacklog(asType(es, "thread"), "thread", sweepTestNow, 6*time.Hour, 9); got != 5 {
		t.Errorf("thread with failed ingests = %d, want 5 (the age rule alone)", got)
	}
}

func TestHasSweepBoundIsFalseForIssueOnly(t *testing.T) {
	for typ, want := range map[string]bool{"pr": true, "thread": true, "issue": false} {
		if got := HasSweepBound(typ); got != want {
			t.Errorf("HasSweepBound(%q) = %v, want %v", typ, got, want)
		}
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

// Observe reports the issue type's pending hydrations as its Due: watched
// never-hydrated entities (owning backend or not) plus the failed-ingest
// entries of the store, while every issue stays in Active.
func TestObserveIssueDueIsPendingHydrationsAndFailedIngests(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	seed := func(id, backend, hydratedAt string, watched bool) {
		t.Helper()
		e := store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: `{}`, AsOf: "x"}
		if _, err := st.WriteEntityStateWithLog(e, 0, hydratedAt, true, nil, "sync", "2026-10-01T10:00:00Z"); err != nil {
			t.Fatal(err)
		}
		if err := st.SetEntityColumns("o/r", "issue", id, &backend, &watched); err != nil {
			t.Fatal(err)
		}
	}
	seed("bd-old", "beads-a", "2026-01-01T00:00:00Z", true) // hydrated long ago: not due
	seed("bd-new", "beads-a", "", true)                     // pending
	seed("PROJ-1", "jira-a", "", true)                      // pending (owning backend)
	seed("bd-unwatched", "beads-a", "", false)              // not watched: not pending
	for _, f := range []store.IngestFailure{
		{Instance: "jira-a", EntityID: "PROJ-8", Seq: 1, StoreIdentity: "s", FirstFailedAt: "2026-10-01T10:00:00Z"},
		{Instance: "jira-a", EntityID: "PROJ-9", Seq: 2, StoreIdentity: "s", FirstFailedAt: "2026-10-01T10:00:00Z"},
	} {
		if err := st.RecordIngestFailure(f); err != nil {
			t.Fatal(err)
		}
	}
	flow, err := Observe(st, sweepTestNow, 6*time.Hour)
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
	if issue.Active != 4 || issue.Due != 4 {
		t.Errorf("issue Active/Due = %d/%d, want 4/4 (2 pending + 2 failed ingests; every active issue counted)", issue.Active, issue.Due)
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
