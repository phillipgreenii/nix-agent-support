package stranded

import (
	"testing"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/bd"
	"github.com/phillipgreenii/beads-exporter/internal/metrics"
)

// testNow is the injected clock of every case; nothing here reads the wall clock.
var testNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// testAge is the module default of staleClaimHours.
const testAge = 12 * time.Hour

func ptrTime(t time.Time) *time.Time { return &t }

// claimed is a bead with an assignee whose last update was ago before testNow.
func claimed(id, assignee, status string, ago time.Duration) bd.Bead {
	return bd.Bead{ID: id, Assignee: assignee, Status: status, UpdatedAt: ptrTime(testNow.Add(-ago))}
}

func ids(r Result) []string {
	out := []string{}
	for _, c := range r.Claims {
		out = append(out, c.BeadID)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAgeBoundaryIsStrictlyOlderThanTheAge(t *testing.T) {
	got := Classify([]bd.Bead{
		claimed("fresh", "w", "in_progress", time.Minute),
		claimed("just-inside", "w", "in_progress", testAge-time.Second),
		claimed("edge", "w", "in_progress", testAge),
		claimed("just-outside", "w", "in_progress", testAge+time.Second),
		claimed("old", "w", "in_progress", 72*time.Hour),
	}, testNow, testAge)
	if want := []string{"just-outside", "old"}; !equalStrings(ids(got), want) {
		t.Fatalf("stranded = %v, want %v (a claim updated exactly the age ago is not yet stranded)", ids(got), want)
	}
}

func TestTheAgeIsTheConfiguredOne(t *testing.T) {
	beads := []bd.Bead{claimed("b1", "w", "open", 3*time.Hour)}
	if got := Classify(beads, testNow, 2*time.Hour); !equalStrings(ids(got), []string{"b1"}) {
		t.Fatalf("age 2h: stranded = %v, want [b1]", ids(got))
	}
	if got := Classify(beads, testNow, 4*time.Hour); len(got.Claims) != 0 {
		t.Fatalf("age 4h: stranded = %v, want none", ids(got))
	}
}

func TestEachClaimStatusIsJudgedAndNoOtherStatusIs(t *testing.T) {
	old := testAge + time.Hour
	var beads []bd.Bead
	for _, st := range []string{"open", "in_progress", "hooked", "blocked", "deferred", "closed", "pinned", "review", "archived"} {
		beads = append(beads, claimed("old-"+st, "w", st, old), claimed("fresh-"+st, "w", st, time.Hour))
	}
	got := Classify(beads, testNow, testAge)
	if want := []string{"old-hooked", "old-in_progress", "old-open"}; !equalStrings(ids(got), want) {
		t.Fatalf("stranded = %v, want %v", ids(got), want)
	}
}

func TestAClaimNeedsAnAssignee(t *testing.T) {
	old := testAge + time.Hour
	got := Classify([]bd.Bead{
		claimed("unassigned-open", "", "open", old),
		claimed("unassigned-in-progress", "", "in_progress", old),
		claimed("unassigned-hooked", "", "hooked", old),
		claimed("assigned", "w", "open", old),
	}, testNow, testAge)
	if want := []string{"assigned"}; !equalStrings(ids(got), want) {
		t.Fatalf("stranded = %v, want %v", ids(got), want)
	}
}

func TestEveryAssigneeIsJudgedTheSameWay(t *testing.T) {
	old, fresh := testAge+time.Minute, time.Minute
	var beads []bd.Bead
	for _, who := range []string{"operator", "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa-drain", "worker-alpha"} {
		beads = append(beads, claimed("old "+who, who, "in_progress", old), claimed("fresh "+who, who, "in_progress", fresh))
	}
	got := Classify(beads, testNow, testAge)
	want := []string{"old aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa-drain", "old operator", "old worker-alpha"}
	if !equalStrings(ids(got), want) {
		t.Fatalf("stranded = %v, want %v", ids(got), want)
	}
}

func TestOnlyUpdatedAtCounts(t *testing.T) {
	// A fresh started_at or created_at does not rescue a claim whose last update is
	// old, and an old one does not strand a claim updated recently.
	startedFresh := claimed("started-fresh-updated-old", "w", "in_progress", testAge+time.Hour)
	startedFresh.StartedAt = ptrTime(testNow.Add(-time.Minute))
	startedFresh.CreatedAt = ptrTime(testNow.Add(-time.Minute))
	startedOld := claimed("started-old-updated-fresh", "w", "in_progress", time.Minute)
	startedOld.StartedAt = ptrTime(testNow.Add(-72 * time.Hour))
	got := Classify([]bd.Bead{startedFresh, startedOld}, testNow, testAge)
	if want := []string{"started-fresh-updated-old"}; !equalStrings(ids(got), want) {
		t.Fatalf("stranded = %v, want %v", ids(got), want)
	}
}

func TestAClaimWithoutUpdatedAtIsStranded(t *testing.T) {
	untimed := bd.Bead{ID: "untimed", Assignee: "w", Status: "open", StartedAt: ptrTime(testNow.Add(-time.Minute))}
	got := Classify([]bd.Bead{untimed}, testNow, testAge)
	if !equalStrings(ids(got), []string{"untimed"}) {
		t.Fatalf("stranded = %v, want [untimed]", ids(got))
	}
	if !got.Claims[0].LastUpdate.IsZero() {
		t.Fatalf("last update = %v, want zero", got.Claims[0].LastUpdate)
	}
}

func TestClaimRecordsBeadFieldsOrderedByID(t *testing.T) {
	started := testNow.Add(-48 * time.Hour)
	updated := testNow.Add(-24 * time.Hour)
	b1 := claimed("b1", "w1", "in_progress", 24*time.Hour)
	b1.StartedAt = &started
	b2 := claimed("b2", "w2", "open", 24*time.Hour)
	got := Classify([]bd.Bead{b2, b1}, testNow, testAge)
	want := []Claim{
		{BeadID: "b1", Assignee: "w1", Status: "in_progress", ClaimTime: started, LastUpdate: updated},
		{BeadID: "b2", Assignee: "w2", Status: "open", LastUpdate: updated},
	}
	if len(got.Claims) != 2 || got.Claims[0] != want[0] || got.Claims[1] != want[1] {
		t.Fatalf("claims = %+v, want %+v", got.Claims, want)
	}
}

func TestNoCandidatesGiveAnEmptyResult(t *testing.T) {
	if got := Classify(nil, testNow, testAge); len(got.Claims) != 0 {
		t.Fatalf("stranded = %v", ids(got))
	}
}

func TestSamplesZeroFillByStatusAndOldestLastUpdate(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	t1 := time.Unix(1_700_000_500, 0)
	r := Result{Claims: []Claim{
		{BeadID: "a", Status: "open", LastUpdate: t1, ClaimTime: time.Unix(1_600_000_000, 0)},
		{BeadID: "b", Status: "open", LastUpdate: t0},
		{BeadID: "c", Status: "hooked"},
	}}
	got := map[string]float64{}
	for _, s := range r.Samples("alpha") {
		key := s.Family
		for _, l := range s.Labels {
			key += "|" + l.Name + "=" + l.Value
		}
		got[key] = s.Value
	}
	want := map[string]float64{
		metrics.FamStrandedClaims + "|db=alpha|status=open":        2,
		metrics.FamStrandedClaims + "|db=alpha|status=in_progress": 0,
		metrics.FamStrandedClaims + "|db=alpha|status=hooked":      1,
		metrics.FamOldestStranded + "|db=alpha":                    1_700_000_000,
	}
	if len(got) != len(want) {
		t.Fatalf("samples = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("samples[%s] = %v, want %v (all: %v)", k, got[k], v, got)
		}
	}
}

func TestSamplesOmitOldestWhenEmptyOrUntimed(t *testing.T) {
	for name, r := range map[string]Result{
		"empty":   {},
		"untimed": {Claims: []Claim{{BeadID: "a", Status: "open", ClaimTime: testNow}}},
	} {
		for _, s := range r.Samples("alpha") {
			if s.Family == metrics.FamOldestStranded {
				t.Fatalf("%s: oldest series emitted: %+v", name, s)
			}
		}
	}
	if n := len((Result{}).Samples("alpha")); n != 3 {
		t.Fatalf("empty result has %d samples, want the three zero-filled statuses", n)
	}
}
