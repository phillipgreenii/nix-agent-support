package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Tests of `pg-desk focus pull` against a real migrated store, the fixed
// clock of the focus tests (2026-10-10 12:00 UTC) and the Annotator seam.

func pull(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	return runFocusCmd(t, newFocusPullCmd, args...)
}

func pullJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	out, _, err := pull(t, append([]string{"--json"}, args...)...)
	mustOK(t, err)
	var m map[string]any
	if jerr := json.Unmarshal([]byte(out), &m); jerr != nil {
		t.Fatalf("--json output is not JSON: %v\n%s", jerr, out)
	}
	return m
}

// showYesterday is the period before the fixture's today.
const showYesterday = "2026-10-09"

// mergedPR seeds a merged PR owned by the operator: stored, finished, no
// longer a candidate.
func (f *selFixture) mergedPR(t *testing.T, n int, title string) focus.Key {
	t.Helper()
	k := f.pr(t, n, title, "2026-10-09T08:00:00Z")
	f.write(t, "pr", k.ID, map[string]any{"pr_show": map[string]any{"state": "merged", "title": title}}, "2026-10-10T09:00:00Z")
	return k
}

// foreignPR seeds an open PR the operator does not own: stored, never a
// candidate.
func (f *selFixture) foreignPR(t *testing.T, n int, title string) focus.Key {
	t.Helper()
	id := prKey(n).ID
	k := f.write(t, "pr", id, map[string]any{"pr_show": map[string]any{"state": "open", "title": title}}, "2026-10-09T08:00:00Z")
	if err := f.st.UpsertInterpretation(store.Interpretation{
		Repo: showRepo, EntityType: "pr", EntityID: id, Ownership: "other",
		Enrichment: "{}", Urgency: "{}", Dispositions: "{}", Approvals: "{}", MatchReasons: "[]", AsOf: "2026-10-09T08:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestPullTopAddsRowsAfterTheLockedPlanWithTheRunsTier(t *testing.T) {
	f := newSelFixture(t)
	k1 := f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	f.pr(t, 3, "charlie", "2026-10-09T10:00:00Z")
	f.pr(t, 4, "delta", "2026-10-09T11:00:00Z")
	f.lock(t, showToday, 6, k1)

	out, errOut, err := pull(t, "--top", "2")
	mustOK(t, err)
	rows := f.plan(t, showToday)
	if len(rows) != 3 {
		t.Fatalf("plan = %+v, want the locked row plus 2 pulled", rows)
	}
	if rows[0].EntityID != "o/r#1" || rows[0].RankPosition != 1 {
		t.Errorf("the locked row moved: %+v", rows[0])
	}
	if rows[1].RankPosition != 2 || rows[2].RankPosition != 3 {
		t.Errorf("positions = %d,%d, want max plus one in rank order (2,3)", rows[1].RankPosition, rows[2].RankPosition)
	}
	// Rank order: oldest first within the tier, so 2 then 3.
	if rows[1].EntityID != "o/r#2" || rows[2].EntityID != "o/r#3" {
		t.Errorf("pulled order = %s,%s, want the current rank (2,3)", rows[1].EntityID, rows[2].EntityID)
	}
	for _, r := range rows[1:] {
		if r.Tier == nil || *r.Tier == "" {
			t.Errorf("row %s has no tier; a pulled row records the tier of the run", r.EntityID)
		}
		if got := f.annotation(t, focus.Key{Type: "pr", ID: r.EntityID}); got != showToday {
			t.Errorf("annotation of %s = %q, want %q", r.EntityID, got, showToday)
		}
	}
	mustContain(t, out, "CHANGES", "pr:o/r#2", "pr:o/r#3", "will mint")
	mustNotContain(t, out, "pr:o/r#4")
	mustContain(t, errOut, `"verb":"pull"`, `"contract":"pg-desk.focus-select/v1"`, `"dry_run":false`)

	r := f.lastRun(t)
	if r.Verb != "pull" || !r.Exit.Valid || r.Exit.Int64 != 0 {
		t.Fatalf("run = %+v, want an exited pull", r)
	}
	if countsInt(r.Counts, "selected") != 2 {
		t.Errorf("counts = %v, want 2 selected", r.Counts)
	}
	anns, _ := r.Counts["annotations"].([]any)
	if len(anns) != 2 {
		t.Fatalf("annotations = %v, want one outcome per pulled entity", anns)
	}
	for _, a := range anns {
		m := a.(map[string]any)
		if m["outcome"] != "ok" || m["seq"].(float64) == 0 {
			t.Errorf("annotation outcome = %v, want ok with a seq", m)
		}
	}
	// A pull never re-caps: the period's cap is the lock's.
	if p, _, _ := f.st.FocusPeriodGet("day", showToday); p.Cap == nil || *p.Cap != 6 {
		t.Errorf("period cap = %v, want 6 untouched", p.Cap)
	}
}

func TestPullIsUncappedAndNeverRemovesOrReSorts(t *testing.T) {
	f := newSelFixture(t)
	k1 := f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	k2 := f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	for i := 3; i <= 6; i++ {
		f.pr(t, i, "more", "2026-10-09T10:00:00Z")
	}
	// A locked plan in an order the fresh rank would not produce (2 above 1).
	f.lock(t, showToday, 2, k2, k1)
	_, _, err := pull(t, "--top", "4")
	mustOK(t, err)
	rows := f.plan(t, showToday)
	if len(rows) != 6 {
		t.Fatalf("plan = %d rows, want 6: pull is uncapped (cap 2)", len(rows))
	}
	if rows[0].EntityID != "o/r#2" || rows[1].EntityID != "o/r#1" {
		t.Errorf("the plan was re-sorted: %s,%s", rows[0].EntityID, rows[1].EntityID)
	}
	if p, _, _ := f.st.FocusPeriodGet("day", showToday); p.Cap == nil || *p.Cap != 2 {
		t.Errorf("period cap = %v, want 2 untouched", p.Cap)
	}
}

func TestPullLeavesTheDraftAndItsMarksSurvive(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	f.pr(t, 3, "charlie", "2026-10-09T10:00:00Z")
	f.showDraft(t)
	before, _ := f.draftRow(t)

	_, _, err := pull(t, "o/r#3")
	mustOK(t, err)

	after, found := f.draftRow(t)
	if !found || after != before {
		t.Fatalf("pull changed the stored draft: found=%v\n%+v\n---\n%+v", found, before, after)
	}
	// The marks survive: the draft still proposes 1 and 2 (+), and the pulled
	// entity now shows in the PLAN block.
	out, _, err := show(t)
	mustOK(t, err)
	mustContain(t, out, "o/r#1", "o/r#2", "DRAFT (stored")
	planAt := strings.Index(out, "PLAN")
	if planAt < 0 || !strings.Contains(out[planAt:], "o/r#3") {
		t.Errorf("the pulled entity is not in the PLAN block:\n%s", out)
	}

	// At the lock the draft row already in the plan is simply already there.
	_, _, err = sel(t, "ok", "--apply")
	mustOK(t, err)
	if got := strings.Join(f.planKeys(t, showToday), ","); got != "o/r#3,o/r#1,o/r#2" {
		t.Errorf("plan after the lock = %s, want the pulled row kept and the draft's rows added", got)
	}
}

func TestPullBareIntegerIsAKeyEndToEnd(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	f.pr(t, 3, "charlie", "2026-10-09T10:00:00Z")

	_, _, err := pull(t, "3")
	mustOK(t, err)
	if got := strings.Join(f.planKeys(t, showToday), ","); got != "o/r#3" {
		t.Fatalf("plan = %s, want exactly PR 3: a positional integer is a key, never a count", got)
	}
	if got := f.annotation(t, prKey(3)); got != showToday {
		t.Errorf("annotation of PR 3 = %q", got)
	}
}

func TestPullTopWithAKeyIsUsage(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	before := f.snapshot(t)
	for _, args := range [][]string{{"--top", "2", "1"}, {"3", "--top", "1"}} {
		_, _, err := pull(t, args...)
		assertFocusExit(t, err, 1, "--top cannot be used together with a key")
	}
	_, _, err := pull(t, "--top", "0")
	assertFocusExit(t, err, 1, "must be positive")
	_, _, err = pull(t, "--top", "-1")
	assertFocusExit(t, err, 1, "must be positive")
	if got := f.snapshot(t); got != before {
		t.Errorf("a usage error changed the store:\n%s\n---\n%s", before, got)
	}
}

func TestPullUnresolvableKeyIsUsage(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	_, _, err := pull(t, "not a key")
	assertFocusExit(t, err, 1, "cannot resolve key")
	if f.count(t, "focus_selection") != 0 {
		t.Errorf("rows were written")
	}
}

func TestPullBareInvocationPreviewsAndWritesNothing(t *testing.T) {
	f := newSelFixture(t)
	k1 := f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	f.lock(t, showToday, 6, k1)
	before := f.snapshot(t)

	out, errOut, err := pull(t)
	mustOK(t, err)
	if got := f.snapshot(t); got != before {
		t.Fatalf("a bare pull changed the store:\n%s\n---\n%s", before, got)
	}
	mustContain(t, out, "o/r#1", "o/r#2")
	mustContain(t, errOut, `"dry_run":true`)
	if f.count(t, "focus_run") != 0 {
		t.Errorf("a bare pull wrote a focus_run row")
	}
}

func TestPullDryRunWritesNoPeriodRowAndNoTableChanges(t *testing.T) {
	for _, args := range [][]string{{"--top", "2", "--dry-run"}, {"o/r#1", "o/r#2", "--dry-run"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f := newSelFixture(t)
			f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
			f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
			f.showDraft(t)
			before := f.snapshot(t)

			out, errOut, err := pull(t, args...)
			mustOK(t, err)
			if got := f.snapshot(t); got != before {
				t.Fatalf("a dry run changed the store:\n%s\n---\n%s", before, got)
			}
			if _, found, _ := f.st.FocusPeriodGet("day", showToday); found {
				t.Errorf("a dry run left a focus_period row behind")
			}
			mustContain(t, out, "CHANGES", "pr:o/r#1", "pr:o/r#2", "will mint")
			mustContain(t, errOut, `"dry_run":true`)
		})
	}
}

func TestPullDryRunReportsExactlyWhatAnApplyDoes(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	dry, _, err := pull(t, "--top", "2", "--dry-run")
	mustOK(t, err)
	applied, _, err := pull(t, "--top", "2")
	mustOK(t, err)
	if dry != applied {
		t.Errorf("the dry run's listing differs from the apply's:\n%s\n---\n%s", dry, applied)
	}
}

func TestPullIntoNewPeriodBeforeFirstLockExitsOne(t *testing.T) {
	f := newSelFixture(t)
	y := f.pr(t, 9, "yesterday", "2026-10-08T08:00:00Z")
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.lock(t, showYesterday, 6, y)
	before := f.snapshot(t)

	for _, args := range [][]string{{"--top", "1"}, {"o/r#1"}, {"--top", "1", "--dry-run"}, {}} {
		_, _, err := pull(t, args...)
		assertFocusExit(t, err, 1, "lock today's plan first: run show, then select --apply")
	}
	if _, found, _ := f.st.FocusPeriodGet("day", showToday); found {
		t.Errorf("a refused pull left a focus_period row behind")
	}
	// Only the run rows of the writing attempts may differ.
	rows := f.plan(t, showYesterday)
	if len(rows) != 1 || rows[0].EntityID != "o/r#9" {
		t.Errorf("yesterday's plan changed: %+v", rows)
	}
	if f.snapshot(t) == before {
		t.Errorf("the refused writing pulls recorded no run row")
	}
	for _, r := range f.runs(t) {
		if r.Verb != "pull" || !r.Exit.Valid || r.Exit.Int64 != 1 {
			t.Errorf("run = %+v, want pull recorded as exit 1", r)
		}
	}
}

func TestPullIntoFuturePeriodIsExitOne(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	_, _, err := pull(t, "--top", "1", "--date", "2026-10-11")
	assertFocusExit(t, err, 1, "2026-10-11 is in the future")
	if _, found, _ := f.st.FocusPeriodGet("day", "2026-10-11"); found {
		t.Errorf("a refused pull created the future period")
	}
	if f.count(t, "focus_selection") != 0 {
		t.Errorf("rows were written")
	}
}

func TestPullExitOneCheckRunsInsideTheTransaction(t *testing.T) {
	f := newSelFixture(t)
	y := f.pr(t, 9, "yesterday", "2026-10-08T08:00:00Z")
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")

	// With nothing locked anywhere the preflight passes; a lock of another
	// period commits in the gap, and the transaction must refuse.
	orig := focusPullBeforeTx
	t.Cleanup(func() { focusPullBeforeTx = orig })
	focusPullBeforeTx = func(*store.Store) { f.lock(t, showYesterday, 6, y) }

	_, _, err := pull(t, "o/r#1")
	assertFocusExit(t, err, 1, "lock today's plan first: run show, then select --apply")
	if _, found, _ := f.st.FocusPeriodGet("day", showToday); found {
		t.Errorf("the refused transaction left a focus_period row behind")
	}
	if rows := f.plan(t, showYesterday); len(rows) != 1 || rows[0].EntityID != "o/r#9" {
		t.Errorf("yesterday's plan = %+v, want it untouched", rows)
	}
	if got := f.annotation(t, prKey(1)); got != "<absent>" {
		t.Errorf("annotation of the refused pull = %q, want none written", got)
	}
}

func TestPullIntoClosedPeriodExitsSix(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.closePeriod(t, showToday)
	for _, args := range [][]string{{"--top", "1"}, {"o/r#1"}, {"--top", "1", "--dry-run"}, {}} {
		_, _, err := pull(t, args...)
		assertFocusExit(t, err, 6, "use --date <tomorrow>")
	}
	if f.count(t, "focus_selection") != 0 {
		t.Errorf("rows were written into a closed period")
	}
}

func TestPullNamedKeysAlreadySelectedOrNoLongerCandidatesAreRoutineSkips(t *testing.T) {
	f := newSelFixture(t)
	k1 := f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	f.mergedPR(t, 3, "gone")
	f.foreignPR(t, 4, "not mine")
	f.lock(t, showToday, 6, k1)

	out, errOut, err := pull(t, "o/r#1", "o/r#2", "o/r#3", "o/r#4")
	mustOK(t, err)
	mustContain(t, errOut,
		"skip pr:o/r#1: already-selected",
		"skip pr:o/r#3: no-longer-candidate",
		"skip pr:o/r#4: not-a-candidate")
	mustNotContain(t, errOut, "skip pr:o/r#2")
	mustContain(t, out, "pr:o/r#2")
	if got := strings.Join(f.planKeys(t, showToday), ","); got != "o/r#1,o/r#2" {
		t.Errorf("plan = %s, want only the candidate added", got)
	}

	// A request in which EVERY key is such a skip is still exit 0.
	_, errOut, err = pull(t, "o/r#1", "o/r#3")
	mustOK(t, err)
	mustContain(t, errOut, "skip pr:o/r#1: already-selected", "skip pr:o/r#3: no-longer-candidate")
	if r := f.lastRun(t); !r.Exit.Valid || r.Exit.Int64 != 0 || countsInt(r.Counts, "selected") != 0 {
		t.Errorf("run = %+v, want an exit 0 that selected nothing", r)
	}
}

func TestPullNoNamedKeyWithAnEntityRowIsExitThree(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	_, _, err := pull(t, "o/r#77", "o/r#78")
	assertFocusExit(t, err, 3, "none of the named keys has an entity row")
	if f.count(t, "focus_selection") != 0 {
		t.Errorf("rows were written")
	}
	if r := f.lastRun(t); !r.Exit.Valid || r.Exit.Int64 != 3 {
		t.Errorf("run = %+v, want the total failure recorded as exit 3", r)
	}

	// One key with an entity row is enough: the rest is a skip.
	_, errOut, err := pull(t, "o/r#77", "o/r#1")
	mustOK(t, err)
	mustContain(t, errOut, "skip pr:o/r#77: not-a-candidate")
	if got := strings.Join(f.planKeys(t, showToday), ","); got != "o/r#1" {
		t.Errorf("plan = %s", got)
	}
}

func TestPullAnnotationIsTheMaxPeriodKeyAndARerunIsIdempotent(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	_, _, err := pull(t, "o/r#1")
	mustOK(t, err)
	if got := f.annotation(t, prKey(1)); got != showToday {
		t.Fatalf("annotation = %q, want %q", got, showToday)
	}
	tabs := []string{"focus_selection", "annotation", "focus_period"}
	first := f.tables(t, tabs...)
	logBefore := f.count(t, "change_log")

	_, errOut, err := pull(t, "o/r#1")
	mustOK(t, err)
	mustContain(t, errOut, "skip pr:o/r#1: already-selected")
	if got := f.tables(t, tabs...); got != first {
		t.Fatalf("a re-run changed the store:\n%s\n---\n%s", first, got)
	}
	if got := f.count(t, "change_log"); got != logBefore {
		t.Errorf("change_log rows = %d, want %d: a re-run wrote a redundant change record", got, logBefore)
	}
	if len(f.runs(t)) != 2 {
		t.Errorf("runs = %d, want one focus_run row per non-dry run", len(f.runs(t)))
	}
}

func TestPullPartialAnnotationFailureIsExitTwoAndTheRowsStay(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	f.useAnnotator(t, func(a focus.Annotator) focus.Annotator { return failingFor{inner: a, id: "o/r#2"} })

	_, _, err := pull(t, "--top", "2")
	assertFocusExit(t, err, 2, "select --repair")
	if len(f.plan(t, showToday)) != 2 {
		t.Fatalf("the rows did not commit")
	}
	if got := f.annotation(t, prKey(2)); got != "<absent>" {
		t.Errorf("the failed entity was annotated: %q", got)
	}
	r := f.lastRun(t)
	if !r.Exit.Valid || r.Exit.Int64 != 2 {
		t.Fatalf("run exit = %+v, want 2", r.Exit)
	}
	// The repair mends it.
	f.useAnnotator(t, nil)
	_, _, err = sel(t, "", "--repair")
	mustOK(t, err)
	if got := f.annotation(t, prKey(2)); got != showToday {
		t.Errorf("annotation after repair = %q", got)
	}
}

func TestPullJSONShape(t *testing.T) {
	f := newSelFixture(t)
	k1 := f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	f.lock(t, showToday, 6, k1)

	m := pullJSON(t, "o/r#1", "o/r#2")
	if m["contract"] != "pg-desk.focus/v1" || m["verb"] != "pull" || m["period"] != showToday {
		t.Fatalf("envelope = %v", m)
	}
	if id, _ := m["run_id"].(string); len(id) != 26 {
		t.Errorf("run_id = %v, want a ULID", m["run_id"])
	}
	sel, _ := m["selected"].([]any)
	if len(sel) != 1 {
		t.Fatalf("selected = %v", m["selected"])
	}
	row := sel[0].(map[string]any)
	if _, hasBecause := row["because"]; row["key"] != "pr:o/r#2" || row["title"] != "bravo" || row["effect"] != "mint" || !hasBecause {
		t.Errorf("selected[0] = %v, want key, title, effect and because", row)
	}
	skipped, _ := m["skipped"].([]any)
	if len(skipped) != 1 || skipped[0].(map[string]any)["key"] != "pr:o/r#1" || skipped[0].(map[string]any)["reason"] != "already-selected" {
		t.Errorf("skipped = %v", m["skipped"])
	}
	anns, _ := m["annotations"].([]any)
	if len(anns) != 1 || anns[0].(map[string]any)["key"] != "pr:o/r#2" || anns[0].(map[string]any)["outcome"] != "ok" {
		t.Errorf("annotations = %v", m["annotations"])
	}

	// A dry run carries an EMPTY run_id and no annotations.
	f2 := newSelFixture(t)
	f2.pr(t, 5, "echo", "2026-10-09T08:00:00Z")
	d := pullJSON(t, "--top", "1", "--dry-run")
	if id, ok := d["run_id"]; !ok || id != "" {
		t.Errorf("dry-run run_id = %v (present %v), want the empty string", id, ok)
	}
	if a, _ := d["annotations"].([]any); len(a) != 0 {
		t.Errorf("dry-run annotations = %v, want none", a)
	}
	if s, _ := d["selected"].([]any); len(s) != 1 {
		t.Errorf("dry-run selected = %v, want the would-be row", d["selected"])
	}
}

func TestPullNothingLeftToPullIsExitZero(t *testing.T) {
	f := newSelFixture(t)
	k1 := f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.lock(t, showToday, 6, k1)
	out, _, err := pull(t, "--top", "3")
	mustOK(t, err)
	mustContain(t, out, "CHANGES", "(none)")
	if r := f.lastRun(t); r.Verb != "pull" || !r.Exit.Valid || r.Exit.Int64 != 0 {
		t.Errorf("run = %+v", r)
	}
}

func TestPullNoteForANonTodayPeriod(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-08T08:00:00Z")
	_, errOut, err := pull(t, "o/r#1", "--date", showYesterday)
	mustOK(t, err)
	mustContain(t, errOut, "NOTE: writing into period 2026-10-09, not today")
	if got := f.planKeys(t, showYesterday); len(got) != 1 {
		t.Errorf("plan = %v", got)
	}
}

// TestPullExitCodes is the pull rows of the verb x documented-code table.
func TestPullExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		prep func(t *testing.T, f *selFixture)
		args []string
		code int
		want string
	}{
		{name: "ok", args: []string{"--top", "1"}, code: 0},
		{name: "ok: bare preview", args: nil, code: 0},
		{
			name: "ok: every key a routine skip", args: []string{"o/r#1"},
			prep: func(t *testing.T, f *selFixture) { f.lock(t, showToday, 6, prKey(1)) }, code: 0,
		},
		{name: "usage: --top with a key", args: []string{"--top", "1", "o/r#1"}, code: 1, want: "--top cannot be used together with a key"},
		{name: "usage: bad date", args: []string{"--top", "1", "--date", "x"}, code: 1, want: "pass --date YYYY-MM-DD"},
		{
			name: "usage: no plan while another period holds rows", args: []string{"--top", "1"},
			prep: func(t *testing.T, f *selFixture) {
				f.pr(t, 9, "old", "2026-10-08T08:00:00Z")
				f.lock(t, showYesterday, 6, prKey(9))
			}, code: 1, want: "lock today's plan first: run show, then select --apply",
		},
		{name: "usage: future period", args: []string{"--top", "1", "--date", "2026-10-12"}, code: 1, want: "is in the future"},
		{name: "total: no named key has an entity row", args: []string{"o/r#99"}, code: 3, want: "none of the named keys has an entity row"},
		{
			name: "period closed", args: []string{"--top", "1"},
			prep: func(t *testing.T, f *selFixture) { f.closePeriod(t, showToday) }, code: 6, want: "use --date <tomorrow>",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSelFixture(t)
			f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
			f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
			if tc.prep != nil {
				tc.prep(t, f)
			}
			_, _, err := pull(t, tc.args...)
			if got := focusExitCode(err); got != tc.code {
				t.Fatalf("exit code = %d, want %d (err: %v)", got, tc.code, err)
			}
			if tc.code != 0 {
				assertFocusExit(t, err, tc.code, tc.want)
			}
		})
	}
}

// TestPullHelpNamesTheRulesAndTheExitCodes keeps the verb's help honest.
func TestPullHelpNamesTheRulesAndTheExitCodes(t *testing.T) {
	c := newFocusPullCmd()
	for _, want := range []string{"--top", "--dry-run", "ALWAYS a key", "lock today's plan first", "6 period closed"} {
		if !strings.Contains(c.Long+c.Use, want) {
			t.Errorf("pull help lacks %q", want)
		}
	}
}
