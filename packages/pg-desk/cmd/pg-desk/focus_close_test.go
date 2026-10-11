package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Tests of `pg-desk focus close` against a real migrated store and the fixed
// clock of the focus tests (2026-10-10 12:00 UTC).

const closeNow = "2026-10-10T12:00:00Z"

// closeVerb runs close with stdin.
func closeVerb(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	c := newFocusCloseCmd()
	var out, errb strings.Builder
	c.SetOut(&out)
	c.SetErr(&errb)
	c.SetIn(strings.NewReader(stdin))
	c.SetArgs(args)
	err := c.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

// summaryJSON is the stdin document of a summary.
func summaryJSON(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"summary": s})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func closeJSON(t *testing.T, stdin string, args ...string) map[string]any {
	t.Helper()
	out, _, err := closeVerb(t, stdin, append([]string{"--json"}, args...)...)
	mustOK(t, err)
	var m map[string]any
	if jerr := json.Unmarshal([]byte(out), &m); jerr != nil {
		t.Fatalf("--json output is not JSON: %v\n%s", jerr, out)
	}
	return m
}

func (f *selFixture) period(t *testing.T, key string) (store.FocusPeriod, bool) {
	t.Helper()
	p, found, err := f.st.FocusPeriodGet("day", key)
	if err != nil {
		t.Fatal(err)
	}
	return p, found
}

func TestCloseWritesTheNoteAndCreatesThePeriodRow(t *testing.T) {
	f := newSelFixture(t)
	if _, found := f.period(t, showToday); found {
		t.Fatal("fixture already has the period row")
	}
	summary := "Shipped \"A\".\nLine two: carries on."
	out, errOut, err := closeVerb(t, summaryJSON(t, summary))
	mustOK(t, err)
	if out != "closed 2026-10-10\n" {
		t.Errorf("stdout = %q, want the one line `closed <date>`", out)
	}
	p, found := f.period(t, showToday)
	if !found || p.ClosedAt != closeNow || p.CloseNote != summary {
		t.Fatalf("period = %+v found %v, want closed at %s with the summary verbatim", p, found, closeNow)
	}
	mustContain(t, errOut, `"verb":"close"`, `"contract":"pg-desk.focus-select/v1"`, `"dry_run":false`, `"exit_code":0`)
}

func TestCloseWritesAFocusRunRow(t *testing.T) {
	f := newSelFixture(t)
	_, _, err := closeVerb(t, summaryJSON(t, "done"))
	mustOK(t, err)
	r := f.lastRun(t)
	if r.Verb != "close" || !r.Exit.Valid || r.Exit.Int64 != 0 {
		t.Fatalf("run = %+v, want an exited close", r)
	}
	p, _ := f.period(t, showToday)
	if id := f.runPeriodID(t); id != p.ID {
		t.Errorf("run focus_period_id = %d, want the closed period %d", id, p.ID)
	}
}

// runPeriodID is the focus_period_id of the newest focus_run row (0 when NULL).
func (f *selFixture) runPeriodID(t *testing.T) int64 {
	t.Helper()
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var id int64
	if err := db.QueryRow(`SELECT COALESCE(focus_period_id, 0) FROM focus_run ORDER BY id DESC LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// rawExec runs one statement against the fixture store file.
func (f *selFixture) rawExec(t *testing.T, q string) {
	t.Helper()
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func TestCloseIdempotentWithSameSummary(t *testing.T) {
	f := newSelFixture(t)
	_, _, err := closeVerb(t, summaryJSON(t, "done"))
	mustOK(t, err)
	first, _ := f.period(t, showToday)
	focusNow = func() time.Time { return focusFixedNow.Add(3 * time.Hour) }
	before := f.tables(t, "focus_period", "focus_selection", "focus_draft")
	out, _, err := closeVerb(t, summaryJSON(t, "done"))
	mustOK(t, err)
	if out != "already closed 2026-10-10 (same summary)\n" {
		t.Errorf("stdout = %q", out)
	}
	if got := f.tables(t, "focus_period", "focus_selection", "focus_draft"); got != before {
		t.Errorf("a repeated close changed the store:\n%s\n---\n%s", before, got)
	}
	again, _ := f.period(t, showToday)
	if again.ClosedAt != first.ClosedAt {
		t.Errorf("closed_at moved from %s to %s", first.ClosedAt, again.ClosedAt)
	}
	m := closeJSON(t, summaryJSON(t, "done"))
	if m["already_closed"] != true || m["closed_at"] != closeNow || m["close_note"] != "done" || m["dry_run"] != false {
		t.Errorf("json = %v", m)
	}
	if rs := f.runs(t); len(rs) != 3 {
		t.Errorf("runs = %d, want one row per non-dry close run (3)", len(rs))
	}
}

func TestCloseWithADifferentSummaryExitsSixAndKeepsTheFirst(t *testing.T) {
	f := newSelFixture(t)
	_, _, err := closeVerb(t, summaryJSON(t, "first"))
	mustOK(t, err)
	before := f.tables(t, "focus_period", "focus_selection", "focus_draft")
	_, _, err = closeVerb(t, summaryJSON(t, "second"))
	assertFocusExit(t, err, 6, closeNow)
	if !strings.Contains(err.Error(), "--date <tomorrow>") || !strings.Contains(err.Error(), "different summary") {
		t.Errorf("message = %q, want the recorded time, the different summary and the way out", err)
	}
	if got := f.tables(t, "focus_period", "focus_selection", "focus_draft"); got != before {
		t.Errorf("a refused close changed the store:\n%s\n---\n%s", before, got)
	}
	if p, _ := f.period(t, showToday); p.CloseNote != "first" {
		t.Errorf("close_note = %q, want the recorded one", p.CloseNote)
	}
	if r := f.lastRun(t); r.Verb != "close" || !r.Exit.Valid || r.Exit.Int64 != 6 {
		t.Errorf("run = %+v, want exit 6 recorded", r)
	}
}

func TestCloseDeletesTheDraftOfItsPeriod(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.showDraft(t)
	_, _, err := closeVerb(t, summaryJSON(t, "done"))
	mustOK(t, err)
	if _, found := f.draftRow(t); found {
		t.Errorf("the draft of the closed period survived")
	}
}

func TestCloseKeepsTheDraftOfAnotherPeriod(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.showDraft(t) // the draft is for today
	_, _, err := closeVerb(t, summaryJSON(t, "yesterday"), "--date", showYesterday)
	mustOK(t, err)
	d, found := f.draftRow(t)
	if !found || d.PeriodKey != showToday {
		t.Errorf("draft = %+v found %v, want today's draft untouched", d, found)
	}
	if p, _ := f.period(t, showYesterday); p.ClosedAt == "" {
		t.Errorf("yesterday was not closed")
	}
}

func TestCloseKeepsThePlanRows(t *testing.T) {
	f := newSelFixture(t)
	k1 := f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.lock(t, showToday, 6, k1)
	_, _, err := closeVerb(t, summaryJSON(t, "done"))
	mustOK(t, err)
	if got := strings.Join(f.planKeys(t, showToday), ","); got != "o/r#1" {
		t.Errorf("plan = %q, want the day's rows kept for the history", got)
	}
}

func TestCloseDryRunWritesNothingNotEvenThePeriodRow(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.showDraft(t)
	before := f.snapshot(t)
	out, errOut, err := closeVerb(t, summaryJSON(t, "line1\nline2"), "--dry-run")
	mustOK(t, err)
	if after := f.snapshot(t); after != before {
		t.Errorf("--dry-run changed the store:\n%s\n---\n%s", before, after)
	}
	if _, found := f.period(t, showToday); found {
		t.Errorf("--dry-run created the period row")
	}
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, `"line1\nline2"`) || !strings.HasPrefix(out, "would close 2026-10-10") {
		t.Errorf("stdout = %q, want one line reporting the resolved close_note", out)
	}
	mustContain(t, errOut, `"dry_run":true`)
	m := closeJSON(t, summaryJSON(t, "x"), "--dry-run")
	if m["run_id"] != nil || m["dry_run"] != true || m["close_note"] != "x" || m["already_closed"] != false || m["closed_at"] != closeNow {
		t.Errorf("json = %v", m)
	}
}

func TestCloseDryRunOfAClosedDayReportsTheRefusalAndWritesNothing(t *testing.T) {
	f := newSelFixture(t)
	_, _, err := closeVerb(t, summaryJSON(t, "first"))
	mustOK(t, err)
	before := f.snapshot(t)
	_, _, err = closeVerb(t, summaryJSON(t, "second"), "--dry-run")
	assertFocusExit(t, err, 6, "")
	if _, _, err := closeVerb(t, summaryJSON(t, "first"), "--dry-run"); err != nil {
		t.Errorf("dry-run of the identical summary: %v", err)
	}
	if after := f.snapshot(t); after != before {
		t.Errorf("--dry-run changed the store")
	}
}

func TestCloseJSONEnvelope(t *testing.T) {
	newSelFixture(t)
	m := closeJSON(t, summaryJSON(t, "all good"))
	if m["contract"] != "pg-desk.focus/v1" || m["verb"] != "close" || m["period"] != showToday ||
		m["closed_at"] != closeNow || m["close_note"] != "all good" || m["already_closed"] != false || m["dry_run"] != false {
		t.Errorf("json = %v", m)
	}
	if id, _ := m["run_id"].(string); len(id) != 26 {
		t.Errorf("run_id = %v, want a ULID", m["run_id"])
	}
}

func TestCloseDecoderIgnoresUnknownMembersAndRequiresAStringSummary(t *testing.T) {
	f := newSelFixture(t)
	_, _, err := closeVerb(t, `{"extra": [1,2], "summary": "ok", "n": null}`)
	mustOK(t, err)
	if p, _ := f.period(t, showToday); p.CloseNote != "ok" {
		t.Errorf("close_note = %q", p.CloseNote)
	}
	for _, bad := range []string{
		``, "  \n", `not json`, `[]`, `"summary"`, `null`, `{}`,
		`{"summary": 3}`, `{"summary": null}`, `{"summary": ["a"]}`,
		`{"summary": "a"} {"summary": "b"}`, `{"summary": "a"} trailing`,
	} {
		g := newSelFixture(t)
		before := g.tables(t, "focus_period")
		_, _, err := closeVerb(t, bad)
		assertFocusExit(t, err, 1, "focus close")
		if got := g.tables(t, "focus_period"); got != before {
			t.Errorf("malformed stdin %q wrote a period row", bad)
		}
	}
}

func TestCloseFutureDateIsExitOne(t *testing.T) {
	f := newSelFixture(t)
	_, _, err := closeVerb(t, summaryJSON(t, "x"), "--date", "2026-10-11")
	assertFocusExit(t, err, 1, "later than today")
	if _, found := f.period(t, "2026-10-11"); found {
		t.Errorf("a future close created a period row")
	}
	// A past day closes fine, with the NOTE line.
	_, errOut, err := closeVerb(t, summaryJSON(t, "x"), "--date", "2026-10-09")
	mustOK(t, err)
	mustContain(t, errOut, "NOTE: writing into period 2026-10-09, not today")
}

func TestCloseFailuresStillWriteTheRunRow(t *testing.T) {
	f := newSelFixture(t)
	_, _, err := closeVerb(t, `{}`)
	assertFocusExit(t, err, 1, "")
	if r := f.lastRun(t); r.Verb != "close" || !r.Exit.Valid || r.Exit.Int64 != 1 {
		t.Errorf("run = %+v, want exit 1 recorded", r)
	}
	// A usage error under --dry-run writes no row.
	g := newSelFixture(t)
	_, _, err = closeVerb(t, `{}`, "--dry-run")
	assertFocusExit(t, err, 1, "")
	if n := len(g.runs(t)); n != 0 {
		t.Errorf("a dry run wrote %d run rows", n)
	}
}

func TestFocusExitCodesClose(t *testing.T) {
	t.Run("usage: malformed stdin", func(t *testing.T) {
		newSelFixture(t)
		_, _, err := closeVerb(t, "nope")
		assertFocusExit(t, err, 1, "pipe {")
	})
	t.Run("usage: period week", func(t *testing.T) {
		newSelFixture(t)
		_, _, err := closeVerb(t, summaryJSON(t, "x"), "--period", "week")
		assertFocusExit(t, err, 1, "pass --period day")
	})
	t.Run("usage: bad date", func(t *testing.T) {
		newSelFixture(t)
		_, _, err := closeVerb(t, summaryJSON(t, "x"), "--date", "x")
		assertFocusExit(t, err, 1, "pass --date YYYY-MM-DD")
	})
	t.Run("usage: unmigrated store", func(t *testing.T) {
		newFocusFixture(t, "old")
		_, _, err := closeVerb(t, summaryJSON(t, "x"))
		assertFocusExit(t, err, 1, "pg-desk migrate --cutover")
	})
	t.Run("usage: future date", func(t *testing.T) {
		newSelFixture(t)
		_, _, err := closeVerb(t, summaryJSON(t, "x"), "--date", "2026-12-01")
		assertFocusExit(t, err, 1, "later than today")
	})
	t.Run("ok", func(t *testing.T) {
		newSelFixture(t)
		_, _, err := closeVerb(t, summaryJSON(t, "x"))
		mustOK(t, err)
	})
	t.Run("already closed, same summary", func(t *testing.T) {
		newSelFixture(t)
		_, _, err := closeVerb(t, summaryJSON(t, "x"))
		mustOK(t, err)
		_, _, err = closeVerb(t, summaryJSON(t, "x"))
		mustOK(t, err)
	})
	t.Run("already closed, different summary", func(t *testing.T) {
		newSelFixture(t)
		_, _, err := closeVerb(t, summaryJSON(t, "x"))
		mustOK(t, err)
		_, _, err = closeVerb(t, summaryJSON(t, "y"))
		assertFocusExit(t, err, 6, "use --date <tomorrow>")
	})
	t.Run("total: a store write failure", func(t *testing.T) {
		f := newSelFixture(t)
		// A trigger makes the close UPDATE fail after the period row exists.
		f.rawExec(t, `CREATE TRIGGER close_fails BEFORE UPDATE OF closed_at ON focus_period BEGIN SELECT RAISE(ABORT, 'no close'); END`)
		before := f.tables(t, "focus_period", "focus_draft")
		_, _, err := closeVerb(t, summaryJSON(t, "x"))
		assertFocusExit(t, err, 3, "check the pg-desk store")
		if got := f.tables(t, "focus_period", "focus_draft"); got != before {
			t.Errorf("a failed close left a partial write:\n%s\n---\n%s", before, got)
		}
		if r := f.lastRun(t); !r.Exit.Valid || r.Exit.Int64 != 3 {
			t.Errorf("run = %+v, want exit 3 recorded", r)
		}
	})
}

// The same-transaction rule: when the write fails after the draft deletion
// would have run, the draft survives (nothing partial commits).
func TestCloseIsAtomicWithTheDraftDeletion(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.showDraft(t)
	f.rawExec(t, `CREATE TRIGGER run_fails BEFORE INSERT ON focus_run BEGIN SELECT RAISE(ABORT, 'no run'); END`)
	_, _, err := closeVerb(t, summaryJSON(t, "x"))
	assertFocusExit(t, err, 3, "")
	if _, found := f.draftRow(t); !found {
		t.Errorf("the draft was deleted although the close did not commit")
	}
	if p, found := f.period(t, showToday); found && p.ClosedAt != "" {
		t.Errorf("the period was closed although the close did not commit: %+v", p)
	}
}

func TestCloseSelectAndPullOnAClosedDayExitSix(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	_, _, err := closeVerb(t, summaryJSON(t, "done"))
	mustOK(t, err)
	_, _, err = pull(t, "--top", "1")
	assertFocusExit(t, err, 6, "period 2026-10-10 is closed; use --date <tomorrow>")
	_, _, err = sel(t, "ok", "--apply")
	assertFocusExit(t, err, 6, "period 2026-10-10 is closed; use --date <tomorrow>")
}
