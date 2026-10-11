package focus

import (
	"reflect"
	"testing"
	"time"
)

func planFixture() []PlanRow {
	return []PlanRow{
		{Key: kb("bd-3"), RankPosition: 3, Tier: tierPtr(TierNotStarted), SelectedAt: "2026-10-10T08:00:00Z"},
		{Key: kp("1"), RankPosition: 1, Tier: tierPtr(TierOverdue), SelectedAt: "2026-10-10T08:00:00Z"},
		{Key: kp("2"), RankPosition: 2, Tier: tierPtr(TierStarted), SelectedAt: "2026-10-10T08:00:00Z"},
		{Key: kp("4"), RankPosition: 4, Tier: nil, SelectedAt: "2026-10-10T08:00:00Z"},
	}
}

func TestPlanViewOrdersByRecordedPositionWithRecordedTier(t *testing.T) {
	// A fresh rank that would put everything elsewhere changes nothing.
	rk := rankingOf(rrow(kp("4"), KindPR, TierOverdue, false), rrow(kb("bd-3"), KindBead, TierOverdue, false),
		rrow(kp("2"), KindPR, TierOverdue, false), rrow(kp("1"), KindPR, TierNotStarted, false))
	v := BuildPlanView(planFixture(), LiveFacts{}, rk, 6)
	var got []Key
	for _, r := range v.Rows {
		got = append(got, r.Key)
	}
	if want := []Key{kp("1"), kp("2"), kb("bd-3"), kp("4")}; !reflect.DeepEqual(got, want) {
		t.Errorf("plan order = %v, want %v", got, want)
	}
	if *v.Rows[0].Tier != TierOverdue || *v.Rows[1].Tier != TierStarted || v.Rows[3].Tier != nil {
		t.Errorf("recorded tiers not preserved: %v %v %v", *v.Rows[0].Tier, *v.Rows[1].Tier, v.Rows[3].Tier)
	}
}

func TestShowPlanModeFreezesOrderAndShowsDrift(t *testing.T) {
	plan := planFixture()
	rk := rankingOf(rrow(kp("4"), KindPR, TierOverdue, false), rrow(kp("1"), KindPR, TierNotStarted, false),
		rrow(kp("9"), KindPR, TierOverdue, false), rrow(kp("2"), KindPR, TierStarted, false), rrow(kb("bd-3"), KindBead, TierStarted, false))
	before := BuildPlanView(plan, LiveFacts{}, rankingOf(), 6)
	after := BuildPlanView(plan, LiveFacts{}, rk, 6)
	if !reflect.DeepEqual(before.Rows, after.Rows) {
		t.Errorf("the plan view depends on the fresh rank:\n%+v\n%+v", before.Rows, after.Rows)
	}

	// Drift is counted and named from the fresh rank by the draft re-render.
	d := richDraft()
	fresh := rankingOf(rrow(kp("2"), KindPR, TierOverdue, false), rrow(kp("1"), KindPR, TierOverdue, true), rrow(kp("7"), KindPR, TierOverdue, false))
	v := ReRender(d, fixtureNow.Add(-3*time.Hour), fixtureNow, LiveFacts{}, fresh, plan)
	if v.Drift.RowsPlacedDifferently != 2 {
		t.Errorf("RowsPlacedDifferently = %d, want 2 (pr:1 and pr:2 swapped)", v.Drift.RowsPlacedDifferently)
	}
	if !reflect.DeepEqual(v.Drift.NoLongerCandidate, []Key{kb("bd-3")}) {
		t.Errorf("NoLongerCandidate = %v, want [issue:bd-3]", v.Drift.NoLongerCandidate)
	}
}

func TestPlanViewMarks(t *testing.T) {
	by := kp("1")
	live := LiveFacts{
		kp("1"):    {Finished: true, Outcome: "merged"},
		kp("2"):    {Claimant: "pat", Deferred: true},
		kb("bd-3"): {Hidden: true},
		kp("4"):    {Inactive: true, CoveredBy: &Key{Type: "issue", ID: "bd-3"}},
	}
	v := BuildPlanView(planFixture(), live, Ranking{Covered: map[Key]Key{kp("2"): by}}, 6)
	want := [][]string{
		{"finished"},
		{"claimed by pat", "deferred", "covered by pr:1"},
		{"no longer a candidate: hidden"},
		{"no longer a candidate: inactive", "covered by issue:bd-3"},
	}
	for i, r := range v.Rows {
		if !reflect.DeepEqual(r.Marks, want[i]) {
			t.Errorf("row %v marks = %v, want %v", r.Key, r.Marks, want[i])
		}
	}
	if v.Rows[0].Facts.Outcome != "merged" {
		t.Errorf("facts not carried: %+v", v.Rows[0].Facts)
	}
	// A key never covers itself.
	self := BuildPlanView(planFixture()[:2], LiveFacts{kb("bd-3"): {CoveredBy: &Key{Type: "issue", ID: "bd-3"}}}, Ranking{}, 6)
	for _, r := range self.Rows {
		if len(r.Marks) != 0 {
			t.Errorf("unexpected marks %v", r.Marks)
		}
	}
}

func TestFinishedRowShownInPlanFreesItsSlot(t *testing.T) {
	plan := planFixture() // 4 rows
	live := LiveFacts{kp("1"): {Finished: true}}
	v := BuildPlanView(plan, live, Ranking{}, 4)
	if want := (CapLine{Cap: 4, InPlan: 3, OpenSlots: 1, Finished: 1}); v.CapLine != want {
		t.Errorf("cap line = %+v, want %+v", v.CapLine, want)
	}
	if len(v.Rows) != 4 {
		t.Errorf("finished row removed from the plan view: %d rows", len(v.Rows))
	}
	if got := v.CapLine.String(); got != "cap 4: 3 in plan, 1 open slots; 1 finished, not counted" {
		t.Errorf("cap line text = %q", got)
	}
}

func TestFinishedShownOnceInsidePlan(t *testing.T) {
	// A finished plan row appears exactly once, inside the plan, and is
	// counted once, as finished (never also as in plan).
	live := LiveFacts{kp("1"): {Finished: true}, kp("2"): {Finished: true}}
	v := BuildPlanView(planFixture(), live, Ranking{}, 6)
	seen := map[Key]int{}
	finished := 0
	for _, r := range v.Rows {
		seen[r.Key]++
		for _, m := range r.Marks {
			if m == "finished" {
				finished++
			}
		}
	}
	for k, n := range seen {
		if n != 1 {
			t.Errorf("%v appears %d times", k, n)
		}
	}
	if finished != 2 || v.CapLine.Finished != 2 || v.CapLine.InPlan != 2 {
		t.Errorf("finished marks %d, cap line %+v", finished, v.CapLine)
	}
}

func TestPlanViewCapLineOverCapNeverNegative(t *testing.T) {
	v := BuildPlanView(planFixture(), LiveFacts{}, Ranking{}, 2)
	if v.CapLine.OpenSlots != 0 || v.CapLine.InPlan != 4 {
		t.Errorf("cap line = %+v", v.CapLine)
	}
	if got := BuildPlanView(nil, nil, Ranking{}, 0).CapLine.String(); got != "cap 6: 0 in plan, 6 open slots" {
		t.Errorf("empty plan cap line = %q", got)
	}
}
