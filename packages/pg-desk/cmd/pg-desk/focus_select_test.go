package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Tests of `pg-desk focus select` against a real migrated store, the fixed
// clock of the focus tests (2026-10-10 12:00 UTC) and the Annotator seam.

// selFixture is a show fixture with ticket keys of the shape J-<n> and an
// Annotator whose per-entity lock lives in a temp dir.
type selFixture struct {
	*showFixture
	annotWrap func(focus.Annotator) focus.Annotator
}

func newSelFixture(t *testing.T) *selFixture {
	t.Helper()
	f := &selFixture{showFixture: newShowFixture(t)}
	f.cfg.TicketPatterns = []string{"J-[0-9]+"}
	f.useAnnotator(t, nil)
	return f
}

// useAnnotator points the verb at a store annotator (optionally wrapped).
func (f *selFixture) useAnnotator(t *testing.T, wrap func(focus.Annotator) focus.Annotator) {
	t.Helper()
	orig := newFocusAnnotator
	dir := t.TempDir()
	newFocusAnnotator = func(st *store.Store) focus.Annotator {
		var a focus.Annotator = focus.NewAnnotator(st, store.NewLocker(store.LockerOptions{LockDir: dir, Timeout: 2 * time.Second}))
		if wrap != nil {
			a = wrap(a)
		}
		return a
	}
	t.Cleanup(func() { newFocusAnnotator = orig })
}

func (f *selFixture) hideEntity(t *testing.T, k focus.Key) {
	t.Helper()
	if err := f.st.SetAnnotation(store.KVAnnotation{
		Repo: showRepo, EntityType: k.Type, EntityID: k.ID, Key: store.AnnotationHidden,
		Value: `{"value":true,"reason":null}`, Origin: "pg-desk", SetBy: "me", SetAt: showAt,
	}); err != nil {
		t.Fatal(err)
	}
}

func prKey(n int) focus.Key { return focus.Key{Type: "pr", ID: fmt.Sprintf("o/r#%d", n)} }

// sel runs select with the reply on stdin.
func sel(t *testing.T, reply string, args ...string) (string, string, error) {
	t.Helper()
	c := newFocusSelectCmd()
	var out, errb strings.Builder
	c.SetOut(&out)
	c.SetErr(&errb)
	c.SetIn(strings.NewReader(reply))
	c.SetArgs(args)
	err := c.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

func (f *selFixture) plan(t *testing.T, period string) []store.FocusSelection {
	t.Helper()
	p, found, err := f.st.FocusPeriodGet("day", period)
	if err != nil || !found {
		t.Fatalf("period %s: found %v err %v", period, found, err)
	}
	rows, err := f.st.FocusPlanRows(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func (f *selFixture) planKeys(t *testing.T, period string) []string {
	t.Helper()
	var out []string
	for _, r := range f.plan(t, period) {
		out = append(out, r.EntityID)
	}
	return out
}

func (f *selFixture) annotation(t *testing.T, k focus.Key) string {
	t.Helper()
	a, found, err := f.st.GetKVAnnotation(showRepo, k.Type, k.ID, store.AnnotationFocusSelected)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		return "<absent>"
	}
	return a.Value
}

type runRecord struct {
	RunID, Verb string
	Exit        sql.NullInt64
	Counts      map[string]any
}

func (f *selFixture) runs(t *testing.T) []runRecord {
	t.Helper()
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT run_id, verb, exit_code, counts_json FROM focus_run ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []runRecord
	for rows.Next() {
		var r runRecord
		var cj string
		if err := rows.Scan(&r.RunID, &r.Verb, &r.Exit, &cj); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(cj), &r.Counts); err != nil {
			t.Fatalf("counts_json %q: %v", cj, err)
		}
		out = append(out, r)
	}
	return out
}

func (f *selFixture) lastRun(t *testing.T) runRecord {
	t.Helper()
	rs := f.runs(t)
	if len(rs) == 0 {
		t.Fatalf("no focus_run row")
	}
	return rs[len(rs)-1]
}

// tables dumps the named tables, row by row.
func (f *selFixture) tables(t *testing.T, names ...string) string {
	t.Helper()
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var sb strings.Builder
	for _, n := range names {
		rows, err := db.Query("SELECT * FROM " + n + " ORDER BY 1") //nolint:gosec // test-only table names
		if err != nil {
			t.Fatalf("dump %s: %v", n, err)
		}
		cols, _ := rows.Columns()
		fmt.Fprintf(&sb, "## %s\n", n)
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&sb, "%v\n", vals)
		}
		_ = rows.Close()
	}
	return sb.String()
}

func (f *selFixture) count(t *testing.T, table string) int {
	t.Helper()
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil { //nolint:gosec // test-only table names
		t.Fatal(err)
	}
	return n
}

// showDraft runs show so the draft is stored.
func (f *selFixture) showDraft(t *testing.T, args ...string) {
	t.Helper()
	if _, _, err := show(t, args...); err != nil {
		t.Fatalf("show: %v", err)
	}
	if _, found := f.draftRow(t); !found {
		t.Fatalf("show stored no draft")
	}
}

func countsInt(m map[string]any, key string) int {
	v, _ := m[key].(float64)
	return int(v)
}

// ------------------------------------------------------------ the lock

func TestSelectApplyLocksThePlanInOrderAndWritesTheRecord(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	f.showDraft(t)

	out, errOut, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	if got := f.planKeys(t, showToday); strings.Join(got, ",") != "o/r#1,o/r#2" {
		t.Fatalf("plan = %v, want the draft's order", got)
	}
	rows := f.plan(t, showToday)
	if rows[0].RankPosition != 1 || rows[1].RankPosition != 2 || rows[0].Tier == nil {
		t.Errorf("rows = %+v, want positions 1..2 with the draft's tier", rows)
	}
	for _, k := range []focus.Key{prKey(1), prKey(2)} {
		if got := f.annotation(t, k); got != showToday {
			t.Errorf("annotation of %v = %q, want %q", k, got, showToday)
		}
	}
	if _, found := f.draftRow(t); found {
		t.Errorf("the lock left the stored draft")
	}
	p, _, _ := f.st.FocusPeriodGet("day", showToday)
	if p.Cap == nil || *p.Cap != focus.DefaultCap {
		t.Errorf("period cap = %v, want the draft's cap %d", p.Cap, focus.DefaultCap)
	}
	mustContain(t, out, "CHANGES", "pr:o/r#1", "pr:o/r#2", "will mint", "ECHO")
	r := f.lastRun(t)
	if r.Verb != "select" || !r.Exit.Valid || r.Exit.Int64 != 0 {
		t.Fatalf("run = %+v, want an exited select", r)
	}
	anns, _ := r.Counts["annotations"].([]any)
	if len(anns) != 2 || countsInt(r.Counts, "selected") != 2 {
		t.Fatalf("counts = %v, want 2 selected and 2 annotations", r.Counts)
	}
	for _, a := range anns {
		m := a.(map[string]any)
		if m["outcome"] != "ok" || m["seq"].(float64) == 0 {
			t.Errorf("annotation outcome = %v, want ok with a seq", m)
		}
	}
	mustContain(t, errOut, `"contract":"pg-desk.focus-select/v1"`, `"verb":"select"`)
	if r.Counts["fresh_order"] != false {
		t.Errorf("fresh_order = %v, want false with a stored draft", r.Counts["fresh_order"])
	}
}

func TestSelectTwiceWithOkIsByteIdentical(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	f.showDraft(t)
	_, _, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	tabs := []string{"focus_selection", "annotation", "focus_period", "focus_draft"}
	first := f.tables(t, tabs...)
	logBefore := f.count(t, "change_log")

	// The second run has no stored draft and computes one in process.
	_, errOut, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	if got := f.tables(t, tabs...); got != first {
		t.Fatalf("a second ok changed the store:\n%s\n---\n%s", first, got)
	}
	if got := f.count(t, "change_log"); got != logBefore {
		t.Errorf("change_log rows = %d, want %d: a re-run wrote a redundant change record", got, logBefore)
	}
	mustContain(t, errOut, "WARNING: no stored draft")
	if r := f.lastRun(t); r.Counts["fresh_order"] != true {
		t.Errorf("fresh_order = %v, want true", r.Counts["fresh_order"])
	}
	if len(f.runs(t)) != 2 {
		t.Errorf("runs = %d, want one focus_run row per apply", len(f.runs(t)))
	}
}

func TestSelectPreviewWithoutApplyWritesNothingInAnyTable(t *testing.T) {
	for _, args := range [][]string{{}, {"--dry-run"}, {"--apply", "--dry-run"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f := newSelFixture(t)
			f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
			f.showDraft(t)
			before := f.snapshot(t)
			out, errOut, err := sel(t, "ok", args...)
			mustOK(t, err)
			if got := f.snapshot(t); got != before {
				t.Fatalf("a preview changed the store:\n%s\n---\n%s", before, got)
			}
			if _, found := f.draftRow(t); !found {
				t.Errorf("a preview deleted the stored draft")
			}
			mustContain(t, out, "CHANGES", "pr:o/r#1", "will mint")
			mustContain(t, errOut, `"dry_run":true`)
		})
	}
}

func TestSelectWithoutStoredDraftWarnsAndRecordsFreshOrder(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	_, errOut, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	mustContain(t, errOut, "WARNING: no stored draft; the order was computed now and may differ from what you saw")
	if r := f.lastRun(t); r.Counts["fresh_order"] != true {
		t.Errorf("fresh_order = %v", r.Counts["fresh_order"])
	}
	if got := f.planKeys(t, showToday); len(got) != 1 {
		t.Errorf("plan = %v, want the in-process draft's proposal", got)
	}
}

func TestSelectUnreadableDraftFallsBackToTheInProcessCompute(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	if _, err := f.st.FocusDraftReplace(store.FocusDraft{PeriodType: "day", PeriodKey: showToday, Cap: 6, MadeAt: showAt, BodyJSON: `{"contract":"nope"}`}); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	mustContain(t, errOut, "WARNING: no stored draft")
	if got := f.planKeys(t, showToday); len(got) != 1 {
		t.Errorf("plan = %v", got)
	}
}

func TestSelectCapFlagWithStoredDraftIsUsageError(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.showDraft(t)
	tabs := []string{"focus_selection", "focus_period", "focus_draft", "annotation"}
	before := f.tables(t, tabs...)
	_, _, err := sel(t, "ok", "--apply", "--cap", "2")
	assertFocusExit(t, err, 1, "--cap cannot be used with an applicable stored draft")
	if f.tables(t, tabs...) != before {
		t.Errorf("a usage error changed selection state")
	}
	if r := f.lastRun(t); !r.Exit.Valid || r.Exit.Int64 != 1 {
		t.Errorf("run = %+v, want the usage error recorded as exit 1", r)
	}
}

func TestCapPersistsAcrossRerun(t *testing.T) {
	f := newSelFixture(t)
	for i := 1; i <= 4; i++ {
		f.pr(t, i, fmt.Sprintf("pr %d", i), fmt.Sprintf("2026-10-09T0%d:00:00Z", i))
	}
	_, _, err := sel(t, "ok", "--apply", "--cap", "2")
	mustOK(t, err)
	if got := f.planKeys(t, showToday); len(got) != 2 {
		t.Fatalf("plan = %v, want the top 2", got)
	}
	capOf := func() int {
		p, _, _ := f.st.FocusPeriodGet("day", showToday)
		if p.Cap == nil {
			return -1
		}
		return *p.Cap
	}
	if capOf() != 2 {
		t.Fatalf("persisted cap = %d, want 2", capOf())
	}
	_, _, err = sel(t, "ok", "--apply")
	mustOK(t, err)
	if capOf() != 2 || len(f.plan(t, showToday)) != 2 {
		t.Errorf("a re-run changed the cap (%d) or the plan (%v)", capOf(), f.planKeys(t, showToday))
	}
}

func TestRankPositionIsAscendingAndNeverRewritten(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	f.pr(t, 3, "charlie", "2026-10-09T10:00:00Z")
	f.showDraft(t, "--cap", "1")
	_, _, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	if got := f.planKeys(t, showToday); strings.Join(got, ",") != "o/r#1" {
		t.Fatalf("plan = %v", got)
	}
	// Force-pull 3 over the plan, then strike 1 and add 2: positions only ever
	// rise and a deletion leaves a gap.
	_, _, err = sel(t, "+3", "--apply")
	mustOK(t, err)
	_, _, err = sel(t, "-1 +2", "--apply")
	mustOK(t, err)
	rows := f.plan(t, showToday)
	if len(rows) != 2 || rows[0].EntityID != "o/r#3" || rows[0].RankPosition != 2 || rows[1].EntityID != "o/r#2" || rows[1].RankPosition != 3 {
		t.Fatalf("rows = %+v, want 3 at 2 then 2 at 3 (a gap at 1, never renumbered)", rows)
	}
	if got := f.annotation(t, prKey(1)); got != "none" {
		t.Errorf("annotation of the struck row = %q, want none", got)
	}
}

func TestCapLineAndForcePullRaisesCap(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	_, _, err := sel(t, "ok", "--apply", "--cap", "1")
	mustOK(t, err)
	_, _, err = sel(t, "+2", "--apply")
	mustOK(t, err)
	// The force-pulled row survives, the persisted cap is unchanged and the
	// cap line never goes negative.
	if got := f.planKeys(t, showToday); len(got) != 2 {
		t.Fatalf("plan = %v, want the forced row kept", got)
	}
	if p, _, _ := f.st.FocusPeriodGet("day", showToday); p.Cap == nil || *p.Cap != 1 {
		t.Errorf("cap = %v, want 1 persisted", p.Cap)
	}
	out, _, _ := show(t)
	mustContain(t, out, "0 open slots")
	mustNotContain(t, out, "-1 open")
}

func TestPlanOrderIsTotalWithNullTier(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	f.showDraft(t, "--cap", "1")
	_, _, err := sel(t, "ok +2", "--apply")
	mustOK(t, err)
	rows := f.plan(t, showToday)
	if len(rows) != 2 || rows[0].RankPosition >= rows[1].RankPosition {
		t.Fatalf("rows = %+v, want strictly ascending positions", rows)
	}
	// Plan-mode show prints the plan in the recorded order.
	out, _, _ := show(t)
	if strings.Index(out, "o/r#1") > strings.Index(out, "o/r#2") {
		t.Errorf("plan order not kept:\n%s", out)
	}
}

func TestSelectWithoutStoredDraftComputesOnce(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	// What a no-store show prints is the order the in-process draft locks.
	shown, _, _ := show(t, "--no-store")
	_, _, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	if strings.Index(shown, "o/r#1") > strings.Index(shown, "o/r#2") || strings.Join(f.planKeys(t, showToday), ",") != "o/r#1,o/r#2" {
		t.Errorf("plan = %v after a no-draft lock", f.planKeys(t, showToday))
	}
	if n := len(f.runs(t)); n != 1 {
		t.Errorf("runs = %d, want one", n)
	}
	if _, found := f.draftRow(t); found {
		t.Errorf("an in-process draft was stored")
	}
}

func TestSelectHandAddHasNullTierAndIsHydratedFirst(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.showDraft(t)
	h := &writingHydrator{st: f.st, write: func(st *store.Store, typ, id string) error {
		e := store.Entity{Repo: showRepo, EntityType: typ, EntityID: id, Facts: `{"issue_show":{"id":"J-9","state":"In Progress","title":"new"}}`, AsOf: showAt, ContentHash: "hJ-9"}
		_, err := st.WriteEntityStateWithLog(e, 0, showAt, true, []string{"created"}, "sync", showAt)
		return err
	}}
	withHydrator(t, h)

	// Under --dry-run nothing is hydrated and the key is reported.
	out, _, err := sel(t, "ok +J-9", "--dry-run")
	mustOK(t, err)
	mustContain(t, out, "would hydrate issue:J-9", "hydrated, new to store")
	if h.calls != 0 {
		t.Fatalf("a dry run hydrated %d keys", h.calls)
	}
	out, _, err = sel(t, "ok +J-9", "--apply")
	mustOK(t, err)
	if h.calls != 1 {
		t.Fatalf("hydrate calls = %d, want 1", h.calls)
	}
	mustContain(t, out, "hydrated issue:J-9", "hydrated, new to store")
	rows := f.plan(t, showToday)
	if len(rows) != 2 || rows[1].EntityID != "J-9" || rows[1].Tier != nil || rows[1].RankPosition != 2 {
		t.Fatalf("rows = %+v, want the hand-add last with a NULL tier", rows)
	}
	if got := f.annotation(t, focus.Key{Type: "issue", ID: "J-9"}); got != showToday {
		t.Errorf("hand-add annotation = %q", got)
	}
	if r := f.lastRun(t); fmt.Sprint(r.Counts["hydrated"]) != "[issue:J-9]" || fmt.Sprint(r.Counts["handadded"]) != "[issue:J-9]" {
		t.Errorf("counts hydrated/handadded = %v / %v", r.Counts["hydrated"], r.Counts["handadded"])
	}
}

// writingHydrator is a changes.Hydrator that writes an entity row.
type writingHydrator struct {
	st    *store.Store
	write func(st *store.Store, typ, id string) error
	err   error
	calls int
}

func (h *writingHydrator) RunEntityChange(ctx context.Context, typ, id string, _ gather.ChangeKind, _ pipeline.EntityChangeOptions) (pipeline.EntityChangeResult, error) {
	h.calls++
	if h.err != nil {
		return pipeline.EntityChangeResult{}, h.err
	}
	if h.write != nil {
		if err := h.write(h.st, typ, id); err != nil {
			return pipeline.EntityChangeResult{}, err
		}
	}
	return pipeline.EntityChangeResult{}, nil
}

func withHydrator(t *testing.T, h changes.Hydrator) {
	t.Helper()
	orig := newRefreshHydrator
	t.Cleanup(func() { newRefreshHydrator = orig })
	newRefreshHydrator = func(*config.Config, *store.Store) changes.Hydrator { return h }
}

// ------------------------------------------------------------ write order

// failingFor fails the annotation of one entity and delegates the rest.
type failingFor struct {
	inner focus.Annotator
	id    string
}

func (a failingFor) SetFocusSelected(repo, typ, id, value, origin, actor, at string) (int64, bool, error) {
	if id == a.id {
		return 0, false, errors.New("injected annotation failure")
	}
	return a.inner.SetFocusSelected(repo, typ, id, value, origin, actor, at)
}

func TestSelectAnnotationFailureExitsTwoAndRepairMendsIt(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	f.showDraft(t)
	f.useAnnotator(t, func(a focus.Annotator) focus.Annotator { return failingFor{inner: a, id: "o/r#2"} })

	_, _, err := sel(t, "ok", "--apply")
	assertFocusExit(t, err, 2, "select --repair")
	if len(f.plan(t, showToday)) != 2 {
		t.Fatalf("the rows did not commit")
	}
	if got := f.annotation(t, prKey(2)); got != "<absent>" {
		t.Fatalf("the failed entity was annotated: %q", got)
	}
	r := f.lastRun(t)
	if !r.Exit.Valid || r.Exit.Int64 != 2 {
		t.Fatalf("run exit = %+v, want 2 recorded as partial", r.Exit)
	}
	failed := 0
	for _, a := range r.Counts["annotations"].([]any) {
		if a.(map[string]any)["outcome"] == "failed" {
			failed++
		}
	}
	if failed != 1 {
		t.Errorf("failed annotations recorded = %d, want 1", failed)
	}

	f.useAnnotator(t, nil)
	// repair reads no reply and works without --apply
	out, _, err := sel(t, "", "--repair")
	mustOK(t, err)
	mustContain(t, out, "repair pr:o/r#2 wrote -")
	if got := f.annotation(t, prKey(2)); got != showToday {
		t.Errorf("repair left the annotation at %q", got)
	}
	if rr := f.lastRun(t); rr.Verb != "repair" || !rr.Exit.Valid || rr.Exit.Int64 != 0 {
		t.Errorf("repair run = %+v", rr)
	}
	out, _, err = sel(t, "", "--repair")
	mustOK(t, err)
	if strings.TrimSpace(out) != "" {
		t.Errorf("a second repair wrote %q, want nothing", out)
	}
}

func TestRowsCommitBeforeAnnotationsAndRunRecordIsFinalizedLast(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.showDraft(t)
	observed := false
	f.useAnnotator(t, func(a focus.Annotator) focus.Annotator {
		return annotatorFunc(func(repo, typ, id, value, origin, actor, at string) (int64, bool, error) {
			if !observed {
				observed = true
				// Mid-write: the rows are committed, the draft is gone, the run
				// row exists with no exit code and no annotation exists yet.
				if n := len(f.plan(t, showToday)); n != 1 {
					t.Errorf("rows committed at annotation time = %d, want 1", n)
				}
				if _, found := f.draftRow(t); found {
					t.Errorf("the draft survived the lock transaction")
				}
				r := f.lastRun(t)
				if r.Exit.Valid {
					t.Errorf("exit_code = %d before the annotations finished, want NULL", r.Exit.Int64)
				}
				if got := f.annotation(t, prKey(1)); got != "<absent>" {
					t.Errorf("annotation = %q before its write", got)
				}
			}
			return a.SetFocusSelected(repo, typ, id, value, origin, actor, at)
		})
	})
	_, _, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	if !observed {
		t.Fatalf("the annotator was never called")
	}
	if r := f.lastRun(t); !r.Exit.Valid || r.Exit.Int64 != 0 {
		t.Errorf("exit_code after the run = %+v, want 0 written last", r.Exit)
	}
}

type annotatorFunc func(repo, typ, id, value, origin, actor, at string) (int64, bool, error)

func (fn annotatorFunc) SetFocusSelected(repo, typ, id, value, origin, actor, at string) (int64, bool, error) {
	return fn(repo, typ, id, value, origin, actor, at)
}

func TestSecondConcurrentLockComputesFresh(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.showDraft(t)
	orig := focusSelectBeforeLock
	t.Cleanup(func() { focusSelectBeforeLock = orig })
	nested := false
	focusSelectBeforeLock = func(*store.Store) {
		if nested {
			return
		}
		nested = true
		// A concurrent lock of the same draft commits in the gap.
		if _, _, err := sel(t, "ok", "--apply"); err != nil {
			t.Errorf("concurrent lock: %v", err)
		}
	}
	_, errOut, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	if got := f.planKeys(t, showToday); len(got) != 1 {
		t.Fatalf("plan = %v, want one row (the second lock adds nothing)", got)
	}
	mustContain(t, errOut, "WARNING: no stored draft")
	rs := f.runs(t)
	if len(rs) != 2 || rs[1].Counts["fresh_order"] != true || rs[0].Counts["fresh_order"] != false {
		t.Fatalf("runs = %+v, want the second lock to record fresh_order", rs)
	}
}

func TestRunRecordCarriesDraftAgeAndDrift(t *testing.T) {
	f := newSelFixture(t)
	f.jira(t, "J-1", "first", "2026-10-01", "2026-10-09T08:00:00Z")
	f.jira(t, "J-2", "second", "2026-10-20", "2026-10-09T09:00:00Z")
	f.showDraft(t)
	// The draft is two hours old and a fresh rank now orders the rows the
	// other way round.
	f.jira(t, "J-1", "first", "2026-10-30", "2026-10-10T10:00:00Z")
	f.jira(t, "J-2", "second", "2026-10-02", "2026-10-10T10:00:00Z")
	focusNow = func() time.Time { return focusFixedNow.Add(2 * time.Hour) }
	_, _, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	r := f.lastRun(t)
	if countsInt(r.Counts, "draft_age_seconds") != 7200 {
		t.Errorf("draft_age_seconds = %v, want 7200", r.Counts["draft_age_seconds"])
	}
	if countsInt(r.Counts, "drift_rows") < 1 {
		t.Errorf("drift_rows = %v, want at least 1", r.Counts["drift_rows"])
	}
	if got := strings.Join(f.planKeys(t, showToday), ","); got != "J-1,J-2" {
		t.Errorf("plan = %s, want the DRAFT's order", got)
	}
}

func TestLockOverDegradedSourceRecordsCoverage(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.cfg.Watch.PR.Queries = []string{"mine"}
	v, _ := json.Marshal(map[string]string{"status": "degraded", "reason": "timeout", "at": showAt})
	if err := f.st.SetMeta("change_flow.listing.pr.mine", string(v)); err != nil {
		t.Fatal(err)
	}
	_, _, _ = show(t) // exit 2: coverage incomplete, the draft is still stored
	_, _, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	cov, _ := f.lastRun(t).Counts["coverage"].(map[string]any)
	if cov["incomplete"] != true {
		t.Errorf("coverage = %v, want incomplete recorded in focus_run", cov)
	}
}

// ------------------------------------------------------------ check order

func TestCheckOrderIsUsageThenClosedThenReplyThenHydrationThenWrite(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.closePeriod(t, "2026-10-09")
	h := &writingHydrator{st: f.st}
	withHydrator(t, h)

	// A malformed reply over a closed period is exit 6, not 1.
	_, _, err := sel(t, "bogus", "--apply", "--date", "2026-10-09")
	assertFocusExit(t, err, 6, "period 2026-10-09 is closed")
	// Over an open period the malformed reply is a usage error.
	_, _, err = sel(t, "bogus", "--apply")
	assertFocusExit(t, err, 1, "unrecognized token")
	// A doomed run never hydrates: a first lock for a future date is exit 1
	// before the hand-add of an unknown key would be hydrated.
	_, _, err = sel(t, "ok +J-9", "--apply", "--date", "2026-10-11")
	assertFocusExit(t, err, 1, "is in the future")
	if h.calls != 0 {
		t.Errorf("a doomed run hydrated %d keys", h.calls)
	}
	// The usage and closed outcomes are runs too.
	outcomes := map[int64]int{}
	for _, r := range f.runs(t) {
		outcomes[r.Exit.Int64]++
	}
	if outcomes[6] != 1 || outcomes[1] != 2 {
		t.Errorf("recorded exits = %v, want one 6 and two 1", outcomes)
	}
	if got := f.count(t, "focus_selection"); got != 0 {
		t.Errorf("a doomed run wrote %d rows", got)
	}
}

func TestFocusExitCodesSelect(t *testing.T) {
	t.Run("period week", func(t *testing.T) {
		newSelFixture(t)
		_, _, err := sel(t, "ok", "--period", "week")
		assertFocusExit(t, err, 1, "pass --period day")
	})
	t.Run("empty reply", func(t *testing.T) {
		f := newSelFixture(t)
		f.pr(t, 1, "alpha", showAt)
		_, _, err := sel(t, "", "--apply")
		assertFocusExit(t, err, 1, "reply is empty")
	})
	t.Run("unmigrated store", func(t *testing.T) {
		newFocusFixture(t, "old")
		_, _, err := sel(t, "ok", "--apply")
		assertFocusExit(t, err, 1, "pg-desk migrate --cutover")
	})
	t.Run("repair takes no merge", func(t *testing.T) {
		newSelFixture(t)
		_, _, err := sel(t, "", "--repair", "--merge", "1=2")
		assertFocusExit(t, err, 1, "--repair takes no --merge")
	})
	t.Run("write failed outright", func(t *testing.T) {
		f := newSelFixture(t)
		f.pr(t, 1, "alpha", showAt)
		// Hydration claims success but writes no entity row, so the plan row's
		// foreign key fails and the whole transaction rolls back.
		withHydrator(t, &writingHydrator{st: f.st})
		f.showDraft(t)
		before := f.tables(t, "focus_selection", "focus_period", "annotation")
		_, _, err := sel(t, "ok +J-9", "--apply")
		assertFocusExit(t, err, 3, "")
		if got := f.tables(t, "focus_selection", "focus_period", "annotation"); got != before {
			t.Errorf("a failed lock wrote selection state:\n%s\n---\n%s", before, got)
		}
		if _, found := f.draftRow(t); !found {
			t.Errorf("a failed lock deleted the draft")
		}
		if r := f.lastRun(t); !r.Exit.Valid || r.Exit.Int64 != 3 {
			t.Errorf("run = %+v, want exit 3 recorded", r)
		}
	})
}

// ------------------------------------------------------------ plan lifecycle

func TestFirstLockOfALaterPeriodReplacesThePlan(t *testing.T) {
	f := newSelFixture(t)
	old := f.pr(t, 1, "old plan", "2026-10-08T08:00:00Z")
	f.pr(t, 2, "new", "2026-10-10T08:00:00Z")
	f.lock(t, "2026-10-09", 6, old)
	f.hideEntity(t, old) // no longer a candidate, so the new plan cannot hold it
	if err := f.st.SetAnnotation(store.KVAnnotation{
		Repo: showRepo, EntityType: old.Type, EntityID: old.ID, Key: store.AnnotationFocusSelected,
		Value: "2026-10-09", Origin: "pg-desk", SetBy: "me", SetAt: showAt,
	}); err != nil {
		t.Fatal(err)
	}
	f.showDraft(t)
	out, _, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	mustContain(t, out, "new plan")
	// The old plan's only row is gone and its entity annotated none.
	p, _, _ := f.st.FocusPeriodGet("day", "2026-10-09")
	rows, _ := f.st.FocusPlanRows(p.ID)
	if len(rows) != 0 {
		t.Fatalf("the old plan keeps %d rows", len(rows))
	}
	if got := f.annotation(t, old); got != "none" {
		t.Errorf("old plan annotation = %q, want none (the decider holds the bead)", got)
	}
	if rr := countsInt(f.lastRun(t).Counts["removed"].(map[string]any), "new_period"); rr != 1 {
		t.Errorf("removed.new_period = %d, want 1", rr)
	}
	if got := f.planKeys(t, showToday); len(got) == 0 {
		t.Errorf("the new plan is empty")
	}
}

func TestFirstLockEarlierOrFutureIsUsageAndEmptyReplacesNothing(t *testing.T) {
	f := newSelFixture(t)
	a := f.pr(t, 1, "a", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "b", "2026-10-09T09:00:00Z")
	f.lock(t, showToday, 6, a)

	_, _, err := sel(t, "ok", "--apply", "--date", "2026-10-09")
	assertFocusExit(t, err, 1, "period 2026-10-09 is earlier than the current plan (2026-10-10); history is not rewritten")
	_, _, err = sel(t, "ok", "--apply", "--date", "2026-10-11")
	assertFocusExit(t, err, 1, "period 2026-10-11 is in the future; plan it on the day")
	if got := strings.Join(f.planKeys(t, showToday), ","); got != "o/r#1" {
		t.Errorf("a refused first lock changed the plan: %s", got)
	}
	if _, found, _ := f.st.FocusPeriodGet("day", "2026-10-09"); found {
		t.Errorf("a refused first lock left a focus_period row")
	}
}

func TestEmptyFirstLockReplacesNothingAppliesStrikesAndConsumesTheDraft(t *testing.T) {
	f := newSelFixture(t)
	old := f.pr(t, 1, "old", "2026-10-08T08:00:00Z")
	f.pr(t, 2, "proposed", "2026-10-10T08:00:00Z")
	f.lock(t, "2026-10-09", 6, old)
	f.hideEntity(t, old)
	f.showDraft(t)
	// Strike the lone proposal (a never-selected key writes no row): nothing is
	// inserted, so the old plan is NOT replaced.
	_, _, err := sel(t, "-2", "--apply")
	mustOK(t, err)
	if len(f.plan(t, "2026-10-09")) != 1 {
		t.Errorf("an empty first lock replaced the old plan")
	}
	if _, found := f.draftRow(t); found {
		t.Errorf("an empty first lock left the draft")
	}
	if r := f.lastRun(t); countsInt(r.Counts, "selected") != 0 || !r.Exit.Valid || r.Exit.Int64 != 0 {
		t.Errorf("run = %+v, want an empty ok run", r)
	}
	// -key still deletes the entity's rows in EVERY period.
	f.showDraft(t)
	_, _, err = sel(t, "-1", "--apply")
	mustOK(t, err)
	if len(f.plan(t, "2026-10-09")) != 0 {
		t.Errorf("-key left the old period's row")
	}
	if got := f.annotation(t, old); got != "none" {
		t.Errorf("annotation after -key = %q, want none", got)
	}
}

func TestStrikeDeletesTheRowInEveryPeriod(t *testing.T) {
	f := newSelFixture(t)
	a := f.pr(t, 1, "a", "2026-10-09T08:00:00Z")
	f.lock(t, "2026-10-09", 6, a)
	f.lock(t, showToday, 6, a) // restored backup or manual edit: two periods hold it
	_, _, err := sel(t, "-1", "--apply")
	mustOK(t, err)
	for _, p := range []string{"2026-10-09", showToday} {
		if n := len(f.plan(t, p)); n != 0 {
			t.Errorf("period %s keeps %d rows after a strike", p, n)
		}
	}
	if got := f.annotation(t, a); got != "none" {
		t.Errorf("annotation = %q, want none so the bead is always held", got)
	}
}

func TestLockAfterMidnightWithExplicitDateLocksTheDraftDay(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "a", "2026-10-09T08:00:00Z")
	f.showDraft(t)
	focusNow = func() time.Time { return time.Date(2026, 10, 11, 0, 30, 0, 0, time.UTC) }
	_, _, err := sel(t, "ok", "--apply", "--date", showToday)
	mustOK(t, err)
	if got := f.planKeys(t, showToday); len(got) != 1 {
		t.Errorf("plan of the draft's day = %v", got)
	}
	if r := f.lastRun(t); r.Counts["fresh_order"] != false {
		t.Errorf("fresh_order = %v, want the stored draft used", r.Counts["fresh_order"])
	}
}

func TestLockWithoutDateAfterMidnightIgnoresTheStaleDraft(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "a", "2026-10-09T08:00:00Z")
	f.showDraft(t)
	focusNow = func() time.Time { return time.Date(2026, 10, 11, 0, 30, 0, 0, time.UTC) }
	_, errOut, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	mustContain(t, errOut, "WARNING: no stored draft")
	if got := f.planKeys(t, "2026-10-11"); len(got) != 1 {
		t.Errorf("plan = %v", got)
	}
}

func TestFirstLockOfALaterPeriodDeletesAStaleDraft(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "a", "2026-10-09T08:00:00Z")
	if _, err := f.st.FocusDraftReplace(store.FocusDraft{PeriodType: "day", PeriodKey: "2026-10-09", Cap: 6, MadeAt: showAt, BodyJSON: `{}`}); err != nil {
		t.Fatal(err)
	}
	_, _, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	if _, found := f.draftRow(t); found {
		t.Errorf("the lock left a draft of an earlier period")
	}
}

func TestRemovalReasonPerCause(t *testing.T) {
	f := newSelFixture(t)
	for i := 1; i <= 4; i++ {
		f.pr(t, i, fmt.Sprintf("p%d", i), fmt.Sprintf("2026-10-09T0%d:00:00Z", i))
	}
	_, _, err := sel(t, "ok", "--apply", "--cap", "4")
	mustOK(t, err)
	removed := func() map[string]any { return f.lastRun(t).Counts["removed"].(map[string]any) }

	// operator
	_, _, err = sel(t, "-1", "--apply")
	mustOK(t, err)
	if countsInt(removed(), "operator") != 1 {
		t.Errorf("removed = %v, want operator 1", removed())
	}
	// cap (an explicit lower cap, highest positions first)
	_, _, err = sel(t, "cap=1", "--apply")
	mustOK(t, err)
	if r := removed(); countsInt(r, "cap") != 2 || countsInt(r, "operator") != 0 {
		t.Errorf("removed = %v, want cap 2", r)
	}
	if got := strings.Join(f.planKeys(t, showToday), ","); got != "o/r#2" {
		t.Errorf("plan after cap=1 = %s, want the lowest position kept", got)
	}
	// dropped (hidden since the lock)
	if err := f.st.SetAnnotation(store.KVAnnotation{
		Repo: showRepo, EntityType: "pr", EntityID: "o/r#2", Key: store.AnnotationHidden,
		Value: `{"value":true,"reason":null}`, Origin: "pg-desk", SetBy: "me", SetAt: showAt,
	}); err != nil {
		t.Fatal(err)
	}
	_, _, err = sel(t, "ok", "--apply")
	mustOK(t, err)
	if countsInt(removed(), "dropped") != 1 {
		t.Errorf("removed = %v, want dropped 1 (never rank)", removed())
	}
}

func TestFinishedItemStaysInPlanAndKeepsItsAnnotation(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "a", "2026-10-09T08:00:00Z")
	_, _, err := sel(t, "ok", "--apply")
	mustOK(t, err)
	f.write(t, "pr", "o/r#1", map[string]any{"pr_show": map[string]any{"state": "closed", "merged": true, "title": "a"}}, "2026-10-10T09:00:00Z")
	logBefore := f.count(t, "change_log")
	_, _, err = sel(t, "ok", "--apply")
	mustOK(t, err)
	if got := f.planKeys(t, showToday); len(got) != 1 {
		t.Errorf("a finished item left the plan: %v", got)
	}
	if got := f.annotation(t, prKey(1)); got != showToday {
		t.Errorf("annotation = %q, want unchanged", got)
	}
	if got := f.count(t, "change_log"); got != logBefore {
		t.Errorf("change_log grew by %d: a finished item must not change the annotation", got-logBefore)
	}
	out, _, _ := show(t)
	mustContain(t, out, "finished")
}

// ------------------------------------------------------------ merge

func (f *selFixture) mergeLinks(t *testing.T, kept focus.Key) []store.XrefLink {
	t.Helper()
	ls, err := f.st.ListXrefLinksFrom(showRepo, kept.Type, kept.ID)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.XrefLink
	for _, l := range ls {
		if l.Reason == focus.MergeReason {
			out = append(out, l)
		}
	}
	return out
}

func TestMergePersistsAcrossRunsAndTheAbsorbedItemIsNeverSelected(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "kept", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "absorbed", "2026-10-09T09:00:00Z")
	out, _, err := sel(t, "ok", "--apply", "--merge", "1=2")
	mustOK(t, err)
	mustContain(t, out, "merged o/r#2 into o/r#1 (link recorded; no bead for o/r#2)")
	if got := strings.Join(f.planKeys(t, showToday), ","); got != "o/r#1" {
		t.Fatalf("plan = %s, want only the kept item", got)
	}
	links := f.mergeLinks(t, prKey(1))
	if len(links) != 1 || links[0].ToID != "o/r#2" || links[0].Relation != "references" || !strings.HasPrefix(links[0].Origin, "external:") {
		t.Fatalf("merge links = %+v, want one external references link", links)
	}
	if got := f.annotation(t, prKey(2)); got != "<absent>" {
		t.Errorf("the absorbed item was annotated: %q", got)
	}
	logBefore := f.count(t, "change_log")
	// A later run without the flag does not return the absorbed item.
	_, _, err = sel(t, "ok", "--apply")
	mustOK(t, err)
	if got := strings.Join(f.planKeys(t, showToday), ","); got != "o/r#1" {
		t.Errorf("a re-run selected the absorbed item: %s", got)
	}
	if got := f.count(t, "change_log"); got != logBefore {
		t.Errorf("a merge or its re-run wrote %d change records", got-logBefore)
	}
}

func TestMergeWithUnselectedKeptKeyRecordsLinkOnly(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "kept", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "absorbed", "2026-10-09T09:00:00Z")
	f.pr(t, 3, "other", "2026-10-09T10:00:00Z")
	f.lock(t, showToday, 6, prKey(3))
	before := f.tables(t, "focus_selection", "annotation")
	_, _, err := sel(t, "ok", "--apply", "--merge", "o/r#1=o/r#2")
	mustOK(t, err)
	if len(f.mergeLinks(t, prKey(1))) != 1 {
		t.Fatalf("the link was not recorded")
	}
	if got := f.tables(t, "focus_selection", "annotation"); got != before {
		t.Errorf("a merge wrote a row or an annotation:\n%s\n---\n%s", before, got)
	}
}

func TestMergeSelfIsUsageAndAbsorbedKeyAlreadyInThePlanStays(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "kept", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "absorbed", "2026-10-09T09:00:00Z")
	_, _, err := sel(t, "ok", "--apply", "--merge", "1=1")
	assertFocusExit(t, err, 1, "cannot merge")
	f.lock(t, showToday, 6, prKey(2))
	_, _, err = sel(t, "ok", "--apply", "--merge", "1=2")
	mustOK(t, err)
	if got := strings.Join(f.planKeys(t, showToday), ","); got != "o/r#2" {
		t.Errorf("plan = %s: the absorbed row already in the plan stays and nothing else is added", got)
	}
}

func TestMergeReversedByPlusAbsorbedKey(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "kept", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "absorbed", "2026-10-09T09:00:00Z")
	_, _, err := sel(t, "ok", "--apply", "--merge", "1=2")
	mustOK(t, err)
	out, _, err := sel(t, "+2", "--apply")
	mustOK(t, err)
	mustContain(t, out, "merge of o/r#2 into o/r#1 reversed (link removed)")
	if n := len(f.mergeLinks(t, prKey(1))); n != 0 {
		t.Fatalf("the merge link survives +<absorbedKey>: %d", n)
	}
	// The key is an ordinary candidate again.
	out, _, _ = show(t)
	mustContain(t, out, "o/r#2")
}

// ------------------------------------------------------------ repair

func TestRepairScansEveryAnnotatedEntityAndEveryPlanRowEntity(t *testing.T) {
	f := newSelFixture(t)
	withRow := f.pr(t, 1, "row without annotation", "2026-10-09T08:00:00Z")
	noRow := f.pr(t, 2, "annotation without row", "2026-10-09T09:00:00Z")
	f.lock(t, showToday, 6, withRow)
	if err := f.st.SetAnnotation(store.KVAnnotation{
		Repo: showRepo, EntityType: noRow.Type, EntityID: noRow.ID, Key: store.AnnotationFocusSelected,
		Value: "2026-10-09", Origin: "pg-desk", SetBy: "me", SetAt: showAt,
	}); err != nil {
		t.Fatal(err)
	}
	// preview: --dry-run writes nothing
	before := f.snapshot(t)
	out, _, err := sel(t, "", "--repair", "--dry-run")
	mustOK(t, err)
	mustContain(t, out, "repair pr:o/r#1 would-write -", "repair pr:o/r#2 would-write -")
	if f.snapshot(t) != before {
		t.Fatalf("a dry-run repair wrote")
	}
	_, _, err = sel(t, "", "--repair")
	mustOK(t, err)
	if got := f.annotation(t, withRow); got != showToday {
		t.Errorf("row without annotation: %q, want %s", got, showToday)
	}
	if got := f.annotation(t, noRow); got != "none" {
		t.Errorf("annotation without row: %q, want none", got)
	}
}

func TestRepairIgnoresClosedPeriod(t *testing.T) {
	f := newSelFixture(t)
	a := f.pr(t, 1, "a", "2026-10-08T08:00:00Z")
	f.lock(t, "2026-10-09", 6, a)
	f.closePeriod(t, "2026-10-09")
	_, _, err := sel(t, "", "--repair", "--date", "2026-10-09")
	mustOK(t, err)
	if got := f.annotation(t, a); got != "2026-10-09" {
		t.Errorf("repair of a closed period left %q", got)
	}
}

func TestSelectJSONCarriesTheContractRunIdAndChanges(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.showDraft(t)
	out, _, err := sel(t, "ok", "--apply", "--json")
	mustOK(t, err)
	var m map[string]any
	if jerr := json.Unmarshal([]byte(out), &m); jerr != nil {
		t.Fatalf("--json output is not JSON: %v\n%s", jerr, out)
	}
	if m["contract"] != "pg-desk.focus/v1" || m["verb"] != "select" || m["run_id"] != f.lastRun(t).RunID {
		t.Errorf("envelope = %v", m)
	}
	ch, _ := m["changes"].([]any)
	if len(ch) != 1 {
		t.Fatalf("changes = %v", m["changes"])
	}
	c := ch[0].(map[string]any)
	if c["direction"] != "select" || c["key"] != "pr:o/r#1" || c["effect"] != "mint" || c["cause"] != "" {
		t.Errorf("change = %v", c)
	}
}
