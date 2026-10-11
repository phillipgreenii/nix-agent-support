package focus

import (
	"reflect"
	"testing"
	"time"
)

func TestNewCandidatesAfterDraftShowAsNewSinceDraft(t *testing.T) {
	d := NewInitialDraft(rankingOf(rrow(kp("1"), KindPR, TierStarted, false), rrow(kp("2"), KindPR, TierStarted, false)), 6, CoverageState{})
	fresh := rankingOf(
		rrow(kb("bd-8"), KindBead, TierOverdue, false), // arrived after the draft
		rrow(kp("1"), KindPR, TierStarted, false),
		rrow(kp("2"), KindPR, TierStarted, false),
		rrow(kp("5"), KindPR, TierStarted, false), // in the plan: shown there, not "new"
	)
	plan := []PlanRow{{Key: kp("5"), RankPosition: 1, SelectedAt: "2026-10-10T08:00:00Z"}}
	v := ReRender(d, fixtureNow.Add(-time.Hour), fixtureNow, LiveFacts{}, fresh, plan)

	if !reflect.DeepEqual(v.NewSinceDraft, []Key{kb("bd-8")}) {
		t.Errorf("NewSinceDraft = %v, want [issue:bd-8]", v.NewSinceDraft)
	}
	for _, r := range v.Rows {
		if r.Key == kb("bd-8") || r.Key == kp("5") {
			t.Errorf("%v added to the draft table", r.Key)
		}
	}
	if len(v.Rows) != 2 {
		t.Errorf("rows = %d, want the draft's 2", len(v.Rows))
	}
}

func TestReRenderUsesDraftOrderAndLiveFacts(t *testing.T) {
	d := NewInitialDraft(rankingOf(
		rrow(kp("1"), KindPR, TierOverdue, false),
		rrow(kp("2"), KindPR, TierStarted, false),
		rrow(kp("3"), KindPR, TierStarted, false),
	), 2, CoverageState{})
	// The fresh rank reverses everything; the order shown is still the draft's.
	fresh := rankingOf(rrow(kp("3"), KindPR, TierOverdue, false), rrow(kp("2"), KindPR, TierOverdue, false), rrow(kp("1"), KindPR, TierOverdue, false))
	live := LiveFacts{kp("2"): {Title: "second", Due: "2026-10-12", Priority: "P1", State: "open"}}
	madeAt := time.Date(2026, 10, 10, 6, 0, 0, 0, time.FixedZone("x", -4*3600))
	v := ReRender(d, madeAt, fixtureNow, live, fresh, nil)

	var got []Key
	for _, r := range v.Rows {
		got = append(got, r.Key)
	}
	if want := []Key{kp("1"), kp("2"), kp("3")}; !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %v, want the draft order %v", got, want)
	}
	if v.Rows[1].Facts.Title != "second" || v.Rows[0].Facts.Title != "" {
		t.Errorf("facts are not the live ones: %+v", v.Rows)
	}
	if !v.Rows[0].Plus || !v.Rows[1].Plus || v.Rows[2].Plus {
		t.Errorf("proposal flags changed: %+v", v.Rows)
	}
	if v.MadeAt != "2026-10-10T10:00:00Z" {
		t.Errorf("MadeAt = %q", v.MadeAt)
	}
	if want := fixtureNow.Sub(madeAt); v.Age != want {
		t.Errorf("Age = %v, want %v", v.Age, want)
	}
	if v.Drift.RowsPlacedDifferently != 2 {
		t.Errorf("RowsPlacedDifferently = %d, want 2 (pr:1 and pr:3 swapped)", v.Drift.RowsPlacedDifferently)
	}
}

func TestReRenderNeverRemovesARow(t *testing.T) {
	d := NewInitialDraft(rankingOf(
		rrow(kp("1"), KindPR, TierOverdue, false),
		rrow(kp("2"), KindPR, TierStarted, false),
		rrow(kp("3"), KindPR, TierStarted, false),
	), 6, CoverageState{})
	// pr:1 finished since; pr:2 left the candidate set; pr:3 is covered.
	fresh := rankingOf(rrow(kp("1"), KindPR, TierOverdue, true))
	fresh.Covered = map[Key]Key{kp("3"): kp("1")}
	live := LiveFacts{kp("2"): {Hidden: true}}
	v := ReRender(d, fixtureNow, fixtureNow, live, fresh, nil)

	if len(v.Rows) != 3 {
		t.Fatalf("rows = %d, want all 3 kept", len(v.Rows))
	}
	if !v.Rows[0].Finished {
		t.Errorf("row finished since the draft is not marked finished in the view")
	}
	if !reflect.DeepEqual(v.Drift.NowFinished, []Key{kp("1")}) {
		t.Errorf("NowFinished = %v", v.Drift.NowFinished)
	}
	if !reflect.DeepEqual(v.Drift.NoLongerCandidate, []Key{kp("2")}) {
		t.Errorf("NoLongerCandidate = %v, want only pr:2 (pr:3 is covered, not gone)", v.Drift.NoLongerCandidate)
	}
	if v.Drift.RowsPlacedDifferently != 0 {
		t.Errorf("RowsPlacedDifferently = %d, want 0: a removal is not a displacement", v.Drift.RowsPlacedDifferently)
	}
	// The stored document is untouched by the view.
	if d.Rows[0].Finished {
		t.Errorf("ReRender mutated the draft")
	}
}

func TestStoredDraftPlanBlockIsReadLive(t *testing.T) {
	_, rk, firstSeen := replanFixture()
	plan, _, _ := replanFixture()
	body, err := EncodeDraft(NewReplanDraft(plan, rk, CoverageState{}, 6, firstSeen))
	if err != nil {
		t.Fatal(err)
	}
	d, err := DecodeDraft(body)
	if err != nil {
		t.Fatal(err)
	}
	// pr:2 is struck after the draft was made: the plan block shows the
	// plan as it is now, never as the draft saw it.
	now := plan[:1]
	pv := BuildPlanView(now, LiveFacts{}, rk, 6)
	if len(pv.Rows) != 1 || pv.Rows[0].Key != kp("1") {
		t.Errorf("plan block = %+v, want only pr:1", pv.Rows)
	}
	for _, r := range pv.Rows {
		if r.Key == kp("2") {
			t.Errorf("struck plan row still shown")
		}
	}
	// And the draft table is unchanged by the plan change: the struck row is
	// not added to it (it is a plain candidate again, "new since draft").
	v := ReRender(d, fixtureNow, fixtureNow, LiveFacts{}, rk, now)
	for _, r := range v.Rows {
		if r.Key == kp("1") || r.Key == kp("2") {
			t.Errorf("plan-related row %v appears in the draft table", r.Key)
		}
	}
	if !reflect.DeepEqual(v.NewSinceDraft, []Key{kp("2")}) {
		t.Errorf("NewSinceDraft = %v, want [pr:2]", v.NewSinceDraft)
	}
}

func TestReRenderAgeNeverNegative(t *testing.T) {
	v := ReRender(DraftDoc{}, fixtureNow.Add(time.Hour), fixtureNow, LiveFacts{}, Ranking{}, nil)
	if v.Age != 0 {
		t.Errorf("Age = %v, want 0 for a made_at after now", v.Age)
	}
}
