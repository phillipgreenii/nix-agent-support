package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Tests of `pg-desk focus show` and `pg-desk focus replan` against a real
// migrated store, a mutable fixture and the fixed clock of the focus tests
// (2026-10-10 12:00 UTC).

const (
	showToday = "2026-10-10"
	showAt    = "2026-10-10T08:00:00Z"
	showRepo  = "o/r"
)

// showFixture seeds entities into the fixture store.
type showFixture struct {
	*focusFixture
	st *store.Store
	// hydrated is the hydrated_at the writes stamp (fresh by default, so no
	// hydration backlog unless a test asks for one).
	hydrated string
}

func newShowFixture(t *testing.T) *showFixture {
	t.Helper()
	f := newFocusFixture(t, "new")
	f.cfg.Focus.OperatorIdentities = []string{"Pat Example"}
	return &showFixture{focusFixture: f, st: f.open(t), hydrated: "2026-10-10T11:00:00Z"}
}

func (f *showFixture) write(t *testing.T, typ, id string, facts any, at string) focus.Key {
	t.Helper()
	b, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	e := store.Entity{Repo: showRepo, EntityType: typ, EntityID: id, Facts: string(b), AsOf: at, ContentHash: "h" + id}
	if cur, found, _ := f.st.GetEntity(showRepo, typ, id); found {
		e.Version = cur.Version
		if _, err := f.st.WriteEntityStateWithLog(e, cur.Version, f.hydrated, true, []string{"updated"}, "sync", at); err != nil {
			t.Fatalf("rewrite %s: %v", id, err)
		}
		return focus.Key{Type: typ, ID: id}
	}
	if _, err := f.st.WriteEntityStateWithLog(e, 0, f.hydrated, true, []string{"created"}, "sync", at); err != nil {
		t.Fatalf("write %s: %v", id, err)
	}
	return focus.Key{Type: typ, ID: id}
}

// pr seeds an open PR owned by the operator (a seed of the candidate set).
func (f *showFixture) pr(t *testing.T, n int, title, at string) focus.Key {
	t.Helper()
	id := fmt.Sprintf("o/r#%d", n)
	k := f.write(t, "pr", id, map[string]any{"pr_show": map[string]any{"state": "open", "title": title}}, at)
	if err := f.st.UpsertInterpretation(store.Interpretation{
		Repo: showRepo, EntityType: "pr", EntityID: id, Ownership: "mine",
		Enrichment: "{}", Urgency: "{}", Dispositions: "{}", Approvals: "{}", MatchReasons: "[]", AsOf: at,
	}); err != nil {
		t.Fatalf("interpretation %s: %v", id, err)
	}
	return k
}

// jira seeds a Jira issue assigned to the configured operator identity.
func (f *showFixture) jira(t *testing.T, id, title, due, at string) focus.Key {
	t.Helper()
	show := map[string]any{"id": id, "state": "In Progress", "assignee": "Pat Example", "title": title}
	if due != "" {
		show["due_date"] = due
	}
	return f.write(t, "issue", id, map[string]any{"issue_show": show}, at)
}

// lock writes a plan for the period (the keys in order) with a cap.
func (f *showFixture) lock(t *testing.T, period string, cap int, keys ...focus.Key) {
	t.Helper()
	if err := f.st.FocusLockTx(func(tx *store.FocusTx) error {
		p, err := tx.GetOrCreatePeriod("day", period)
		if err != nil {
			return err
		}
		if err := tx.SetPeriodCap(p.ID, cap); err != nil {
			return err
		}
		for i, k := range keys {
			tier := "not_started"
			if _, err := tx.InsertSelection(store.FocusSelection{
				FocusPeriodID: p.ID, Repo: showRepo, EntityType: k.Type, EntityID: k.ID,
				SelectedAt: "2026-10-10T09:00:00Z", RankPosition: i + 1, Tier: &tier,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("lock: %v", err)
	}
}

func (f *showFixture) closePeriod(t *testing.T, period string) {
	t.Helper()
	if err := f.st.FocusLockTx(func(tx *store.FocusTx) error {
		p, err := tx.GetOrCreatePeriod("day", period)
		if err != nil {
			return err
		}
		return tx.ClosePeriod(p.ID, "2026-10-09T20:00:00Z", "done")
	}); err != nil {
		t.Fatalf("close period: %v", err)
	}
}

func (f *showFixture) snapshot(t *testing.T) string {
	t.Helper()
	return snapshotAllTables(t, f.path)
}

func (f *showFixture) draftRow(t *testing.T) (store.FocusDraft, bool) {
	t.Helper()
	d, found, err := f.st.FocusDraftGet()
	if err != nil {
		t.Fatal(err)
	}
	return d, found
}

// runFocusCmd runs a freshly built focus verb command with args and returns
// stdout, stderr and the error main would exit on.
func runFocusCmd(t *testing.T, build func() *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	c := build()
	var out, errb bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errb)
	c.SetArgs(args)
	err := c.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

func show(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	return runFocusCmd(t, newFocusShowCmd, args...)
}

func replan(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	return runFocusCmd(t, newFocusReplanCmd, args...)
}

func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func showJSON(t *testing.T, verb string, args ...string) map[string]any {
	t.Helper()
	run := show
	if verb == "replan" {
		run = replan
	}
	out, _, err := run(t, append([]string{"--json"}, args...)...)
	mustOK(t, err)
	var m map[string]any
	if jerr := json.Unmarshal([]byte(out), &m); jerr != nil {
		t.Fatalf("--json output is not JSON: %v\n%s", jerr, out)
	}
	return m
}

func hasLine(out, prefix string) bool {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

func mustContain(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}

func mustNotContain(t *testing.T, out string, nots ...string) {
	t.Helper()
	for _, n := range nots {
		if strings.Contains(out, n) {
			t.Errorf("output has %q:\n%s", n, out)
		}
	}
}

// sectionOrder lists the section markers of a text screen in print order.
func sectionOrder(out string) []string {
	var got []string
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "scope: "):
			got = append(got, "scope")
		case strings.HasPrefix(l, "coverage: "):
			got = append(got, "coverage")
		case strings.HasPrefix(l, "candidates: "):
			got = append(got, "sources")
		case strings.HasPrefix(l, "DRAFT") || strings.HasPrefix(l, "PLAN ("):
			got = append(got, "draftline")
		case l == "PLAN":
			got = append(got, "PLAN")
		case strings.HasPrefix(l, "cap "):
			got = append(got, "capline")
		case strings.HasPrefix(l, "candidates ("):
			got = append(got, "candidates")
		case l == "epics with children in play":
			got = append(got, "epics")
		case l == "new since draft":
			got = append(got, "newsince")
		}
	}
	return got
}

// Traces INV-FOCUS-12, INV-FOCUS-15, INV-FOCUS-16: one stored draft, re-rendered by a second bare show.
func TestShowStoresOneDraftAndReRendersIt(t *testing.T) {
	f := newShowFixture(t)
	a := f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	_ = a

	out, _, err := show(t)
	mustOK(t, err)
	mustContain(t, out, "DRAFT (stored; reply to lock it in)", "o/r#1", "o/r#2", "scope: o/r")
	d, found := f.draftRow(t)
	if !found || d.PeriodKey != showToday || d.Cap != focus.DefaultCap {
		t.Fatalf("draft row = %+v, found %v", d, found)
	}
	doc, derr := focus.DecodeDraft([]byte(d.BodyJSON))
	mustOK(t, derr)
	if len(doc.Rows) != 2 || !doc.Rows[0].Plus || !doc.Rows[1].Plus {
		t.Fatalf("stored draft rows = %+v, want two proposed", doc.Rows)
	}
	before := f.snapshot(t)

	// A candidate that arrives after the draft is NEW SINCE DRAFT and never
	// joins the table; a fact changed since is read live.
	f.jira(t, "J-1", "overdue jira", "2026-10-01", "2026-10-10T10:00:00Z")
	f.pr(t, 1, "alpha renamed", "2026-10-10T10:00:00Z")
	before = f.snapshot(t)
	focusNow = func() time.Time { return focusFixedNow.Add(2 * time.Hour) }
	out, _, err = show(t)
	mustOK(t, err)
	mustContain(t, out, "draft made 2h ago", "new since draft", "J-1", "alpha renamed")
	if got := f.snapshot(t); got != before {
		t.Fatalf("a re-render changed the store:\n%s\n---\n%s", before, got)
	}
	// The table (before the new-since block) holds the draft's rows only.
	tbl := out[:strings.Index(out, "new since draft")]
	mustNotContain(t, tbl, "J-1")
	if !(strings.Index(tbl, "o/r#1") < strings.Index(tbl, "o/r#2")) {
		t.Errorf("the draft's order is not kept:\n%s", tbl)
	}
}

func TestShowWithLockedPlanStoresNothing(t *testing.T) {
	f := newShowFixture(t)
	a := f.pr(t, 1, "alpha", showAt)
	f.pr(t, 2, "bravo", showAt)
	f.lock(t, showToday, 6, a)
	before := f.snapshot(t)

	out, _, err := show(t)
	mustOK(t, err)
	mustContain(t, out, "PLAN (locked 2026-10-10T09:00:00Z)", "current rank, not frozen", "o/r#2")
	if got := f.snapshot(t); got != before {
		t.Fatalf("show with a plan wrote to the store:\n%s\n---\n%s", before, got)
	}
	if _, found := f.draftRow(t); found {
		t.Errorf("a draft was stored over a locked plan")
	}
}

// Traces INV-FOCUS-16: --no-store stores no draft.
func TestShowNoStoreStoresNothing(t *testing.T) {
	f := newShowFixture(t)
	f.pr(t, 1, "alpha", showAt)
	before := f.snapshot(t)

	out, _, err := show(t, "--no-store")
	mustOK(t, err)
	mustContain(t, out, "DRAFT (computed, not stored)", "o/r#1")
	if got := f.snapshot(t); got != before {
		t.Fatalf("--no-store changed the store:\n%s\n---\n%s", before, got)
	}

	// --no-store never re-renders a stored draft either, and says so.
	_, _, err = show(t)
	mustOK(t, err)
	before = f.snapshot(t)
	out, _, err = show(t, "--no-store")
	mustOK(t, err)
	mustContain(t, out, "NOTICE stored draft ignored (--no-store)", "DRAFT (computed, not stored)")
	mustNotContain(t, out, "draft made")
	if got := f.snapshot(t); got != before {
		t.Fatalf("--no-store with a stored draft changed the store")
	}
}

// Traces INV-FOCUS-16: a closed period stores no draft.
func TestShowOnAClosedPeriodStoresNothing(t *testing.T) {
	f := newShowFixture(t)
	f.pr(t, 1, "alpha", showAt)
	f.closePeriod(t, showToday)
	before := f.snapshot(t)

	out, _, err := show(t)
	mustOK(t, err)
	mustContain(t, out, "period closed")
	if got := f.snapshot(t); got != before {
		t.Fatalf("show on a closed period changed the store")
	}
	if _, found := f.draftRow(t); found {
		t.Errorf("a draft was stored for a closed period")
	}
}

// Traces INV-FOCUS-16: the plan check is re-made inside the transaction.
func TestShowLockRaceStoresNothingAndPrintsThePlan(t *testing.T) {
	f := newShowFixture(t)
	a := f.pr(t, 1, "alpha", showAt)
	f.pr(t, 2, "bravo", showAt)
	orig := focusShowBeforeStore
	t.Cleanup(func() { focusShowBeforeStore = orig })
	focusShowBeforeStore = func(*store.Store) { f.lock(t, showToday, 6, a) }

	out, _, err := show(t)
	mustOK(t, err)
	mustContain(t, out, "PLAN (locked 2026-10-10T09:00:00Z)")
	mustNotContain(t, out, "DRAFT")
	if _, found := f.draftRow(t); found {
		t.Errorf("a draft was stored although a lock committed first")
	}
}

func TestStoredDraftOfALaterPeriodIsIgnoredWithNotice(t *testing.T) {
	f := newShowFixture(t)
	f.pr(t, 1, "alpha", showAt)
	later := store.FocusDraft{PeriodType: "day", PeriodKey: "2026-10-11", Cap: 6, MadeAt: "2026-10-10T11:00:00Z", BodyJSON: `{"contract":"pg-desk.focus.draft/v1"}`}
	if ok, err := f.st.FocusDraftStore(later, "2026-10-11"); err != nil || !ok {
		t.Fatalf("seed later draft: %v %v", ok, err)
	}
	before := f.snapshot(t)
	out, _, err := show(t)
	mustOK(t, err)
	mustContain(t, out, "NOTICE stored draft is for 2026-10-11; pass --date 2026-10-11 or run replan", "DRAFT (computed, not stored)")
	if got := f.snapshot(t); got != before {
		t.Fatalf("a later period's draft was disturbed")
	}
}

func TestStaleEarlierPeriodDraftIsReplacedByShow(t *testing.T) {
	f := newShowFixture(t)
	f.pr(t, 1, "alpha", showAt)
	old := store.FocusDraft{PeriodType: "day", PeriodKey: "2026-10-09", Cap: 6, MadeAt: "2026-10-09T23:50:00Z", BodyJSON: `{"contract":"pg-desk.focus.draft/v1"}`}
	if ok, err := f.st.FocusDraftStore(old, "2026-10-09"); err != nil || !ok {
		t.Fatalf("seed stale draft: %v %v", ok, err)
	}
	out, _, err := show(t)
	mustOK(t, err)
	mustContain(t, out, "DRAFT (stored; reply to lock it in)")
	mustNotContain(t, out, "NOTICE stored draft")
	d, _ := f.draftRow(t)
	if d.PeriodKey != showToday {
		t.Fatalf("stale draft not replaced: %+v", d)
	}
}

func TestStaleEarlierPeriodDraftIsReplacedByReplan(t *testing.T) {
	f := newShowFixture(t)
	f.pr(t, 1, "alpha", showAt)
	old := store.FocusDraft{PeriodType: "day", PeriodKey: "2026-10-09", Cap: 6, MadeAt: "2026-10-09T23:50:00Z", BodyJSON: `{"contract":"pg-desk.focus.draft/v1"}`}
	if ok, err := f.st.FocusDraftStore(old, "2026-10-09"); err != nil || !ok {
		t.Fatalf("seed stale draft: %v %v", ok, err)
	}
	_, _, err := replan(t)
	mustOK(t, err)
	if d, _ := f.draftRow(t); d.PeriodKey != showToday {
		t.Fatalf("stale draft not replaced by replan: %+v", d)
	}
}

// Traces INV-FOCUS-16: a day other than today stores no draft.
func TestNonTodayDateStoresNothing(t *testing.T) {
	f := newShowFixture(t)
	f.pr(t, 1, "alpha", showAt)
	before := f.snapshot(t)
	for name, run := range map[string]func(*testing.T, ...string) (string, string, error){"show": show, "replan": replan} {
		out, _, err := run(t, "--date", "2026-10-09")
		mustOK(t, err)
		mustContain(t, out, "NOTICE period 2026-10-09 is not today; no draft stored")
		if got := f.snapshot(t); got != before {
			t.Fatalf("%s --date (not today) changed the store", name)
		}
	}
}

func TestHasPlanIsModeIndependent(t *testing.T) {
	f := newShowFixture(t)
	a := f.pr(t, 1, "alpha", showAt)
	f.pr(t, 2, "bravo", showAt)
	f.lock(t, showToday, 6, a)

	// A stored replan draft over a locked plan reads mode draft, has_plan true.
	_, _, err := replan(t)
	mustOK(t, err)
	m := showJSON(t, "show")
	if m["mode"] != "draft" || m["has_plan"] != true {
		t.Fatalf("mode %v has_plan %v, want draft true", m["mode"], m["has_plan"])
	}
	// The existence check with --no-store stores nothing and reads plan.
	before := f.snapshot(t)
	m = showJSON(t, "show", "--no-store")
	if m["mode"] != "plan" || m["has_plan"] != true {
		t.Fatalf("--no-store: mode %v has_plan %v, want plan true", m["mode"], m["has_plan"])
	}
	if rows, _ := m["plan_rows"].([]any); len(rows) != 1 {
		t.Errorf("plan_rows = %v, want the one locked row", m["plan_rows"])
	}
	if got := f.snapshot(t); got != before {
		t.Fatalf("show --json --no-store changed the store")
	}
	// A period with no plan reads has_plan false.
	m = showJSON(t, "show", "--no-store", "--date", "2026-10-09")
	if m["has_plan"] != false {
		t.Errorf("has_plan = %v for a planless period", m["has_plan"])
	}
}

func TestShowJsonUnclosedPeriodsAndNotice(t *testing.T) {
	f := newShowFixture(t)
	a := f.pr(t, 1, "alpha", showAt)
	f.lock(t, "2026-10-08", 6, a)

	m := showJSON(t, "show", "--no-store")
	for _, k := range []string{"contract", "verb", "mode", "has_plan", "plan_rows", "unclosed_periods", "period", "made_at", "cap", "rows", "epics", "coverage", "rank_inputs"} {
		if _, ok := m[k]; !ok {
			t.Errorf("--json lacks member %q", k)
		}
	}
	if m["contract"] != focusContract {
		t.Errorf("contract = %v", m["contract"])
	}
	if u, _ := m["unclosed_periods"].([]any); len(u) != 1 || u[0] != "2026-10-08" {
		t.Errorf("unclosed_periods = %v", m["unclosed_periods"])
	}
	out, _, err := show(t, "--no-store")
	mustOK(t, err)
	mustContain(t, out, "NOTICE period 2026-10-08 was never closed")
}

func TestUnreadableDraftIsTreatedAsAbsent(t *testing.T) {
	for name, body := range map[string]string{
		"garbage":          `not json`,
		"missing contract": `{"mode":"draft"}`,
		"unknown contract": `{"contract":"pg-desk.focus.draft/v9"}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newShowFixture(t)
			f.pr(t, 1, "alpha", showAt)
			bad := store.FocusDraft{PeriodType: "day", PeriodKey: showToday, Cap: 6, MadeAt: "2026-10-10T11:00:00Z", BodyJSON: body}
			if ok, err := f.st.FocusDraftStore(bad, showToday); err != nil || !ok {
				t.Fatalf("seed: %v %v", ok, err)
			}
			// The existence guard never exits 1 for a bad draft and stores nothing.
			before := f.snapshot(t)
			_, _, err := show(t, "--json", "--no-store")
			mustOK(t, err)
			if got := f.snapshot(t); got != before {
				t.Fatalf("--no-store replaced an unreadable draft")
			}
			out, _, err := show(t)
			mustOK(t, err)
			mustContain(t, out, "NOTICE stored draft unreadable; replaced", "DRAFT (stored; reply to lock it in)")
			d, _ := f.draftRow(t)
			if _, derr := focus.DecodeDraft([]byte(d.BodyJSON)); derr != nil {
				t.Errorf("the replaced draft is still unreadable: %v", derr)
			}
			// replan prints the same notice and replaces too.
			if ok, err := f.st.FocusDraftReplace(store.FocusDraft{PeriodType: "day", PeriodKey: showToday, Cap: 6, MadeAt: "2026-10-10T11:00:00Z", BodyJSON: body}); err != nil || ok {
				t.Fatalf("reseed: %v %v", ok, err)
			}
			out, _, err = replan(t)
			mustOK(t, err)
			mustContain(t, out, "NOTICE stored draft unreadable; replaced")
		})
	}
}

func TestEmptyIdentityListIsANoticeWithExitZero(t *testing.T) {
	f := newShowFixture(t)
	f.cfg.Focus.OperatorIdentities = nil
	f.pr(t, 1, "alpha", showAt)
	for name, args := range map[string][]string{"show": {"--no-store"}, "replan": nil} {
		run := show
		if name == "replan" {
			run = replan
		}
		out, _, err := run(t, args...)
		mustOK(t, err)
		mustContain(t, out, "NOTICE focus.operator_identities is empty: no Jira issue can be a candidate by assignment", "o/r#1")
	}
}

func TestNoBeadCandidatesLineNamesTheLabel(t *testing.T) {
	f := newShowFixture(t)
	f.cfg.BeadIDPattern = `^bd-`
	f.pr(t, 1, "alpha", showAt)
	f.write(t, "issue", "bd-1", map[string]any{"issue_show": map[string]any{"id": "bd-1", "state": "open", "title": "unlabelled bead"}}, showAt)
	out, _, err := show(t, "--no-store")
	mustOK(t, err)
	mustContain(t, out, "candidates: pr 1, jira 0, bead 0 (label pg-focus-planable matches none of 1 open beads; label one or use +key)")
}

func TestEmptyCandidateSetExitsZero(t *testing.T) {
	f := newShowFixture(t)
	out, _, err := show(t)
	mustOK(t, err)
	mustContain(t, out, "coverage: complete", "candidates: pr 0, jira 0, bead 0", "(none)")
	m := showJSON(t, "show", "--no-store")
	if rows, _ := m["rows"].([]any); len(rows) != 0 {
		t.Errorf("rows = %v, want none", rows)
	}
	_ = f
}

// Traces INV-FOCUS-12: a replan replaces the draft.
func TestReplanReplacesTheStoredDraft(t *testing.T) {
	f := newShowFixture(t)
	f.pr(t, 1, "alpha", showAt)
	_, _, err := show(t, "--cap", "1")
	mustOK(t, err)
	if d, _ := f.draftRow(t); d.Cap != 1 {
		t.Fatalf("first draft cap = %d", d.Cap)
	}
	f.pr(t, 2, "bravo", "2026-10-10T10:00:00Z")
	out, _, err := replan(t)
	mustOK(t, err)
	mustContain(t, out, "DRAFT (stored; reply to lock it in)", "o/r#2")
	d, _ := f.draftRow(t)
	if d.Cap != focus.DefaultCap {
		t.Errorf("replaced draft cap = %d, want %d", d.Cap, focus.DefaultCap)
	}
	doc, derr := focus.DecodeDraft([]byte(d.BodyJSON))
	mustOK(t, derr)
	if len(doc.Rows) != 2 {
		t.Errorf("replaced draft rows = %d, want 2", len(doc.Rows))
	}
}

// Traces INV-FOCUS-16: replan writes only the single draft row.
func TestReplanWritesTheDraftRowAndNoRunRow(t *testing.T) {
	f := newShowFixture(t)
	a := f.pr(t, 1, "alpha", showAt)
	f.pr(t, 2, "bravo", showAt)
	f.lock(t, showToday, 6, a)
	runs := func() string { return tableDump(f.snapshot(t), "focus_run") }
	periods := func() string { return tableDump(f.snapshot(t), "focus_period") }
	sel := func() string { return tableDump(f.snapshot(t), "focus_selection") }
	r0, p0, s0 := runs(), periods(), sel()
	_, _, err := replan(t)
	mustOK(t, err)
	if _, found := f.draftRow(t); !found {
		t.Fatalf("replan stored no draft row")
	}
	if runs() != r0 || periods() != p0 || sel() != s0 {
		t.Errorf("replan wrote a focus_run, focus_period or focus_selection row")
	}
}

// tableDump returns the lines of one table from snapshotAllTables.
func tableDump(snapshot, table string) string {
	var sb strings.Builder
	in := false
	for _, l := range strings.Split(snapshot, "\n") {
		if strings.HasPrefix(l, "## ") {
			in = strings.HasPrefix(l, "## "+table+" ")
		}
		if in {
			sb.WriteString(l + "\n")
		}
	}
	return sb.String()
}

func TestReplanOnClosedPeriodPrintsPlanAndStoresNothing(t *testing.T) {
	f := newShowFixture(t)
	a := f.pr(t, 1, "alpha", showAt)
	f.lock(t, showToday, 6, a)
	f.closePeriod(t, showToday)
	before := f.snapshot(t)
	out, _, err := replan(t)
	mustOK(t, err)
	mustContain(t, out, "PLAN (locked", "period closed")
	if got := f.snapshot(t); got != before {
		t.Fatalf("replan on a closed period changed the store")
	}
	m := showJSON(t, "replan")
	if m["mode"] != "plan" {
		t.Errorf("mode = %v, want plan", m["mode"])
	}
}

func TestReplanVsConcurrentLockRecomputesAsPlanModeReplan(t *testing.T) {
	f := newShowFixture(t)
	a := f.pr(t, 1, "alpha", showAt)
	f.pr(t, 2, "bravo", showAt)
	orig := focusReplanBeforeStore
	t.Cleanup(func() { focusReplanBeforeStore = orig })
	locked := false
	focusReplanBeforeStore = func(*store.Store) {
		if !locked {
			locked = true
			f.lock(t, showToday, 6, a)
		}
	}
	out, _, err := replan(t)
	mustOK(t, err)
	d, found := f.draftRow(t)
	if !found {
		t.Fatalf("no draft stored after the recompute")
	}
	doc, derr := focus.DecodeDraft([]byte(d.BodyJSON))
	mustOK(t, derr)
	// A plan-mode replan: the plan row is out of the draft and nothing is proposed.
	if len(doc.Rows) != 1 || doc.Rows[0].Key.ID != "o/r#2" || doc.Rows[0].Plus {
		t.Fatalf("recomputed draft rows = %+v, want only o/r#2 and no proposal", doc.Rows)
	}
	mustContain(t, out, "DRAFT (stored; reply to lock it in)")
}

func TestInitialDraftAndReplanShareSkeleton(t *testing.T) {
	f := newShowFixture(t)
	a := f.pr(t, 1, "alpha", showAt)
	f.pr(t, 2, "bravo", showAt)
	initial, _, err := show(t, "--no-store")
	mustOK(t, err)
	f.lock(t, showToday, 6, a)
	re, _, err := replan(t)
	mustOK(t, err)
	plan, _, err := show(t, "--no-store")
	mustOK(t, err)
	want := []string{"scope", "coverage", "sources", "draftline", "PLAN", "capline", "candidates"}
	for name, out := range map[string]string{"initial": initial, "replan": re, "plan": plan} {
		got := sectionOrder(out)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s sections = %v, want %v:\n%s", name, got, want, out)
		}
	}
}

// Traces INV-FOCUS-13: a locked plan renders in its recorded order, drift only counted.
func TestShowPlanModeFreezesOrderAndShowsDrift(t *testing.T) {
	f := newShowFixture(t)
	a := f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	b := f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	f.lock(t, showToday, 6, b, a) // recorded order: bravo, alpha
	f.jira(t, "J-1", "overdue jira", "2026-10-01", "2026-10-10T10:00:00Z")

	out, _, err := show(t, "--no-store")
	mustOK(t, err)
	if !(strings.Index(out, "o/r#2") < strings.Index(out, "o/r#1")) {
		t.Errorf("plan rows are not in the recorded order:\n%s", out)
	}
	mustContain(t, out, "NOTICE 1 candidates now rank above the plan's last row; run replan to re-order", "current rank, not frozen", "J-1")
	// J-1 is a candidate, not a plan row: it is in the candidates block.
	plan := out[strings.Index(out, "\nPLAN\n"):strings.Index(out, "candidates (current rank")]
	mustNotContain(t, plan, "J-1")
}

func TestReplanShowsPlanBlockThenSortedCandidates(t *testing.T) {
	f := newShowFixture(t)
	a := f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	f.lock(t, showToday, 6, a)
	f.jira(t, "J-1", "overdue jira", "2026-10-01", "2026-10-10T10:00:00Z")
	out, _, err := replan(t)
	mustOK(t, err)
	iPlan := strings.Index(out, "\nPLAN\n")
	iCands := strings.Index(out, "candidates (")
	if iPlan < 0 || iCands < iPlan {
		t.Fatalf("PLAN block must precede the candidates block:\n%s", out)
	}
	plan, cands := out[iPlan:iCands], out[iCands:]
	mustContain(t, plan, "o/r#1", "*")
	mustNotContain(t, plan, "+ ")
	// Re-ranked: the overdue issue sorts ahead of the other PR, and is NEW.
	if !(strings.Index(cands, "J-1") < strings.Index(cands, "o/r#2")) {
		t.Errorf("candidates are not sorted by the fresh rank:\n%s", cands)
	}
	mustContain(t, cands, "new")
}

func TestShowOfAPlannedPeriodWhileADraftIsStoredForAnotherPeriod(t *testing.T) {
	f := newShowFixture(t)
	a := f.pr(t, 1, "alpha", showAt)
	f.lock(t, "2026-10-09", 6, a)
	other := store.FocusDraft{PeriodType: "day", PeriodKey: showToday, Cap: 6, MadeAt: "2026-10-10T11:00:00Z", BodyJSON: `{"contract":"pg-desk.focus.draft/v1"}`}
	if ok, err := f.st.FocusDraftStore(other, showToday); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	before := f.snapshot(t)
	out, _, err := show(t, "--date", "2026-10-09")
	mustOK(t, err)
	mustContain(t, out, "PLAN (locked", "o/r#1")
	if got := f.snapshot(t); got != before {
		t.Fatalf("showing a planned period disturbed the stored draft")
	}
}

func TestShowCapWithAnApplicableDraftIsAUsageError(t *testing.T) {
	f := newShowFixture(t)
	f.pr(t, 1, "alpha", showAt)
	_, _, err := show(t)
	mustOK(t, err)
	_, _, err = show(t, "--cap", "3")
	assertFocusExit(t, err, 1, "--cap cannot be used with an applicable stored draft")
	_, _, err = show(t, "--cap", "0")
	assertFocusExit(t, err, 1, "--cap 0 must be positive")
	_ = f
}

func TestShowTruncatesAndFootnotesWithoutAll(t *testing.T) {
	f := newShowFixture(t)
	for i := 1; i <= 14; i++ {
		f.pr(t, i, fmt.Sprintf("pr %d", i), fmt.Sprintf("2026-10-09T%02d:00:00Z", i))
	}
	out, _, err := show(t, "--no-store")
	mustOK(t, err)
	// cap 6 proposed in PLAN; of the other 8 candidates all fit under ten.
	mustNotContain(t, out, "shown of")
	f.cfg.Focus.PendingGateAge = ""
	for i := 15; i <= 30; i++ {
		f.pr(t, i, fmt.Sprintf("pr %d", i), fmt.Sprintf("2026-10-09T%02d:30:00Z", i-14))
	}
	out, _, err = show(t, "--no-store")
	mustOK(t, err)
	mustContain(t, out, "10 shown of 24 candidates (--all)")
	out, _, err = show(t, "--no-store", "--all")
	mustOK(t, err)
	mustNotContain(t, out, "shown of")
	m := showJSON(t, "show", "--no-store")
	if rows, _ := m["rows"].([]any); len(rows) != 30 {
		t.Errorf("--json rows = %d, want every candidate (30)", len(rows))
	}
}

// TestFocusExitCodesShowAndReplan covers the show exit-code rows of the spec: coverage
// complete 0, a degraded or failed listing 2, a backlog above
// focus.coverage_backlog_max 2, no candidates computable 3, unmigrated 1.
//
// Traces INV-FOCUS-17: the exit codes of show and replan.
func TestFocusExitCodesShowAndReplan(t *testing.T) {
	setListing := func(t *testing.T, f *showFixture, query, status string) {
		t.Helper()
		f.cfg.Watch.PR.Queries = []string{query}
		v, _ := json.Marshal(map[string]string{"status": status, "reason": "timeout", "at": showAt})
		if err := f.st.SetMeta("change_flow.listing.pr."+query, string(v)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		prep func(t *testing.T, f *showFixture)
		code int
		want string
	}{
		{"complete", func(t *testing.T, f *showFixture) { f.pr(t, 1, "a", showAt); setListing(t, f, "mine", "ok") }, 0, ""},
		{"empty set with ok listings", func(t *testing.T, f *showFixture) { setListing(t, f, "mine", "ok") }, 0, ""},
		{"degraded listing", func(t *testing.T, f *showFixture) { f.pr(t, 1, "a", showAt); setListing(t, f, "mine", "degraded") }, 2, "coverage incomplete"},
		{"failed listing with candidates", func(t *testing.T, f *showFixture) { f.pr(t, 1, "a", showAt); setListing(t, f, "mine", "failed") }, 2, "coverage incomplete"},
		{"unknown listing", func(t *testing.T, f *showFixture) {
			f.pr(t, 1, "a", showAt)
			f.cfg.Watch.PR.Queries = []string{"never-ran"}
		}, 2, "never-ran"},
		{"backlog above the bound", func(t *testing.T, f *showFixture) {
			// Both entities were hydrated long before the sweep max age.
			f.hydrated = "2026-10-01T00:00:00Z"
			f.pr(t, 1, "a", showAt)
			f.pr(t, 2, "b", showAt)
			setListing(t, f, "mine", "ok")
			n := 1
			f.cfg.Focus.CoverageBacklogMax = &n
		}, 2, "backlog"},
		{"no candidates computable", func(t *testing.T, f *showFixture) { setListing(t, f, "mine", "failed") }, 3, "no candidates are computable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newShowFixture(t)
			tc.prep(t, f)
			before := f.snapshot(t)
			out, _, err := show(t, "--no-store")
			if got := focusExitCode(err); got != tc.code {
				t.Fatalf("exit = %d, want %d (err %v)\n%s", got, tc.code, err, out)
			}
			if tc.code != 0 {
				assertFocusExit(t, err, tc.code, tc.want)
			}
			if tc.code == 2 {
				mustContain(t, out, "coverage: INCOMPLETE", "o/r#1") // shown, never silently omitted
			}
			if got := f.snapshot(t); got != before {
				t.Errorf("--no-store changed the store")
			}
		})
	}
	t.Run("replan shares show's exit codes and still stores the draft", func(t *testing.T) {
		f := newShowFixture(t)
		f.pr(t, 1, "a", showAt)
		setListing(t, f, "mine", "degraded")
		_, _, err := replan(t)
		assertFocusExit(t, err, 2, "coverage incomplete")
		if _, found := f.draftRow(t); !found {
			t.Errorf("replan with incomplete coverage must still store its draft")
		}
	})
	t.Run("unmigrated store", func(t *testing.T) {
		newFocusFixture(t, "old")
		_, _, err := show(t)
		assertFocusExit(t, err, 1, "pg-desk migrate --cutover")
	})
	t.Run("a total failure stores no draft", func(t *testing.T) {
		f := newShowFixture(t)
		setListing(t, f, "mine", "failed")
		_, _, err := show(t)
		if focusExitCode(err) != 3 {
			t.Fatalf("exit = %d", focusExitCode(err))
		}
		if _, found := f.draftRow(t); found {
			t.Errorf("a draft was stored for a total failure")
		}
	})
	t.Run("failed source notice above the table", func(t *testing.T) {
		f := newShowFixture(t)
		f.pr(t, 1, "a", showAt)
		setListing(t, f, "mine", "failed")
		out, _, _ := show(t, "--no-store")
		mustContain(t, out, "NOTICE mine FAILED (timeout): candidates from this pr query are NOT all in this table")
		if strings.Index(out, "NOTICE mine FAILED") > strings.Index(out, "\nPLAN\n") {
			t.Errorf("the failed-source notice is not above the table")
		}
	})
}

// Traces INV-FOCUS-18: the bead column.
func TestShowBeadColumnReadsTheLinkedBead(t *testing.T) {
	f := newShowFixture(t)
	f.cfg.BeadIDPattern = `^bd-`
	f.cfg.Focus.SuppressedKind = "example-kind"
	a := f.pr(t, 1, "alpha", showAt)
	// A minted bead names its source in its own metadata and is linked to it
	// by relation source.
	bead := f.write(t, "issue", "bd-9", map[string]any{"issue_show": map[string]any{
		"id": "bd-9", "state": "deferred", "assignee": "", "title": "Focus alpha",
		"metadata": map[string]string{"source_type": "pr", "source_id": a.ID, "focus_hold": "struck"},
	}}, showAt)
	if err := f.st.ReplaceDerivedXrefs(showRepo, "issue", bead.ID, []store.XrefLink{{
		Repo: showRepo, FromType: "issue", FromID: bead.ID, ToType: "pr", ToID: a.ID,
		Relation: "source", Origin: "derived:source-entity", FirstSeen: showAt, LastConfirmed: showAt,
	}}); err != nil {
		t.Fatal(err)
	}
	m := showJSON(t, "show", "--no-store")
	rows, _ := m["rows"].([]any)
	var got map[string]any
	for _, r := range rows {
		if row, _ := r.(map[string]any); row["key"] == "pr:o/r#1" {
			got = row
		}
	}
	if got == nil {
		t.Fatalf("no row for the PR in %v", rows)
	}
	if got["bead"] != "held" || got["bead_state"] != "held" {
		t.Errorf("bead column = %v / %v, want held", got["bead"], got["bead_state"])
	}
}

// TestShowGoldens pins the text and JSON of each mode. Set UPDATE_GOLDEN=1 to
// rewrite them.
func TestShowGoldens(t *testing.T) {
	f := newShowFixture(t)
	a := f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	golden := func(name, got string) {
		t.Helper()
		p := filepath.Join("testdata", "focus", name)
		if os.Getenv("UPDATE_GOLDEN") == "1" {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read golden %s: %v (UPDATE_GOLDEN=1 writes it)", p, err)
		}
		if strings.HasSuffix(name, ".json") {
			// The formatter re-indents committed JSON, so JSON goldens compare as values.
			var w, g any
			if err := json.Unmarshal(want, &w); err != nil {
				t.Fatalf("golden %s is not JSON: %v", name, err)
			}
			if err := json.Unmarshal([]byte(got), &g); err != nil {
				t.Fatalf("output for %s is not JSON: %v", name, err)
			}
			if !reflect.DeepEqual(w, g) {
				t.Errorf("%s differs from its golden:\n%s\n--- want:\n%s", name, got, want)
			}
			return
		}
		if string(want) != got {
			t.Errorf("%s differs from its golden:\n%s\n--- want:\n%s", name, got, want)
		}
	}
	out, _, err := show(t, "--no-store")
	mustOK(t, err)
	golden("show_draft.golden.txt", out)
	out, _, err = show(t, "--no-store", "--json")
	mustOK(t, err)
	golden("show_draft.golden.json", out)

	f.lock(t, showToday, 6, a)
	out, _, err = show(t, "--no-store")
	mustOK(t, err)
	golden("show_plan.golden.txt", out)
	out, _, err = show(t, "--no-store", "--json")
	mustOK(t, err)
	golden("show_plan.golden.json", out)
	out, _, err = replan(t, "--json")
	mustOK(t, err)
	golden("replan.golden.json", out)
	out, _, err = show(t)
	mustOK(t, err)
	golden("show_rerender.golden.txt", out)
	_ = fmt.Sprint
}
