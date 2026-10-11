package focus

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// pullFixture is a ranking of four PRs (1 and 2 overdue, 3 started, 4
// finished), all candidates, a plan that holds 2, and an entity row for every
// key except "ghost".
func pullFixture() (Ranking, CandidateSet, LiveFacts, []PlanRow, map[Key]store.Entity) {
	rk := rankingOf(
		rrow(kp("1"), KindPR, TierOverdue, false),
		rrow(kp("2"), KindPR, TierOverdue, false),
		rrow(kp("3"), KindPR, TierStarted, false),
		rrow(kp("4"), KindPR, TierStarted, true),
	)
	set := CandidateSet{}
	ents := map[Key]store.Entity{}
	for _, r := range rk.Rows {
		set.Candidates = append(set.Candidates, r.Candidate)
		ents[r.Key] = store.Entity{}
	}
	ents[kp("hidden")] = store.Entity{}
	ents[kp("closed")] = store.Entity{}
	ents[kp("other")] = store.Entity{}
	facts := LiveFacts{
		kp("hidden"): {Hidden: true},
		kp("closed"): {Finished: true},
	}
	plan := []PlanRow{{Key: kp("2"), RankPosition: 1}}
	return rk, set, facts, plan, ents
}

func keysOf(ins []Insertion) []Key {
	var out []Key
	for _, i := range ins {
		out = append(out, i.Key)
	}
	return out
}

func TestResolvePullTopTakesTheBestRankedNotYetSelectedAndNeverFinished(t *testing.T) {
	rk, set, facts, plan, ents := pullFixture()
	got := ResolvePull(rk, set, facts, plan, ents, PullRequest{Top: 2})
	// 2 is in the plan and 4 is finished: the next two by rank are 1 and 3.
	if want := []Key{kp("1"), kp("3")}; !reflect.DeepEqual(keysOf(got.Select), want) {
		t.Fatalf("Select = %v, want %v", keysOf(got.Select), want)
	}
	if got.Select[0].Source != SourcePull || got.Select[0].Tier == nil || *got.Select[0].Tier != TierOverdue || *got.Select[1].Tier != TierStarted {
		t.Errorf("Select = %+v, want the pull source and the tier of the run", got.Select)
	}
	if len(got.Skipped) != 0 || got.AllMissing {
		t.Errorf("--top reports no skips: %+v", got)
	}
	// Fewer candidates than asked: what exists, no error.
	if got := ResolvePull(rk, set, facts, plan, ents, PullRequest{Top: 10}); len(got.Select) != 2 {
		t.Errorf("Select = %v, want the 2 that exist", keysOf(got.Select))
	}
}

func TestResolvePullTopSkipsHiddenAndInactiveRows(t *testing.T) {
	rk, set, facts, plan, ents := pullFixture()
	facts[kp("1")] = Facts{Hidden: true}
	facts[kp("3")] = Facts{Inactive: true}
	got := ResolvePull(rk, set, facts, nil, ents, PullRequest{Top: 5})
	if want := []Key{kp("2")}; !reflect.DeepEqual(keysOf(got.Select), want) {
		t.Fatalf("Select = %v, want %v", keysOf(got.Select), want)
	}
	_ = plan
}

func TestResolvePullNamedKeysSkipWithTheRightReason(t *testing.T) {
	rk, set, facts, plan, ents := pullFixture()
	got := ResolvePull(rk, set, facts, plan, ents, PullRequest{Keys: []Key{
		kp("3"), kp("2"), kp("closed"), kp("hidden"), kp("other"), kp("4"), kp("ghost"), kp("1"), kp("3"),
	}})
	if want := []Key{kp("1"), kp("3")}; !reflect.DeepEqual(keysOf(got.Select), want) {
		t.Fatalf("Select = %v, want %v in rank order (3 was named first)", keysOf(got.Select), want)
	}
	wantSkips := []Skip{
		{kp("2"), SkipAlreadySelected},
		{kp("closed"), SkipNoLongerCandidate},
		{kp("hidden"), SkipNoLongerCandidate},
		{kp("other"), SkipNotACandidate},
		{kp("4"), SkipNoLongerCandidate},
		{kp("ghost"), SkipNotACandidate},
	}
	if !reflect.DeepEqual(got.Skipped, wantSkips) {
		t.Errorf("Skipped = %+v, want %+v", got.Skipped, wantSkips)
	}
	if got.AllMissing {
		t.Errorf("AllMissing with entity rows present")
	}
}

func TestResolvePullAllMissingOnlyWhenEveryNamedKeyHasNoEntityRow(t *testing.T) {
	rk, set, facts, plan, ents := pullFixture()
	got := ResolvePull(rk, set, facts, plan, ents, PullRequest{Keys: []Key{kp("ghost"), kp("phantom")}})
	if !got.AllMissing || len(got.Select) != 0 || len(got.Skipped) != 2 {
		t.Errorf("got %+v, want AllMissing with 2 skips", got)
	}
	// A named key that is merely already selected is NOT a total failure.
	got = ResolvePull(rk, set, facts, plan, ents, PullRequest{Keys: []Key{kp("2")}})
	if got.AllMissing || len(got.Skipped) != 1 || got.Skipped[0].Reason != SkipAlreadySelected {
		t.Errorf("got %+v, want a routine skip", got)
	}
	// No keys and no count is nothing named: never AllMissing.
	if got := ResolvePull(rk, set, facts, plan, ents, PullRequest{}); got.AllMissing || len(got.Select) != 0 {
		t.Errorf("an empty request selected or failed: %+v", got)
	}
}

func TestResolvePullKeyTheRankDoesNotPlaceIsSelectedWithoutATierAfterTheRanked(t *testing.T) {
	rk, set, facts, plan, ents := pullFixture()
	// 9 is a candidate the slot rule or a group suppressed: no ranked row.
	set.Candidates = append(set.Candidates, Candidate{Key: kp("9"), Kind: KindPR})
	ents[kp("9")] = store.Entity{}
	got := ResolvePull(rk, set, facts, plan, ents, PullRequest{Keys: []Key{kp("9"), kp("1")}})
	if want := []Key{kp("1"), kp("9")}; !reflect.DeepEqual(keysOf(got.Select), want) {
		t.Fatalf("Select = %v, want %v", keysOf(got.Select), want)
	}
	if got.Select[1].Tier != nil {
		t.Errorf("tier of an unranked key = %v, want none", *got.Select[1].Tier)
	}
}

// ------------------------------------------------------------ the transaction

func pullIn(keys ...Key) PullInput {
	var ins []Insertion
	for _, k := range keys {
		ins = append(ins, Insertion{Key: k, Source: SourcePull, Tier: tierPtr(TierStarted)})
	}
	return PullInput{Period: Period{Type: "day", Key: lockToday}, Actor: "tester", Select: ins, RunID: "RUNP"}
}

func TestPullTransactionTakesMaxPlusOneAndWritesAnnotationsAndTheRunRow(t *testing.T) {
	f := newFixture(t)
	a, b, c := f.pr("1", prSpec{ownership: "mine"}), f.pr("2", prSpec{ownership: "mine"}), f.pr("3", prSpec{ownership: "mine"})
	d := lockDeps(t, f)
	// An earlier lock with a gap: positions 1 and 4.
	if err := f.st.FocusLockTx(func(tx *store.FocusTx) error {
		p, err := tx.GetOrCreatePeriod("day", lockToday)
		if err != nil {
			return err
		}
		for _, r := range []struct {
			k   Key
			pos int
		}{{a, 1}, {b, 4}} {
			if _, err := tx.InsertSelection(store.FocusSelection{FocusPeriodID: p.ID, Repo: testRepo, EntityType: r.k.Type, EntityID: r.k.ID, SelectedAt: fixtureAt, RankPosition: r.pos}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	res, err := Pull(context.Background(), d, pullIn(c))
	if err != nil || res.ExitCode != ExitOK || res.Outcome != OutcomeOK || len(res.Inserted) != 1 {
		t.Fatalf("Pull = %+v, %v", res, err)
	}
	p, _, _ := f.st.FocusPeriodGet("day", lockToday)
	rows, _ := f.st.FocusPlanRows(p.ID)
	if len(rows) != 3 || rows[2].EntityID != "3" || rows[2].RankPosition != 5 || rows[2].Tier == nil || *rows[2].Tier != TierStarted {
		t.Fatalf("rows = %+v, want the pulled row at max plus one (5) with the run's tier", rows)
	}
	if v, _ := f.st.FocusAnnotationValue(testRepo, c.Type, c.ID); v != lockToday {
		t.Errorf("annotation of the pulled row = %q", v)
	}
	if len(res.Annotations) != 1 || res.Annotations[0].Outcome != AnnotationOK || res.Annotations[0].Seq == 0 {
		t.Errorf("annotations = %+v", res.Annotations)
	}
	if stats, _ := f.st.FocusRunStats(); stats.ByVerbOutcome[[2]string{"pull", "total"}] != 1 {
		t.Errorf("run stats = %v, want the unfinalised pull row", stats.ByVerbOutcome)
	}
}

func TestPullTransactionIsAtomicAndLeavesTheDraft(t *testing.T) {
	f := newFixture(t)
	a := f.pr("1", prSpec{ownership: "mine"})
	d := lockDeps(t, f)
	if _, err := f.st.FocusDraftReplace(store.FocusDraft{PeriodType: "day", PeriodKey: lockToday, Cap: 6, MadeAt: fixtureAt, BodyJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	before := dumpFocus(t, f.st)
	// The second key has no entity row: the foreign key fails after the first
	// row was written, and everything rolls back, the period row and run too.
	res, err := Pull(context.Background(), d, pullIn(a, Key{"pr", "ghost"}))
	if err == nil || res.ExitCode != ExitTotal || res.Outcome != OutcomeTotal || len(res.Inserted) != 0 {
		t.Fatalf("Pull = %+v, %v; want exit 3", res, err)
	}
	if got := dumpFocus(t, f.st); got != before {
		t.Fatalf("a failed pull left state behind:\n%s\n---\n%s", before, got)
	}
	if res, err := Pull(context.Background(), d, pullIn(a)); err != nil || res.ExitCode != ExitOK {
		t.Fatalf("Pull = %+v, %v", res, err)
	}
	if dr, found, _ := f.st.FocusDraftGet(); !found || dr.PeriodKey != lockToday {
		t.Errorf("the pull deleted or changed the stored draft: %+v found=%v", dr, found)
	}
}

func TestPullTransactionRefusesClosedFutureAndNoPlanPeriods(t *testing.T) {
	f := newFixture(t)
	a := f.pr("1", prSpec{ownership: "mine"})
	old := f.pr("9", prSpec{ownership: "mine"})
	d := lockDeps(t, f)

	// Future.
	in := pullIn(a)
	in.Period.Key = "2026-10-11"
	res, err := Pull(context.Background(), d, in)
	var ue *UsageError
	if !errors.As(err, &ue) || res.ExitCode != ExitUsage {
		t.Fatalf("future: %+v, %v; want a usage error", res, err)
	}
	if _, found, _ := f.st.FocusPeriodGet("day", "2026-10-11"); found {
		t.Errorf("the refused pull left the future period behind")
	}

	// No plan while another period holds rows.
	if err := f.st.FocusLockTx(func(tx *store.FocusTx) error {
		p, err := tx.GetOrCreatePeriod("day", "2026-10-09")
		if err != nil {
			return err
		}
		_, err = tx.InsertSelection(store.FocusSelection{FocusPeriodID: p.ID, Repo: testRepo, EntityType: old.Type, EntityID: old.ID, SelectedAt: fixtureAt, RankPosition: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	res, err = Pull(context.Background(), d, pullIn(a))
	if !errors.As(err, &ue) || ue.Remedy != PullNoPlanRemedy || res.ExitCode != ExitUsage {
		t.Fatalf("no plan: %+v, %v; want the usage error with the remedy", res, err)
	}
	if err := PullPreflight(d, Period{Type: "day", Key: lockToday}); !errors.As(err, &ue) || ue.Remedy != PullNoPlanRemedy {
		t.Errorf("preflight = %v, want the same usage error", err)
	}
	if _, found, _ := f.st.FocusPeriodGet("day", lockToday); found {
		t.Errorf("the refused pull left a period row behind")
	}

	// Closed.
	if err := f.st.FocusLockTx(func(tx *store.FocusTx) error {
		p, err := tx.GetOrCreatePeriod("day", lockToday)
		if err != nil {
			return err
		}
		return tx.ClosePeriod(p.ID, fixtureAt, "done")
	}); err != nil {
		t.Fatal(err)
	}
	res, err = Pull(context.Background(), d, pullIn(a))
	if !errors.Is(err, ErrPeriodClosed) || res.ExitCode != ExitPeriodClosed {
		t.Fatalf("closed: %+v, %v; want exit 6", res, err)
	}
	if err := PullPreflight(d, Period{Type: "day", Key: lockToday}); !errors.Is(err, ErrPeriodClosed) {
		t.Errorf("preflight = %v, want ErrPeriodClosed", err)
	}
}

func TestPullTransactionReportsAKeyAConcurrentPullAddedAsRaced(t *testing.T) {
	f := newFixture(t)
	a, b := f.pr("1", prSpec{ownership: "mine"}), f.pr("2", prSpec{ownership: "mine"})
	d := lockDeps(t, f)
	if res, err := Pull(context.Background(), d, pullIn(a)); err != nil || len(res.Inserted) != 1 {
		t.Fatalf("first pull: %+v, %v", res, err)
	}
	in := pullIn(a, b)
	in.RunID = "RUNQ"
	res, err := Pull(context.Background(), d, in)
	if err != nil || res.ExitCode != ExitOK {
		t.Fatalf("second pull: %+v, %v", res, err)
	}
	if !reflect.DeepEqual(res.Raced, []Key{a}) || len(res.Inserted) != 1 || res.Inserted[0].Key != b {
		t.Errorf("Raced = %v, Inserted = %v, want a raced and b inserted", res.Raced, keysOf(res.Inserted))
	}
	p, _, _ := f.st.FocusPeriodGet("day", lockToday)
	rows, _ := f.st.FocusPlanRows(p.ID)
	if len(rows) != 2 || rows[1].RankPosition != 2 {
		t.Errorf("rows = %+v", rows)
	}
}

func TestPullTransactionWithNothingToAddIsEmptyOutcome(t *testing.T) {
	f := newFixture(t)
	d := lockDeps(t, f)
	res, err := Pull(context.Background(), d, pullIn())
	if err != nil || res.Outcome != OutcomeEmpty || res.ExitCode != ExitOK {
		t.Fatalf("Pull = %+v, %v", res, err)
	}
}
