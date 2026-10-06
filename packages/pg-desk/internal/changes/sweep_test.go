package changes

import (
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

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

func TestDueBacklog(t *testing.T) {
	// due: old-b, old-a, oldest, never, garbled; not due: fresh, edge (6h is
	// not older than 6h), gone (inactive), other-type.
	if got := DueBacklog(sweepEntities(), "pr", sweepTestNow, 6*time.Hour); got != 5 {
		t.Errorf("DueBacklog = %d, want 5", got)
	}
	if got := DueBacklog(sweepEntities(), "pr", sweepTestNow, 100*24*time.Hour); got != 2 {
		t.Errorf("DueBacklog with a huge max age = %d, want 2 (never hydrated and unparseable)", got)
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
