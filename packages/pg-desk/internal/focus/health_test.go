package focus

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// healthWorld is a fixture with one configured repository, UTC focus time
// zone and helpers that write plan rows, annotations and focus beads the way
// the verbs and the decider leave them.
type healthWorld struct {
	*fixture
}

func newHealthWorld(t *testing.T) *healthWorld {
	t.Helper()
	f := newFixture(t)
	f.cfg.Repos = []config.RepoConfig{{Remote: testRepo}}
	f.cfg.Focus.TimeZone = "UTC"
	return &healthWorld{f}
}

// row writes one plan row of the day period key, with its focus_selected
// annotation set to the period key (what a lock leaves).
func (w *healthWorld) row(periodKey string, k Key, pos int, selectedAt string) {
	w.t.Helper()
	w.rowOnly(periodKey, k, pos, selectedAt)
	w.annotate(k, periodKey)
}

// rowOnly writes the plan row without the annotation (a crash between the
// two).
func (w *healthWorld) rowOnly(periodKey string, k Key, pos int, selectedAt string) {
	w.t.Helper()
	err := w.st.FocusLockTx(func(tx *store.FocusTx) error {
		p, err := tx.GetOrCreatePeriod("day", periodKey)
		if err != nil {
			return err
		}
		_, err = tx.InsertSelection(store.FocusSelection{
			FocusPeriodID: p.ID, Repo: testRepo, EntityType: k.Type, EntityID: k.ID,
			SelectedAt: selectedAt, RankPosition: pos,
		})
		return err
	})
	if err != nil {
		w.t.Fatalf("row %v: %v", k, err)
	}
}

func (w *healthWorld) annotate(k Key, value string) {
	w.t.Helper()
	if err := w.st.SetAnnotation(store.KVAnnotation{
		Repo: testRepo, EntityType: k.Type, EntityID: k.ID, Key: store.AnnotationFocusSelected,
		Value: value, Origin: "pg-desk", SetBy: "operator", SetAt: fixtureAt,
	}); err != nil {
		w.t.Fatalf("annotate %v: %v", k, err)
	}
}

// beadSpec describes a minted focus bead.
type beadSpec struct {
	state, assignee, hold, createdAt string
	labels                           []string
}

// bead stores a minted focus bead for source (its metadata names the source
// and a link relation source joins them, as the link extractor stores it).
func (w *healthWorld) bead(id string, source Key, s beadSpec) Key {
	w.t.Helper()
	md := map[string]string{"source_id": source.ID, "source_type": source.Type}
	if s.hold != "" {
		md["focus_hold"] = s.hold
	}
	k := w.issue(id, issueSpec{state: s.state, assignee: s.assignee, metadata: md, labels: s.labels, createdAt: s.createdAt})
	w.link(k, source, relationSource)
	return k
}

func (w *healthWorld) health() Health {
	w.t.Helper()
	w.inputs() // flushes the fixture's derived links
	h, err := ComputeHealth(w.st, w.cfg, fixtureNow)
	if err != nil {
		w.t.Fatalf("ComputeHealth: %v", err)
	}
	return h
}

func wantDivergence(t *testing.T, h Health, want map[string]int) {
	t.Helper()
	full := zeroCounts(HealthDivergenceKinds)
	for k, n := range want {
		full[k] = n
	}
	if !reflect.DeepEqual(h.Divergence, full) {
		t.Errorf("divergence = %v, want %v", h.Divergence, full)
	}
}

const (
	today     = "2026-10-10"
	yesterday = "2026-10-09"
)

func TestComputeHealthOnAnUnmigratedStoreOrNoRepoIsZero(t *testing.T) {
	h, err := ComputeHealth(store.OpenForTest(t), &config.Config{Repos: []config.RepoConfig{{Remote: testRepo}}}, fixtureNow)
	if err != nil || h.Migrated() || h.Beads != nil {
		t.Fatalf("unmigrated: %+v, %v; want the zero Health and no error", h, err)
	}
	f := newFixture(t) // a new-schema store but no repository configured
	h, err = ComputeHealth(f.st, f.cfg, fixtureNow)
	if err != nil || h.Migrated() {
		t.Fatalf("no repo: %+v, %v; want the zero Health and no error", h, err)
	}
	if _, err := ComputeHealth(nil, f.cfg, fixtureNow); err == nil {
		t.Errorf("a nil store must be an error")
	}
}

func TestComputeHealthOnAnEmptyMigratedStoreIsMigratedAndAllZero(t *testing.T) {
	w := newHealthWorld(t)
	h := w.health()
	if !h.Migrated() || h.Selected != 0 || h.DraftAge != nil || h.RunAge != nil || h.AuditCommentsToday != 0 {
		t.Fatalf("health = %+v", h)
	}
	for _, n := range h.Beads {
		if n != 0 {
			t.Errorf("beads = %v", h.Beads)
		}
	}
	if len(h.OldestBeadAge) != 0 || len(h.OldestPendingAge) != 0 {
		t.Errorf("ages = %v %v, want none", h.OldestBeadAge, h.OldestPendingAge)
	}
	wantDivergence(t, h, nil)
	if !h.IdentityListEmpty == (len(w.cfg.FocusOperatorIdentities()) == 0) {
		t.Errorf("IdentityListEmpty = %v", h.IdentityListEmpty)
	}
}

func TestSelectedIsTheNewestPeriodThatHoldsRowsNotTheLocalDay(t *testing.T) {
	w := newHealthWorld(t)
	a, b, c := w.pr("1", prSpec{ownership: "mine"}), w.pr("2", prSpec{ownership: "mine"}), w.pr("3", prSpec{ownership: "mine"})
	w.row("2026-10-07", a, 1, fixtureAt)
	w.row("2026-10-08", b, 1, fixtureAt)
	w.row("2026-10-08", c, 2, fixtureAt)
	// A newer period that holds no row (an emptied or just-opened period) is
	// not "the current plan".
	if _, err := w.st.GetOrCreateFocusPeriod("day", today); err != nil {
		t.Fatal(err)
	}
	h := w.health()
	if h.Selected != 2 {
		t.Errorf("Selected = %d, want the two rows of 2026-10-08", h.Selected)
	}
}

func TestFinishedRowIsNotCountedAsHeldOrUnminted(t *testing.T) {
	w := newHealthWorld(t)
	// A merged PR whose bead is held by the terminal-source hold, a merged PR
	// with no bead, and a merged PR whose bead is still open and not held.
	heldSrc := w.pr("1", prSpec{ownership: "mine", state: "closed", merged: true})
	noBead := w.pr("2", prSpec{ownership: "mine", state: "closed", merged: true})
	openSrc := w.pr("3", prSpec{ownership: "mine", state: "closed", merged: true})
	for i, k := range []Key{heldSrc, noBead, openSrc} {
		w.row(today, k, i+1, fixtureAt)
	}
	w.bead("bd-held", heldSrc, beadSpec{state: "deferred", hold: "struck"})
	w.bead("bd-open", openSrc, beadSpec{state: "open"})

	h := w.health()
	if h.Pending[PendingSourceTerminal] != 3 || h.Pending[PendingUnminted] != 0 {
		t.Errorf("pending = %v, want 3 source_terminal and 0 unminted", h.Pending)
	}
	wantDivergence(t, h, map[string]int{DivergenceTerminalSourceBeadOpen: 1})
	if h.PlanRows["finished"] != 3 || h.PlanRows["in_plan"] != 0 || h.PlanRows["open_slots"] != DefaultCap {
		t.Errorf("plan rows = %v", h.PlanRows)
	}
}

func TestEachDivergenceKindIsTrippedByItsOwnFixture(t *testing.T) {
	cases := []struct {
		name  string
		setup func(w *healthWorld)
		want  map[string]int
	}{
		{"row without annotation", func(w *healthWorld) {
			w.rowOnly(today, w.pr("1", prSpec{ownership: "mine"}), 1, fixtureAt)
		}, map[string]int{DivergenceRowWithoutAnnotation: 1}},
		{"row with a mismatched annotation", func(w *healthWorld) {
			k := w.pr("1", prSpec{ownership: "mine"})
			w.rowOnly(today, k, 1, fixtureAt)
			w.annotate(k, yesterday)
		}, map[string]int{DivergenceRowWithoutAnnotation: 1}},
		{"annotation without row", func(w *healthWorld) {
			w.annotate(w.pr("1", prSpec{ownership: "mine"}), today)
		}, map[string]int{DivergenceAnnotationWithoutRow: 1}},
		{"annotation none is not a divergence", func(w *healthWorld) {
			w.annotate(w.pr("1", prSpec{ownership: "mine"}), store.FocusAnnotationNone)
		}, nil},
		{"stale period older than the newest", func(w *healthWorld) {
			w.row(yesterday, w.pr("1", prSpec{ownership: "mine"}), 1, fixtureAt)
			w.row(today, w.pr("2", prSpec{ownership: "mine"}), 1, fixtureAt)
		}, map[string]int{DivergenceStalePeriod: 1}},
		{"stale period older than yesterday", func(w *healthWorld) {
			w.row("2026-10-07", w.pr("1", prSpec{ownership: "mine"}), 1, fixtureAt)
		}, map[string]int{DivergenceStalePeriod: 1}},
		{"yesterday's open period alone is not stale", func(w *healthWorld) {
			w.row(yesterday, w.pr("1", prSpec{ownership: "mine"}), 1, fixtureAt)
		}, nil},
		{"a closed old period is not stale", func(w *healthWorld) {
			w.row("2026-10-07", w.pr("1", prSpec{ownership: "mine"}), 1, fixtureAt)
			if err := w.st.FocusPeriodClose("day", "2026-10-07", "done", fixtureAt); err != nil {
				t.Fatal(err)
			}
		}, nil},
		{"selected bead held", func(w *healthWorld) {
			k := w.pr("1", prSpec{ownership: "mine"})
			w.row(today, k, 1, fixtureAt)
			w.bead("bd-1", k, beadSpec{state: "deferred", hold: "struck"})
		}, map[string]int{DivergenceSelectedBeadHeld: 1}},
		{"removed bead open and unclaimed", func(w *healthWorld) {
			k := w.pr("1", prSpec{ownership: "mine"})
			w.annotate(k, store.FocusAnnotationNone)
			w.bead("bd-1", k, beadSpec{state: "open"})
		}, map[string]int{DivergenceRemovedBeadOpenUnclaimed: 1}},
		{"removed bead claimed", func(w *healthWorld) {
			k := w.pr("1", prSpec{ownership: "mine"})
			w.annotate(k, store.FocusAnnotationNone)
			w.bead("bd-1", k, beadSpec{state: "in_progress", assignee: "worker"})
		}, map[string]int{DivergenceRemovedBeadClaimed: 1}},
		{"removed bead held is fine", func(w *healthWorld) {
			k := w.pr("1", prSpec{ownership: "mine"})
			w.annotate(k, store.FocusAnnotationNone)
			w.bead("bd-1", k, beadSpec{state: "deferred", hold: "struck"})
		}, nil},
		{"terminal source with an open bead", func(w *healthWorld) {
			k := w.pr("1", prSpec{ownership: "mine", state: "closed", merged: true})
			w.row(today, k, 1, fixtureAt)
			w.bead("bd-1", k, beadSpec{state: "open"})
		}, map[string]int{DivergenceTerminalSourceBeadOpen: 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newHealthWorld(t)
			c.setup(w)
			wantDivergence(t, w.health(), c.want)
		})
	}
}

func TestDivergenceReadsOnlyTheNewestRowOfAnItem(t *testing.T) {
	// A restored backup can leave an item with rows in two periods: only the
	// newest row counts, so the item is not forever mismatched when its
	// annotation names the newest period.
	w := newHealthWorld(t)
	k := w.pr("1", prSpec{ownership: "mine"})
	w.rowOnly(yesterday, k, 1, fixtureAt)
	w.rowOnly(today, k, 1, fixtureAt)
	w.annotate(k, today)
	h := w.health()
	// The older period still holds a row of the item: only the stale_period
	// kind sees it.
	wantDivergence(t, h, map[string]int{DivergenceStalePeriod: 1})
}

func TestPendingReasonsAndTheirAges(t *testing.T) {
	w := newHealthWorld(t)
	w.cfg.Focus.SuppressedKind = "kind-x"
	w.cfg.Watch.PR.Queries = []string{"mine"}
	// Every PR but "1" is listed by the watch query; the PR "1" is outside it.
	if err := w.st.SetMeta("change_flow.watchset.pr.mine", `["2","3","4","5","8","9","10","11"]`); err != nil {
		t.Fatal(err)
	}
	unminted := w.pr("9", prSpec{ownership: "mine"})
	unminted2 := w.pr("8", prSpec{ownership: "mine"})
	unwatched := w.pr("1", prSpec{ownership: "mine"})
	hidden := w.pr("2", prSpec{ownership: "mine"})
	w.hide(hidden)
	suppressed := w.pr("3", prSpec{ownership: "mine"})
	if err := w.st.SetAnnotation(store.KVAnnotation{
		Repo: testRepo, EntityType: "pr", EntityID: "3", Key: store.KeySuppress("kind-x"),
		Value: "true", Origin: "pg-desk", SetBy: "operator", SetAt: fixtureAt,
	}); err != nil {
		t.Fatal(err)
	}
	// A hidden and a suppressed source whose bead already exists are not
	// pending: they read "open (source hidden)" and "open (source suppressed)"
	// (pg2-o1lq9), so the gate reports neither pending reason for them.
	hiddenBead := w.pr("10", prSpec{ownership: "mine"})
	w.hide(hiddenBead)
	w.bead("bd-10", hiddenBead, beadSpec{state: "open"})
	suppressedBead := w.pr("11", prSpec{ownership: "mine"})
	if err := w.st.SetAnnotation(store.KVAnnotation{
		Repo: testRepo, EntityType: "pr", EntityID: "11", Key: store.KeySuppress("kind-x"),
		Value: "true", Origin: "pg-desk", SetBy: "operator", SetAt: fixtureAt,
	}); err != nil {
		t.Fatal(err)
	}
	w.bead("bd-11", suppressedBead, beadSpec{state: "open"})
	byDesign := w.issue("bd-77", issueSpec{state: "open"})
	terminal := w.pr("4", prSpec{ownership: "mine", state: "closed", merged: true})
	bead := w.pr("5", prSpec{ownership: "mine"})
	w.bead("bd-5", bead, beadSpec{state: "open"})

	// Selected 90 minutes, 30 minutes and 10 minutes before fixtureNow.
	at := func(d time.Duration) string { return fixtureNow.Add(-d).Format(time.RFC3339) }
	w.row(today, unminted, 1, at(90*time.Minute))
	w.row(today, unminted2, 2, at(30*time.Minute))
	w.row(today, unwatched, 3, at(10*time.Minute))
	w.row(today, hidden, 4, at(time.Minute))
	w.row(today, suppressed, 5, at(2*time.Minute))
	w.row(today, byDesign, 6, at(3*time.Minute))
	w.row(today, terminal, 7, at(4*time.Minute))
	w.row(today, bead, 8, at(5*time.Minute))
	w.row(today, hiddenBead, 9, at(6*time.Minute))
	w.row(today, suppressedBead, 10, at(7*time.Minute))

	h := w.health()
	want := map[string]int{
		PendingUnminted: 2, PendingHidden: 1, PendingSuppressed: 1, PendingNoBeadByDesign: 1,
		PendingUnwatched: 1, PendingSourceTerminal: 1,
	}
	if !reflect.DeepEqual(h.Pending, want) {
		t.Errorf("pending = %v, want %v", h.Pending, want)
	}
	if got := h.OldestPendingAge[PendingUnminted]; got != 90*time.Minute {
		t.Errorf("oldest unminted age = %v, want 90m (the oldest row)", got)
	}
	if got := h.OldestPendingAge[PendingUnwatched]; got != 10*time.Minute {
		t.Errorf("oldest unwatched age = %v, want 10m", got)
	}
	if h.UnwatchedSelected != 1 {
		t.Errorf("UnwatchedSelected = %d, want 1", h.UnwatchedSelected)
	}
	if h.Selected != 10 {
		t.Errorf("Selected = %d", h.Selected)
	}
}

func TestAnUnannotatedRowIsNotUnminted(t *testing.T) {
	w := newHealthWorld(t)
	w.rowOnly(today, w.pr("1", prSpec{ownership: "mine"}), 1, fixtureAt)
	h := w.health()
	if h.Pending[PendingUnminted] != 0 {
		t.Errorf("pending = %v: the row without an annotation is a divergence, not an unminted selection", h.Pending)
	}
}

func TestBeadsByStateAndOldestAges(t *testing.T) {
	w := newHealthWorld(t)
	src := func(id string) Key { return w.pr(id, prSpec{ownership: "mine"}) }
	old := fixtureNow.Add(-5 * time.Hour).Format(time.RFC3339)
	older := fixtureNow.Add(-50 * time.Hour).Format(time.RFC3339)
	young := fixtureNow.Add(-10 * time.Minute).Format(time.RFC3339)
	w.bead("bd-a", src("1"), beadSpec{state: "open", createdAt: old})
	w.bead("bd-b", src("2"), beadSpec{state: "open", createdAt: young})
	w.bead("bd-c", src("3"), beadSpec{state: "in_progress", assignee: "worker", createdAt: old})
	w.bead("bd-d", src("4"), beadSpec{state: "deferred", hold: "struck", createdAt: older})
	w.bead("bd-e", src("5"), beadSpec{state: "deferred", createdAt: old})
	w.bead("bd-f", src("6"), beadSpec{state: "blocked", createdAt: old})
	w.bead("bd-g", src("7"), beadSpec{state: "closed", createdAt: old})
	// An escalation (human) bead is not a focus bead; a bead with no
	// source_id is not one either.
	w.bead("bd-h", src("8"), beadSpec{state: "open", createdAt: old, labels: []string{"human"}})
	w.issue("bd-plain", issueSpec{state: "open", createdAt: old})

	h := w.health()
	want := map[string]int{"open": 2, "claimed": 1, "held": 1, "deferred_other": 1, "blocked": 1, "closed": 1}
	if !reflect.DeepEqual(h.Beads, want) {
		t.Errorf("beads = %v, want %v", h.Beads, want)
	}
	if h.OldestBeadAge["open"] != 5*time.Hour || h.OldestBeadAge["held"] != 50*time.Hour {
		t.Errorf("oldest bead ages = %v", h.OldestBeadAge)
	}
	if _, ok := h.OldestBeadAge["claimed"]; ok {
		t.Errorf("only open and held carry an age: %v", h.OldestBeadAge)
	}
	// Open and unassigned beads older than focus.pending_gate_age (30m).
	if h.MintedNeverClaimed != 1 {
		t.Errorf("MintedNeverClaimed = %d, want 1 (bd-a; bd-b is 10m old)", h.MintedNeverClaimed)
	}
}

func TestBeadAgeFallsBackToFirstSeen(t *testing.T) {
	w := newHealthWorld(t)
	w.bead("bd-a", w.pr("1", prSpec{ownership: "mine"}), beadSpec{state: "open"}) // no created_at
	h := w.health()
	// fixtureAt is 08:00, fixtureNow 12:00 (the entity's first_seen_at is its
	// write time).
	if h.OldestBeadAge["open"] <= 0 || h.OldestBeadAge["open"] > 24*time.Hour {
		t.Errorf("age = %v, want a first-seen based age", h.OldestBeadAge)
	}
}

func TestRemovedBeadInPlayAndHeldButReselectedReports(t *testing.T) {
	w := newHealthWorld(t)
	removed := w.pr("1", prSpec{ownership: "mine"})
	w.annotate(removed, store.FocusAnnotationNone)
	w.bead("bd-1", removed, beadSpec{state: "open"})
	reselected := w.pr("2", prSpec{ownership: "mine"})
	w.row(today, reselected, 1, fixtureAt)
	w.bead("bd-2", reselected, beadSpec{state: "deferred", hold: "struck"})
	h := w.health()
	if h.RemovedWithBeadInPlay != 1 || h.HeldButReselected != 1 {
		t.Errorf("removed-in-play %d, held-but-reselected %d, want 1 and 1", h.RemovedWithBeadInPlay, h.HeldButReselected)
	}
}

func TestEscalatedFocusRulesCountsSourcesNotAnnotations(t *testing.T) {
	w := newHealthWorld(t)
	k := w.pr("1", prSpec{ownership: "mine"})
	for _, key := range []string{"escalated.focus.item", "escalated.focus.other"} {
		if err := w.st.SetAnnotation(store.KVAnnotation{
			Repo: testRepo, EntityType: k.Type, EntityID: k.ID, Key: key,
			Value: "true", Origin: "decider", SetBy: "decider", SetAt: fixtureAt,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := w.health().EscalatedFocusRules; got != 1 {
		t.Errorf("EscalatedFocusRules = %d, want 1 source", got)
	}
}

func TestPlanRowsAgainstThePeriodCap(t *testing.T) {
	w := newHealthWorld(t)
	for i, k := range []Key{
		w.pr("1", prSpec{ownership: "mine"}),
		w.pr("2", prSpec{ownership: "mine"}),
		w.pr("3", prSpec{ownership: "mine", state: "closed", merged: true}),
	} {
		w.row(today, k, i+1, fixtureAt)
	}
	err := w.st.FocusLockTx(func(tx *store.FocusTx) error {
		p, err := tx.GetOrCreatePeriod("day", today)
		if err != nil {
			return err
		}
		return tx.SetPeriodCap(p.ID, 3)
	})
	if err != nil {
		t.Fatal(err)
	}
	h := w.health()
	want := map[string]int{"in_plan": 2, "finished": 1, "open_slots": 1}
	if !reflect.DeepEqual(h.PlanRows, want) {
		t.Errorf("plan rows = %v, want %v", h.PlanRows, want)
	}
}

// Traces INV-FOCUS-9: a row with no exit code counts as a total failure.
func TestRunsDerivedFromFocusRunWithANullExitCodeCountedAsTotal(t *testing.T) {
	w := newHealthWorld(t)
	exit := func(n int) *int { return &n }
	runs := []store.FocusRun{
		{RunID: "R1", Verb: "select", StartedAt: "2026-10-10T09:00:00Z", Actor: "a", ExitCode: exit(0), CountsJSON: `{"selected":2,"removed":{"operator":0},"draft_age_seconds":7200,"drift_rows":3}`},
		{RunID: "R2", Verb: "select", StartedAt: "2026-10-10T10:00:00Z", Actor: "a", ExitCode: exit(0), CountsJSON: `{"selected":0,"removed":{"operator":0}}`},
		{RunID: "R3", Verb: "select", StartedAt: "2026-10-10T08:00:00Z", Actor: "a", ExitCode: exit(2), CountsJSON: `{}`},
		{RunID: "R4", Verb: "select", StartedAt: "2026-10-10T07:00:00Z", Actor: "a", ExitCode: exit(3), CountsJSON: `{}`},
		{RunID: "R5", Verb: "select", StartedAt: "2026-10-10T06:00:00Z", Actor: "a", ExitCode: exit(6), CountsJSON: `{}`},
		{RunID: "R6", Verb: "pull", StartedAt: "2026-10-10T05:00:00Z", Actor: "a", ExitCode: exit(1), CountsJSON: `{}`},
		{RunID: "R7", Verb: "close", StartedAt: "2026-10-10T04:00:00Z", Actor: "a", ExitCode: nil, CountsJSON: `{}`},
	}
	for _, r := range runs {
		if _, err := w.st.FocusRunInsert(r); err != nil {
			t.Fatal(err)
		}
	}
	h := w.health()
	want := map[[2]string]int{
		{"select", "ok"}: 1, {"select", "empty"}: 1, {"select", "partial"}: 1, {"select", "total"}: 1,
		{"select", "period_closed"}: 1, {"pull", "usage"}: 1, {"close", "total"}: 1,
	}
	if !reflect.DeepEqual(h.Runs, want) {
		t.Errorf("runs = %v, want %v", h.Runs, want)
	}
	if h.RunAge == nil || *h.RunAge != 2*time.Hour {
		t.Errorf("RunAge = %v, want 2h since the newest select (10:00)", h.RunAge)
	}
	// The last-lock values come from the newest select row (R2), which has
	// neither member: both absent.
	if h.LastLockDraftAge != nil || h.LastLockDriftRows != nil {
		t.Errorf("last lock = %v %v, want absent", h.LastLockDraftAge, h.LastLockDriftRows)
	}
}

func TestLastLockValuesComeFromTheNewestSelectRun(t *testing.T) {
	w := newHealthWorld(t)
	zero := 0
	if _, err := w.st.FocusRunInsert(store.FocusRun{
		RunID: "R1", Verb: "select", StartedAt: "2026-10-10T09:00:00Z", Actor: "a", ExitCode: &zero,
		CountsJSON: `{"selected":2,"removed":{"operator":0},"draft_age_seconds":7200,"drift_rows":3}`,
	}); err != nil {
		t.Fatal(err)
	}
	h := w.health()
	if h.LastLockDraftAge == nil || *h.LastLockDraftAge != 2*time.Hour || h.LastLockDriftRows == nil || *h.LastLockDriftRows != 3 {
		t.Errorf("last lock = %v %v", h.LastLockDraftAge, h.LastLockDriftRows)
	}
}

func TestDraftAgeIsAbsentWithoutADraft(t *testing.T) {
	w := newHealthWorld(t)
	if h := w.health(); h.DraftAge != nil || h.DraftPeriodKey != "" {
		t.Fatalf("draft = %v %q, want none", h.DraftAge, h.DraftPeriodKey)
	}
	if _, err := w.st.FocusDraftReplace(store.FocusDraft{PeriodType: "day", PeriodKey: today, Cap: 6, MadeAt: "2026-10-10T09:00:00Z", BodyJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	h := w.health()
	if h.DraftAge == nil || *h.DraftAge != 3*time.Hour || h.DraftPeriodKey != today {
		t.Errorf("draft = %v %q, want 3h and the period key", h.DraftAge, h.DraftPeriodKey)
	}
}

func TestCandidatesRankInputsListingsAndJiraCategory(t *testing.T) {
	w := newHealthWorld(t)
	w.cfg.Watch.PR.Queries = []string{"mine"}
	setListing(t, w.fixture, "pr", "mine", "degraded", "truncated")
	w.pr("1", prSpec{ownership: "mine"})
	// A Jira issue assigned to the operator without a status category, one
	// with a category, a bead and a minted bead (never a Jira issue).
	w.issue("PROJ-1", issueSpec{state: "To Do", assignee: "Pat Example"})
	w.issue("PROJ-2", issueSpec{state: "To Do", category: "new", assignee: "Pat Example"})
	w.issue("bd-1", issueSpec{state: "open", labels: []string{PlanableLabel}})
	w.bead("bd-9", w.pr("2", prSpec{}), beadSpec{state: "open"})

	h := w.health()
	if h.Candidates["pr"] != 1 || h.Candidates["jira"] != 2 || h.Candidates["bead"] != 1 {
		t.Errorf("candidates = %v", h.Candidates)
	}
	if h.JiraCategoryAbsent != 1 {
		t.Errorf("JiraCategoryAbsent = %d, want 1", h.JiraCategoryAbsent)
	}
	if h.RankInputs.StatusCategoryAbsent != 1 {
		t.Errorf("rank inputs = %+v", h.RankInputs)
	}
	var found bool
	for _, l := range h.Listings {
		if l.Type == "pr" && l.Query == "mine" && l.Status == "degraded" {
			found = true
		}
	}
	if !found {
		t.Errorf("listings = %v, want the degraded pr listing", h.Listings)
	}
	if !h.Coverage.Incomplete {
		t.Errorf("coverage = %+v, want incomplete", h.Coverage)
	}
	if h.OpenBeads < 1 {
		t.Errorf("OpenBeads = %d", h.OpenBeads)
	}
}

func TestComputeHealthWritesNothing(t *testing.T) {
	w := newHealthWorld(t)
	k := w.pr("1", prSpec{ownership: "mine"})
	w.row(today, k, 1, fixtureAt)
	w.bead("bd-1", k, beadSpec{state: "open"})
	w.inputs()
	before := dumpAll(t, w.st)
	if _, err := ComputeHealth(w.st, w.cfg, fixtureNow); err != nil {
		t.Fatal(err)
	}
	if after := dumpAll(t, w.st); after != before {
		t.Errorf("ComputeHealth changed the store:\n%s\n---\n%s", before, after)
	}
}

// dumpAll is a coarse snapshot of what a read could disturb: the focus tables,
// the entities with their versions, every annotation, the change log and the
// meta rows.
func dumpAll(t *testing.T, s *store.Store) string {
	t.Helper()
	out := dumpFocus(t, s)
	ents, err := s.ListEntities()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		out += fmt.Sprintf("entity %s %s v%d %s\n", e.EntityType, e.EntityID, e.Version, e.ContentHash)
		anns, err := s.ListKVAnnotations(e.Repo, e.EntityType, e.EntityID)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range anns {
			out += fmt.Sprintf("ann %s %s %s=%s\n", e.EntityType, e.EntityID, a.Key, a.Value)
		}
	}
	for _, typ := range []string{"pr", "issue"} {
		recs, err := s.ListChangesAfter(typ, 0, 10000)
		if err != nil {
			t.Fatal(err)
		}
		out += fmt.Sprintf("changes %s %d\n", typ, len(recs))
	}
	meta, err := s.ListMetaPrefix("")
	if err != nil {
		t.Fatal(err)
	}
	return out + fmt.Sprint(meta)
}
