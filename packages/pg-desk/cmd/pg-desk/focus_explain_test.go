package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Tests of `pg-desk focus explain` against a real migrated store and the
// fixed clock of the focus tests (2026-10-10 12:00 UTC).

func explain(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	return runFocusCmd(t, newFocusExplainCmd, args...)
}

// explainFixture is a store that holds one of each case the verb explains.
//
//	o/r#1   open PR of the operator, with a minted bead bd-9 (held); not in the plan
//	o/r#2   open PR of the operator, in today's plan (cap 6), no bead yet
//	o/r#3   hidden PR of the operator
//	ZZ-7    unassigned issue linked from o/r#1: a linked candidate
//	ZZ-5    issue of someone else, no link: not a candidate
//	EP-1    epic of the operator whose open child EP-2 covers it
//	bd-9    the minted bead of o/r#1 (marker struck, deferred, audit comment stored)
type explainFixture struct {
	*showFixture
	pr1, pr2, pr3, zz7, zz5, ep1, ep2, bd9 focus.Key
}

func newExplainFixture(t *testing.T) *explainFixture {
	t.Helper()
	f := &explainFixture{showFixture: newShowFixture(t)}
	f.cfg.BeadIDPattern = `^bd-`
	f.cfg.TicketPatterns = []string{`[A-Z]+-[0-9]+`}
	f.pr1 = f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr2 = f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	f.pr3 = f.pr(t, 3, "charlie", "2026-10-09T10:00:00Z")
	if err := f.st.SetAnnotation(store.KVAnnotation{
		Repo: showRepo, EntityType: f.pr3.Type, EntityID: f.pr3.ID, Key: store.AnnotationHidden,
		Value: `{"value":true,"reason":null}`, Origin: "pg-desk", SetBy: "me", SetAt: showAt,
	}); err != nil {
		t.Fatal(err)
	}
	f.zz7 = f.write(t, "issue", "ZZ-7", map[string]any{"issue_show": map[string]any{
		"id": "ZZ-7", "state": "Open", "title": "linked",
	}}, showAt)
	f.zz5 = f.write(t, "issue", "ZZ-5", map[string]any{"issue_show": map[string]any{
		"id": "ZZ-5", "state": "Open", "assignee": "Someone Else", "title": "not mine",
	}}, showAt)
	f.ep1 = f.write(t, "issue", "EP-1", map[string]any{"issue_show": map[string]any{
		"id": "EP-1", "state": "In Progress", "assignee": "Pat Example", "issue_type": "epic", "title": "the epic",
	}}, showAt)
	f.ep2 = f.write(t, "issue", "EP-2", map[string]any{"issue_show": map[string]any{
		"id": "EP-2", "state": "In Progress", "assignee": "Pat Example", "parent": "EP-1", "title": "the child",
	}}, showAt)
	f.bd9 = f.write(t, "issue", "bd-9", map[string]any{"issue_show": map[string]any{
		"id": "bd-9", "state": "deferred", "title": "Focus alpha",
		"metadata": map[string]string{"source_type": "pr", "source_id": f.pr1.ID, "focus_hold": "struck"},
		"comments": []map[string]string{
			{"body": "an unrelated note"},
			{"body": "pg-decider audit\nrule: focus.item\nop: hold\nbecause: struck"},
			{"body": "a later unrelated note"},
		},
	}}, showAt)
	f.link(t, f.bd9, f.pr1, "source", "derived:source-entity")
	f.link(t, f.pr1, f.zz7, "mentions", "derived:mentions")
	f.lock(t, showToday, 6, f.pr2)
	return f
}

func (f *explainFixture) link(t *testing.T, from, to focus.Key, relation, origin string) {
	t.Helper()
	if err := f.st.ReplaceDerivedXrefs(showRepo, from.Type, from.ID, []store.XrefLink{{
		Repo: showRepo, FromType: from.Type, FromID: from.ID, ToType: to.Type, ToID: to.ID,
		Relation: relation, Origin: origin, FirstSeen: showAt, LastConfirmed: showAt,
	}}); err != nil {
		t.Fatal(err)
	}
}

// explainGolden compares got with testdata/focus/explain/<name>; UPDATE_GOLDEN=1
// rewrites it. JSON goldens compare as values (the formatter re-indents them).
func explainGolden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", "focus", "explain", name)
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

func explainJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	out, _, err := explain(t, append([]string{"--json"}, args...)...)
	mustOK(t, err)
	var m map[string]any
	if jerr := json.Unmarshal([]byte(out), &m); jerr != nil {
		t.Fatalf("--json output is not JSON: %v\n%s", jerr, out)
	}
	return m
}

// TestFocusExplainOutput pins the text and JSON of each case: not a
// candidate and why, a linked candidate with its via, an epic suppressed by
// its child, a row in the plan, a minted bead, a held source, and an unknown
// key.
func TestFocusExplainOutput(t *testing.T) {
	newExplainFixture(t)
	cases := []struct {
		name string
		key  string
		want []string
	}{
		{"not_candidate", "ZZ-5", []string{"candidate: no (not-seed-or-linked)", "plan row: not in the plan", "tier and deciding keys: not ranked"}},
		{"hidden", "o/r#3", []string{"candidate: no (hidden)"}},
		{"linked", "ZZ-7", []string{"candidate: yes", "via: pr:o/r#1 by mentions", "slot: ranked"}},
		{"epic_suppressed", "EP-1", []string{"candidate: yes", "slot: suppressed by issue:EP-2"}},
		{"in_plan", "2", []string{"candidate: yes", "plan row: selected_at 2026-10-10T09:00:00Z, rank_position 1, tier not_started", "annotation: focus_selected=unset", "bead: pending 3h"}},
		{"minted", "bd-9", []string{"candidate: no (minted-bead, deferred)", "bead: n/a (epic or bead) (marker struck; latest audit: pg-decider audit rule: focus.item op: hold because: struck)"}},
		{"held", "1", []string{"candidate: yes", "bead: held (marker struck; latest audit: pg-decider audit rule: focus.item op: hold because: struck)", "skip reasons (recomputed now): already handled", "plan row: not in the plan"}},
		{"unknown", "o/r#99", []string{"pr:o/r#99: not held by pg-desk"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, errOut, err := explain(t, c.key)
			mustOK(t, err)
			if errOut != "" {
				t.Errorf("stderr = %q, want none", errOut)
			}
			mustContain(t, out, c.want...)
			mustContain(t, out, "run_id: null")
			explainGolden(t, c.name+".golden.txt", out)
			jout, _, err := explain(t, "--json", c.key)
			mustOK(t, err)
			explainGolden(t, c.name+".golden.json", jout)
		})
	}
}

func TestExplainTextLineOrder(t *testing.T) {
	f := newExplainFixture(t)
	out, _, err := explain(t, f.pr2.ID)
	mustOK(t, err)
	var labels []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		labels = append(labels, l[:strings.Index(l, ":")])
	}
	want := []string{"key", "candidate", "tier and deciding keys", "slot", "cap line", "plan row", "annotation", "bead", "skip reasons (recomputed now)", "run_id"}
	if !reflect.DeepEqual(labels, want) {
		t.Errorf("labels = %v, want %v", labels, want)
	}
	// A linked candidate adds the via line, right after candidate.
	out, _, err = explain(t, "ZZ-7")
	mustOK(t, err)
	if !strings.Contains(out, "candidate: yes\nvia: ") {
		t.Errorf("via line is not right after candidate:\n%s", out)
	}
}

func TestExplainHasNoFlipHistory(t *testing.T) {
	f := newExplainFixture(t)
	wantMembers := []string{"annotation", "bead", "candidate", "cap_position", "contract", "deciding", "key", "plan_row", "reasons", "run_id", "slot", "tier", "verb", "via"}
	for _, key := range []string{f.pr1.ID, f.pr2.ID, "ZZ-5", "ZZ-7", "EP-1", "bd-9", "o/r#99"} {
		m := explainJSON(t, key)
		var got []string
		for k := range m {
			got = append(got, k)
			if strings.Contains(strings.ToLower(k), "flip") || strings.Contains(strings.ToLower(k), "history") {
				t.Errorf("%s: --json has a history member %q", key, k)
			}
		}
		if !sameSet(got, wantMembers) {
			t.Errorf("%s: members = %v, want %v", key, got, wantMembers)
		}
		if m["contract"] != "pg-desk.focus/v1" || m["verb"] != "explain" {
			t.Errorf("%s: contract/verb = %v/%v", key, m["contract"], m["verb"])
		}
		if v, ok := m["run_id"]; !ok || v != nil {
			t.Errorf("%s: run_id = %v (present %v), want null", key, v, ok)
		}
		out, _, err := explain(t, key)
		mustOK(t, err)
		mustNotContain(t, strings.ToLower(out), "flip", "history", "was previously", "used to be")
	}
}

func sameSet(a, b []string) bool {
	m := map[string]int{}
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}

// TestExplainIsReadOnly: a full-store snapshot, every table and meta, is
// byte-identical after every shape of the verb, and no period, run or
// change row appears for a day nothing was written for.
func TestExplainIsReadOnly(t *testing.T) {
	f := newExplainFixture(t)
	before := f.snapshot(t)
	for _, args := range [][]string{
		{f.pr1.ID},
		{f.pr2.ID},
		{"--json", f.pr2.ID},
		{"ZZ-7"},
		{"EP-1"},
		{"bd-9"},
		{"o/r#99"},
		{"--json", "o/r#99"},
		{"--date", "2026-10-15", f.pr2.ID},
		{"--date", "2026-10-09", "--json", f.pr2.ID},
	} {
		if _, _, err := explain(t, args...); err != nil {
			t.Fatalf("explain %v: %v", args, err)
		}
		if after := f.snapshot(t); after != before {
			t.Fatalf("explain %v changed the store:\n%s", args, firstDiff(before, after))
		}
	}
	if _, found, err := f.st.FocusPeriodGet("day", "2026-10-15"); err != nil || found {
		t.Errorf("period 2026-10-15: found=%v err=%v, want no row", found, err)
	}
	if _, found := f.draftRow(t); found {
		t.Error("explain stored a draft")
	}
	if tbl := tableDump(before, "focus_run"); strings.Contains(tbl, "explain") {
		t.Errorf("focus_run holds an explain row:\n%s", tbl)
	}
}

func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) && i < len(bl); i++ {
		if al[i] != bl[i] {
			return "- " + al[i] + "\n+ " + bl[i]
		}
	}
	return "length differs"
}

func TestExplainUnknownKeyIsNotHeldAndExitsZero(t *testing.T) {
	newExplainFixture(t)
	out, _, err := explain(t, "o/r#99")
	if err != nil || focusExitCode(err) != 0 {
		t.Fatalf("err = %v, want exit 0", err)
	}
	mustContain(t, out, "not held")
	m := explainJSON(t, "o/r#99")
	if m["candidate"] != false || !reflect.DeepEqual(m["reasons"], []any{"not-in-store"}) {
		t.Errorf("json = %v, want candidate false with reason not-in-store", m)
	}
}

func TestExplainUnmigratedStoreExitsOne(t *testing.T) {
	newFocusFixture(t, "old")
	_, _, err := explain(t, "o/r#1")
	if err == nil || focusExitCode(err) != 1 {
		t.Fatalf("err = %v, want exit 1", err)
	}
	var fe *focusError
	if !errors.As(err, &fe) || !strings.Contains(fe.msg, "not migrated") || !strings.Contains(fe.Remedy(), "migrate --cutover") {
		t.Errorf("error = %v, want the unmigrated refusal with its remedy", err)
	}
}

func TestExplainUsageErrorsExitOne(t *testing.T) {
	newExplainFixture(t)
	for _, args := range [][]string{{}, {"a", "b"}, {"not a key!"}, {"--date", "nope", "1"}, {"--period", "week", "1"}, {"other/repo#4"}} {
		_, _, err := explain(t, args...)
		if err == nil || focusExitCode(err) != 1 {
			t.Errorf("explain %v: err = %v, want exit 1", args, err)
		}
	}
}

func TestExplainKeyFormsMatchSelect(t *testing.T) {
	f := newExplainFixture(t)
	want := explainJSON(t, f.pr2.ID)
	for _, form := range []string{"2", "#2", "o/r#2", "https://example.test/o/r/pull/2"} {
		if got := explainJSON(t, form); !reflect.DeepEqual(got, want) {
			t.Errorf("key form %q explains differently:\n%v\nwant %v", form, got, want)
		}
	}
}

func TestExplainDecidingKeysAndCapLine(t *testing.T) {
	f := newExplainFixture(t)
	// #1 and #2 are both started PRs of the same tier; the older one (#1) is
	// first, and age is the key that separates them.
	m1, m2 := explainJSON(t, "1"), explainJSON(t, "2")
	d1, _ := m1["deciding"].(map[string]any)
	d2, _ := m2["deciding"].(map[string]any)
	if d1["above"] != "" || d1["below"] != "age" || d2["above"] != "age" {
		t.Errorf("deciding = %v / %v, want #1 below age and #2 above age", d1, d2)
	}
	if m1["tier"] != "started" {
		t.Errorf("tier = %v, want started", m1["tier"])
	}
	if got, ok := m1["cap_position"].(float64); !ok || got != 1 {
		t.Errorf("#1 cap_position = %v, want 1", m1["cap_position"])
	}
	if got, ok := m2["cap_position"].(float64); !ok || got != 2 {
		t.Errorf("#2 cap_position = %v, want 2", m2["cap_position"])
	}
	// A cap of 1 on the addressed period pushes #2 outside the cap line.
	if err := f.st.FocusLockTx(func(tx *store.FocusTx) error {
		p, err := tx.GetOrCreatePeriod("day", showToday)
		if err != nil {
			return err
		}
		return tx.SetPeriodCap(p.ID, 1)
	}); err != nil {
		t.Fatal(err)
	}
	if m2 = explainJSON(t, "2"); m2["cap_position"] != nil {
		t.Errorf("#2 cap_position = %v, want null with cap 1", m2["cap_position"])
	}
	out, _, err := explain(t, "2")
	mustOK(t, err)
	mustContain(t, out, "cap line: outside the cap line (cap 1)")
	out, _, err = explain(t, "1")
	mustOK(t, err)
	mustContain(t, out, "cap line: position 1 of cap 1")
}

func TestExplainPlanRowOfAnotherPeriodIsNamed(t *testing.T) {
	f := newShowFixture(t)
	a := f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.lock(t, "2026-10-09", 6, a)
	out, _, err := explain(t, "1")
	mustOK(t, err)
	mustContain(t, out, "(in period 2026-10-09, not the addressed period 2026-10-10)")
	m := explainJSON(t, "1")
	if pr, _ := m["plan_row"].(map[string]any); pr["period"] != "2026-10-09" || pr["rank_position"] != float64(1) {
		t.Errorf("plan_row = %v", m["plan_row"])
	}
	out, _, err = explain(t, "--date", "2026-10-09", "1")
	mustOK(t, err)
	mustNotContain(t, out, "not the addressed period")
	mustContain(t, out, "plan row: selected_at 2026-10-10T09:00:00Z, rank_position 1, tier not_started")
}

func TestExplainBeadAuditStates(t *testing.T) {
	f := newShowFixture(t)
	f.cfg.BeadIDPattern = `^bd-`
	a := f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	mint := func(id string, extra map[string]any) {
		show := map[string]any{
			"id": id, "state": "open", "title": "bead",
			"metadata": map[string]string{"source_type": "pr", "source_id": a.ID},
		}
		for k, v := range extra {
			show[k] = v
		}
		bead := f.write(t, "issue", id, map[string]any{"issue_show": show}, showAt)
		if err := f.st.ReplaceDerivedXrefs(showRepo, "issue", id, []store.XrefLink{{
			Repo: showRepo, FromType: "issue", FromID: id, ToType: "pr", ToID: a.ID,
			Relation: "source", Origin: "derived:source-entity", FirstSeen: showAt, LastConfirmed: showAt,
		}}); err != nil {
			t.Fatal(err)
		}
		_ = bead
	}
	// No bead at all: pending, and no audit to report.
	out, _, err := explain(t, "1")
	mustOK(t, err)
	mustContain(t, out, "bead: pending (marker none; latest audit: none)")
	// A bead whose snapshot has no comment list: the audit is not stored.
	mint("bd-1", nil)
	out, _, err = explain(t, "1")
	mustOK(t, err)
	mustContain(t, out, "bead: open (marker none; audit: not stored)")
	// A snapshot with comments but no audit comment: none.
	mint("bd-1", map[string]any{"comments": []string{"hello"}})
	out, _, err = explain(t, "1")
	mustOK(t, err)
	mustContain(t, out, "bead: open (marker none; latest audit: none)")
	// The latest audit comment wins.
	mint("bd-1", map[string]any{"comments": []string{"rule: focus.item\nop: mint", "x", "rule: focus.item\nop: hold"}})
	m := explainJSON(t, "1")
	if b, _ := m["bead"].(map[string]any); b["audit"] != "rule: focus.item\nop: hold" {
		t.Errorf("bead = %v, want the latest audit comment", m["bead"])
	}
}
