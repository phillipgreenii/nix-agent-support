package focus

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const lockToday = "2026-10-10"

func lockDeps(t *testing.T, f *fixture) LockDeps {
	t.Helper()
	f.cfg.Repos = []config.RepoConfig{{Remote: testRepo}}
	f.cfg.Focus.TimeZone = "UTC"
	return LockDeps{
		Store: f.st, Annotator: newAnnotator(t, f.st), Config: f.cfg,
		Now: func() time.Time { return fixtureNow }, Actor: "tester",
	}
}

func lockIn(fs FinalSet) LockInput {
	return LockInput{Period: Period{Type: "day", Key: lockToday}, Actor: "tester", Apply: true, Final: fs, RunID: "RUN1"}
}

func dumpFocus(t *testing.T, s *store.Store) string {
	t.Helper()
	var sb strings.Builder
	rows, err := s.FocusRowsAllPeriods()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		sb.WriteString(r.EntityID + "|" + r.SelectedAt + "\n")
	}
	ps, _ := s.FocusPeriods()
	for _, p := range ps {
		sb.WriteString(p.PeriodKey + "\n")
	}
	if d, found, _ := s.FocusDraftGet(); found {
		sb.WriteString("draft " + d.PeriodKey + "\n")
	}
	st, _ := s.FocusRunStats()
	for k, n := range st.ByVerbOutcome {
		sb.WriteString(k[0] + k[1] + string(rune('0'+n)) + "\n")
	}
	return sb.String()
}

func TestLockInsertsInLockOrderAnnotatesAndWritesTheRunRow(t *testing.T) {
	f := newFixture(t)
	a, b := f.pr("1", prSpec{ownership: "mine"}), f.pr("2", prSpec{ownership: "mine"})
	d := lockDeps(t, f)
	if _, err := f.st.FocusDraftReplace(store.FocusDraft{PeriodType: "day", PeriodKey: lockToday, Cap: 6, MadeAt: fixtureAt, BodyJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	six := 6
	fs := FinalSet{
		Insert:     []Insertion{{Key: a, Source: SourceDraft, Tier: tierPtr(TierStarted)}, {Key: b, Source: SourceHandAdd}},
		PersistCap: &six,
	}
	res, err := Lock(context.Background(), d, lockIn(fs))
	if err != nil || res.ExitCode != ExitOK || res.Outcome != OutcomeOK {
		t.Fatalf("Lock = %+v, %v", res, err)
	}
	p, _, _ := f.st.FocusPeriodGet("day", lockToday)
	rows, _ := f.st.FocusPlanRows(p.ID)
	if len(rows) != 2 || rows[0].RankPosition != 1 || rows[1].RankPosition != 2 || rows[0].Tier == nil || rows[1].Tier != nil {
		t.Fatalf("rows = %+v, want 1..2 and a NULL tier for the hand-add", rows)
	}
	if p.Cap == nil || *p.Cap != 6 {
		t.Errorf("cap = %v", p.Cap)
	}
	if _, found, _ := f.st.FocusDraftGet(); found {
		t.Errorf("the lock left the draft")
	}
	for _, k := range []Key{a, b} {
		if v, _ := f.st.FocusAnnotationValue(testRepo, k.Type, k.ID); v != lockToday {
			t.Errorf("annotation value of %v = %q", k, v)
		}
	}
	if len(res.Annotations) != 2 || res.Annotations[0].Seq == 0 {
		t.Errorf("annotations = %+v", res.Annotations)
	}
	stats, _ := f.st.FocusRunStats()
	if stats.ByVerbOutcome[[2]string{"select", "total"}] != 1 {
		// the row exists with exit_code NULL until the verb finalizes it
		t.Errorf("run stats = %v, want the unfinalised row counted as total", stats.ByVerbOutcome)
	}
}

func TestLockDeletesTheDraftInTheSameTransaction(t *testing.T) {
	f := newFixture(t)
	f.pr("1", prSpec{ownership: "mine"})
	d := lockDeps(t, f)
	if _, err := f.st.FocusDraftReplace(store.FocusDraft{PeriodType: "day", PeriodKey: lockToday, Cap: 6, MadeAt: fixtureAt, BodyJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	before := dumpFocus(t, f.st)
	// The second insertion names an entity the store never gathered: the
	// foreign key fails after the first row was written.
	fs := FinalSet{Insert: []Insertion{{Key: Key{"pr", "1"}, Source: SourceDraft}, {Key: Key{"pr", "ghost"}, Source: SourceHandAdd}}}
	res, err := Lock(context.Background(), d, lockIn(fs))
	if err == nil || res.ExitCode != ExitTotal || res.Outcome != OutcomeTotal {
		t.Fatalf("Lock = %+v, %v; want exit 3", res, err)
	}
	if got := dumpFocus(t, f.st); got != before {
		t.Fatalf("a failed lock left state behind (rows, period, draft and run all roll back together):\n%s\n---\n%s", before, got)
	}
	// The same lock without the bad row commits all of it, draft deletion
	// included.
	if res, err := Lock(context.Background(), d, lockIn(FinalSet{Insert: []Insertion{{Key: Key{"pr", "1"}, Source: SourceDraft}}})); err != nil || res.ExitCode != ExitOK {
		t.Fatalf("Lock = %+v, %v", res, err)
	}
	if _, found, _ := f.st.FocusDraftGet(); found {
		t.Errorf("the committed lock left the draft")
	}
}

func TestLockPreviewWritesNothing(t *testing.T) {
	f := newFixture(t)
	a := f.pr("1", prSpec{ownership: "mine"})
	d := lockDeps(t, f)
	before := dumpFocus(t, f.st)
	for _, mod := range []func(*LockInput){func(in *LockInput) { in.Apply = false }, func(in *LockInput) { in.DryRun = true }} {
		in := lockIn(FinalSet{Insert: []Insertion{{Key: a, Source: SourceDraft}}})
		mod(&in)
		res, err := Lock(context.Background(), d, in)
		if err != nil || res.Outcome != OutcomePreview || len(res.Preview.Changes) != 1 || res.Preview.Changes[0].Effect != "mint" {
			t.Fatalf("preview = %+v, %v", res, err)
		}
	}
	if got := dumpFocus(t, f.st); got != before {
		t.Errorf("a preview wrote state")
	}
}

func TestLockSeesAChangedPlanOrDraftAndWritesNothing(t *testing.T) {
	f := newFixture(t)
	a, b := f.pr("1", prSpec{ownership: "mine"}), f.pr("2", prSpec{ownership: "mine"})
	d := lockDeps(t, f)
	// The caller read an empty plan, but a row appeared.
	if err := f.st.FocusLockTx(func(tx *store.FocusTx) error {
		p, err := tx.GetOrCreatePeriod("day", lockToday)
		if err != nil {
			return err
		}
		_, err = tx.InsertSelection(store.FocusSelection{FocusPeriodID: p.ID, Repo: testRepo, EntityType: "pr", EntityID: "2", SelectedAt: fixtureAt, RankPosition: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before := dumpFocus(t, f.st)
	_, err := Lock(context.Background(), d, lockIn(FinalSet{Insert: []Insertion{{Key: a}}}))
	if !errors.Is(err, ErrPlanChanged) {
		t.Fatalf("err = %v, want ErrPlanChanged", err)
	}
	// The caller used a stored draft that has since been consumed.
	in := lockIn(FinalSet{})
	in.Seen = Seen{Plan: []PlanRow{{Key: b}}, Draft: &store.FocusDraft{MadeAt: fixtureAt, BodyJSON: "{}"}}
	if _, err := Lock(context.Background(), d, in); !errors.Is(err, ErrPlanChanged) {
		t.Fatalf("err = %v, want ErrPlanChanged for a vanished draft", err)
	}
	if got := dumpFocus(t, f.st); got != before {
		t.Errorf("a stale lock wrote state")
	}
}

func TestLockOfAClosedPeriodIsExitSix(t *testing.T) {
	f := newFixture(t)
	a := f.pr("1", prSpec{ownership: "mine"})
	d := lockDeps(t, f)
	if err := f.st.FocusLockTx(func(tx *store.FocusTx) error {
		p, err := tx.GetOrCreatePeriod("day", lockToday)
		if err != nil {
			return err
		}
		return tx.ClosePeriod(p.ID, fixtureAt, "done")
	}); err != nil {
		t.Fatal(err)
	}
	res, err := Lock(context.Background(), d, lockIn(FinalSet{Insert: []Insertion{{Key: a}}}))
	if !errors.Is(err, ErrPeriodClosed) || res.ExitCode != ExitPeriodClosed || res.Outcome != OutcomePeriodClosed {
		t.Fatalf("Lock = %+v, %v", res, err)
	}
}

func TestDecideFirstLock(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		first, nonEmpty       bool
		key, today            string
		otherHolds            bool
		maxOther              string
		wantReplace           bool
		wantErrContains       string
		wantErrRemedyContains string
	}{
		{name: "plan exists", first: false, nonEmpty: true, key: "2026-10-10", today: "2026-10-10"},
		{name: "empty first lock replaces nothing", first: true, key: "2026-10-10", today: "2026-10-10", otherHolds: true, maxOther: "2026-10-09"},
		{name: "empty first lock of the future is no error", first: true, key: "2026-10-12", today: "2026-10-10"},
		{name: "replaces a plan", first: true, nonEmpty: true, key: "2026-10-10", today: "2026-10-10", otherHolds: true, maxOther: "2026-10-09", wantReplace: true},
		{name: "no other plan", first: true, nonEmpty: true, key: "2026-10-10", today: "2026-10-10"},
		{name: "earlier", first: true, nonEmpty: true, key: "2026-10-08", today: "2026-10-10", otherHolds: true, maxOther: "2026-10-09", wantErrContains: "is earlier than the current plan (2026-10-09); history is not rewritten"},
		{name: "same key as the newest other", first: true, nonEmpty: true, key: "2026-10-09", today: "2026-10-10", otherHolds: true, maxOther: "2026-10-09", wantErrContains: "earlier than the current plan"},
		{name: "future", first: true, nonEmpty: true, key: "2026-10-11", today: "2026-10-10", wantErrContains: "period 2026-10-11 is in the future", wantErrRemedyContains: "plan it on the day"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			replace, err := decideFirstLock(tc.first, tc.nonEmpty, tc.key, tc.today, tc.otherHolds, tc.maxOther)
			if tc.wantErrContains != "" {
				var ue *UsageError
				if !errors.As(err, &ue) || !strings.Contains(ue.Msg, tc.wantErrContains) || !strings.Contains(ue.Remedy, tc.wantErrRemedyContains) {
					t.Fatalf("err = %v, want a usage error containing %q", err, tc.wantErrContains)
				}
				return
			}
			if err != nil || replace != tc.wantReplace {
				t.Fatalf("replace = %v, err = %v; want %v", replace, err, tc.wantReplace)
			}
		})
	}
}

func TestNextRankPositionIsMaxPlusOne(t *testing.T) {
	f := newFixture(t)
	f.pr("1", prSpec{ownership: "mine"})
	f.pr("2", prSpec{ownership: "mine"})
	err := f.st.FocusLockTx(func(tx *store.FocusTx) error {
		p, err := tx.GetOrCreatePeriod("day", lockToday)
		if err != nil {
			return err
		}
		if n, _ := NextRankPosition(tx, p.ID); n != 1 {
			t.Errorf("empty period: next = %d, want 1", n)
		}
		for _, r := range []struct {
			id  string
			pos int
		}{{"1", 5}, {"2", 9}} {
			if _, err := tx.InsertSelection(store.FocusSelection{FocusPeriodID: p.ID, Repo: testRepo, EntityType: "pr", EntityID: r.id, SelectedAt: fixtureAt, RankPosition: r.pos}); err != nil {
				return err
			}
		}
		if n, _ := NextRankPosition(tx, p.ID); n != 10 {
			t.Errorf("next = %d, want max plus one (10)", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestApplyAnnotationsReportsAFailureAndKeepsGoing(t *testing.T) {
	f := newFixture(t)
	a, b := f.pr("1", prSpec{ownership: "mine"}), f.pr("2", prSpec{ownership: "mine"})
	d := lockDeps(t, f)
	d.Annotator = failingAnnotator{inner: d.Annotator, failID: "1"}
	got := ApplyAnnotations(context.Background(), d, []Key{a, b}, fixtureAt)
	if len(got) != 2 || got[0].Outcome != AnnotationFailed || got[0].Seq != 0 || got[1].Outcome != AnnotationOK || got[1].Seq == 0 {
		t.Fatalf("outcomes = %+v", got)
	}
}

func TestBuildPreviewResolvesEffectAndBecauseFromTheBeadStateTable(t *testing.T) {
	f := newFixture(t)
	src := f.pr("1", prSpec{ownership: "mine"})
	claimed := f.pr("2", prSpec{ownership: "mine"})
	bead := f.issue("bd-10", issueSpec{state: "open", metadata: map[string]string{}})
	cbead := f.issue("bd-11", issueSpec{state: "in_progress", assignee: "worker-1"})
	f.link(bead, src, relationSource)
	f.link(cbead, claimed, relationSource)
	f.pr("3", prSpec{ownership: "mine"})
	f.inputs() // flushes the derived links into the store
	d := lockDeps(t, f)
	pv, err := BuildPreview(context.Background(), d, lockIn(FinalSet{
		Delete: []Deletion{{Key: src, Cause: CauseOperator}, {Key: claimed, Cause: CauseCap}},
		Insert: []Insertion{{Key: Key{"pr", "3"}, Source: SourceDraft}},
	}), FinalSet{
		Delete: []Deletion{{Key: src, Cause: CauseOperator}, {Key: claimed, Cause: CauseCap}},
		Insert: []Insertion{{Key: Key{"pr", "3"}, Source: SourceDraft}},
	})
	if err != nil {
		t.Fatal(err)
	}
	by := map[Key]ChangeRow{}
	for _, c := range pv.Changes {
		by[c.Key] = c
	}
	if c := by[src]; c.Effect != "hold" || c.BeadID != "bd-10" || c.State != StateOpen || c.CauseText != "you" || c.Echo != "will hold" {
		t.Errorf("strike of a source with an open bead = %+v", c)
	}
	if c := by[claimed]; c.Effect != "none" || c.BeadID != "bd-11" || c.Echo != "claimed by worker-1, left running" || c.CauseText != "cap" {
		t.Errorf("strike of a source with a claimed bead = %+v", c)
	}
	if c := by[Key{"pr", "3"}]; c.Effect != "mint" || c.Echo != "will mint" || c.Direction != DirectionSelect {
		t.Errorf("selection of a new source = %+v", c)
	}
	if pv.Changes[0].Direction != DirectionSelect {
		t.Errorf("selections come first: %+v", pv.Changes)
	}
}

func TestHydrateHandAddsOnlyHydratesUnknownKeysAndNamesAFailure(t *testing.T) {
	f := newFixture(t)
	known := f.pr("1", prSpec{ownership: "mine"})
	in := f.inputs()
	d := lockDeps(t, f)
	var called []Key
	d.Hydrate = func(_ context.Context, k Key) error { called = append(called, k); return nil }
	got, err := HydrateHandAdds(context.Background(), d, []Key{known, {Type: "issue", ID: "X-1"}}, in)
	if err != nil || len(got) != 1 || len(called) != 1 || called[0].ID != "X-1" {
		t.Fatalf("hydrated = %v, called = %v, err = %v", got, called, err)
	}
	d.Hydrate = func(context.Context, Key) error { return errors.New("not found") }
	_, err = HydrateHandAdds(context.Background(), d, []Key{{Type: "issue", ID: "X-2"}}, in)
	var ue *UsageError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "X-2") || !strings.Contains(ue.Msg, "hydration may already have written store rows") {
		t.Fatalf("err = %v, want a usage error naming the key and the hydration caveat", err)
	}
}
