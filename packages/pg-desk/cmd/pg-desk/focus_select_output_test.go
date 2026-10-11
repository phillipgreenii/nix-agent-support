package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Tests of what `pg-desk focus select` prints (spec section 7.2 step 7): the
// CHANGES block, the echo table and their --json members. Goldens live under
// testdata/focus/select/, one .txt and one .json per case named after the
// test that uses them; UPDATE_GOLDEN=1 rewrites them.

// bead seeds a minted bead named by src (linked by relation source).
func (f *selFixture) bead(t *testing.T, src focus.Key, id, status, assignee, hold string) {
	t.Helper()
	meta := map[string]string{"source_type": src.Type, "source_id": src.ID}
	if hold != "" {
		meta["focus_hold"] = hold
	}
	f.write(t, "issue", id, map[string]any{"issue_show": map[string]any{
		"id": id, "state": status, "assignee": assignee, "title": "Focus " + id, "metadata": meta,
	}}, showAt)
	if err := f.st.ReplaceDerivedXrefs(showRepo, "issue", id, []store.XrefLink{{
		Repo: showRepo, FromType: "issue", FromID: id, ToType: src.Type, ToID: src.ID,
		Relation: "source", Origin: "derived:source-entity", FirstSeen: showAt, LastConfirmed: showAt,
	}}); err != nil {
		t.Fatal(err)
	}
}

// selectGolden compares got with the golden testdata/focus/select/<name>
// (JSON compares as values: the formatter re-indents committed JSON).
func selectGolden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", "focus", "select", name)
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

// effectsScenario is a plan of five rows whose beads span every strike effect
// and three candidates whose beads span every select effect:
//
//	plan   #1 open bead, #2 claimed bead, bd-5 is a bead itself, #3 no bead
//	cands  #6 no bead, #7 held bead, #8 claimed bead
func effectsScenario(t *testing.T) *selFixture {
	t.Helper()
	f := newSelFixture(t)
	f.cfg.BeadIDPattern = `^bd-`
	p1 := f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	p2 := f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	b5 := f.jira(t, "bd-5", "epic five", "", "2026-10-09T10:00:00Z")
	p3 := f.pr(t, 3, "charlie", "2026-10-09T11:00:00Z")
	f.pr(t, 6, "foxtrot", "2026-10-09T12:00:00Z")
	p7 := f.pr(t, 7, "golf", "2026-10-09T13:00:00Z")
	p8 := f.pr(t, 8, "hotel", "2026-10-09T14:00:00Z")
	f.bead(t, p1, "bd-1", "open", "", "")
	f.bead(t, p2, "bd-2", "in_progress", "worker-1", "")
	f.bead(t, p7, "bd-7", "deferred", "", "struck")
	f.bead(t, p8, "bd-8", "in_progress", "worker-2", "")
	f.lock(t, showToday, 6, p1, p2, b5, p3)
	return f
}

const effectsReply = "-1 -2 -bd-5 -3 +6 +7 +8"

func TestSelectGoldenEchoEffects(t *testing.T) {
	f := effectsScenario(t)
	before := f.snapshot(t)
	out, _, err := sel(t, effectsReply, "--dry-run")
	mustOK(t, err)
	if f.snapshot(t) != before {
		t.Fatalf("a dry run changed the store")
	}
	selectGolden(t, "TestSelectGoldenEchoEffects.txt", out)
	// Every effect word and the two phrases the spec names.
	mustContain(
		t, out,
		"will mint", "will hold", "held, will release", "nothing to do",
		"claimed by worker-1, left running (stop it with bd; pg-desk cannot)",
		"is bead bd-5; strike does not defer it",
		"claimed by worker-2, no change",
	)
	jout, _, err := sel(t, effectsReply, "--dry-run", "--json")
	mustOK(t, err)
	selectGolden(t, "TestSelectGoldenEchoEffects.json", jout)

	var m struct {
		Echo []struct {
			Key, Effect, Because, Bead string
		} `json:"echo"`
	}
	if err := json.Unmarshal([]byte(jout), &m); err != nil {
		t.Fatal(err)
	}
	effects := map[string]string{}
	for _, e := range m.Echo {
		effects[e.Key] = e.Effect
	}
	want := map[string]string{
		"pr:o/r#1": "hold", "pr:o/r#2": "none", "issue:bd-5": "none", "pr:o/r#3": "none",
		"pr:o/r#6": "mint", "pr:o/r#7": "release", "pr:o/r#8": "none",
	}
	if !reflect.DeepEqual(effects, want) {
		t.Errorf("effects = %v, want %v", effects, want)
	}
}

func TestSelectGoldenCauses(t *testing.T) {
	f := newSelFixture(t)
	var keys []focus.Key
	for i := 1; i <= 4; i++ {
		keys = append(keys, f.pr(t, i, string(rune('a'+i-1))+"-item", "2026-10-09T08:00:00Z"))
	}
	f.lock(t, showToday, 4, keys...)
	f.hideEntity(t, keys[2]) // #3 lost candidacy: hidden
	const reply = "-1 cap=1" // #1 by the operator, #4 by the cap, #3 dropped
	out, _, err := sel(t, reply, "--dry-run")
	mustOK(t, err)
	selectGolden(t, "TestSelectGoldenCauses.txt", out)
	jout, _, err := sel(t, reply, "--dry-run", "--json")
	mustOK(t, err)
	selectGolden(t, "TestSelectGoldenCauses.json", jout)

	var m struct {
		Changes []struct {
			Key       string
			Cause     string
			CauseText string `json:"cause_text"`
		} `json:"changes"`
	}
	if err := json.Unmarshal([]byte(jout), &m); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range m.Changes {
		got[c.Key] = c.Cause + "=" + c.CauseText
	}
	want := map[string]string{
		"pr:o/r#1": "operator=you",
		"pr:o/r#3": "dropped=no longer a candidate: hidden",
		"pr:o/r#4": "cap=cap",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("causes = %v, want %v", got, want)
	}
}

func TestDryRunEchoEqualsApplyEcho(t *testing.T) {
	section := func(out, heading string) string {
		i := strings.Index(out, heading+"\n")
		if i < 0 {
			t.Fatalf("no %s in:\n%s", heading, out)
		}
		return out[i:]
	}
	f := effectsScenario(t)
	dry, _, err := sel(t, effectsReply, "--dry-run")
	mustOK(t, err)
	applied, _, err := sel(t, effectsReply, "--apply")
	mustOK(t, err)
	if d, a := section(dry, "CHANGES"), section(applied, "CHANGES"); d != a {
		t.Errorf("the CHANGES block and the echo differ between a dry run and the apply:\n%s\n--- apply:\n%s", d, a)
	}
	if len(f.planKeys(t, showToday)) == 0 {
		t.Fatalf("the apply wrote no plan")
	}
}

func TestBarePreviewPrintsTheDraftAndChangesWithoutTheEcho(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.showDraft(t)
	out, _, err := sel(t, "ok")
	mustOK(t, err)
	mustContain(t, out, "DRAFT (stored)", "CHANGES", "will mint")
	if strings.Contains(out, "ECHO") {
		t.Errorf("a bare preview printed the echo table:\n%s", out)
	}
	if strings.Index(out, "DRAFT") > strings.Index(out, "CHANGES") {
		t.Errorf("the draft must come before the CHANGES block:\n%s", out)
	}
}

func TestOkOverReplanEchoesNoChange(t *testing.T) {
	f := newSelFixture(t)
	a := f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	f.lock(t, showToday, 6, a)
	out, _, err := sel(t, "ok", "--dry-run")
	mustOK(t, err)
	mustContain(t, out, "no change; name items with +key or use pull", "(none)")
	if strings.Contains(out, "will mint") {
		t.Errorf("a bare ok over a replan draft minted something:\n%s", out)
	}
}

func TestFirstLockWithNothingSelectedEchoesNoChange(t *testing.T) {
	newSelFixture(t)
	out, _, err := sel(t, "ok", "--dry-run")
	mustOK(t, err)
	mustContain(t, out, "no change")
	if strings.Contains(out, "name items with +key") {
		t.Errorf("a first lock must not give the replan advice:\n%s", out)
	}
}

func TestNewPeriodLockShowsTheRemovalsInChanges(t *testing.T) {
	f := newSelFixture(t)
	old := f.pr(t, 1, "old plan", "2026-10-08T08:00:00Z")
	f.pr(t, 2, "new", "2026-10-10T08:00:00Z")
	f.lock(t, "2026-10-09", 6, old)
	f.closePeriod(t, "2026-10-09")
	f.showDraft(t)
	out, _, err := sel(t, "ok", "--dry-run")
	mustOK(t, err)
	selectGolden(t, "TestNewPeriodLockShowsTheRemovalsInChanges.txt", out)
	mustContain(t, out, "remove  pr:o/r#1", "new plan", "select  pr:o/r#2")
	if strings.Contains(out, "was never closed") {
		t.Errorf("a closed period was reported as never closed:\n%s", out)
	}
	jout, _, err := sel(t, "ok", "--dry-run", "--json")
	mustOK(t, err)
	selectGolden(t, "TestNewPeriodLockShowsTheRemovalsInChanges.json", jout)
}

func TestChangesBlockNamesAnUnclosedPeriod(t *testing.T) {
	f := newSelFixture(t)
	old := f.pr(t, 1, "old plan", "2026-10-08T08:00:00Z")
	f.pr(t, 2, "new", "2026-10-10T08:00:00Z")
	f.lock(t, "2026-10-09", 6, old) // never closed
	f.showDraft(t)
	out, _, err := sel(t, "ok")
	mustOK(t, err)
	mustContain(t, out, "period 2026-10-09 was never closed; its rows are deleted by this lock")
	jout, _, err := sel(t, "ok", "--json")
	mustOK(t, err)
	var m map[string]any
	if err := json.Unmarshal([]byte(jout), &m); err != nil {
		t.Fatal(err)
	}
	if nc, _ := m["never_closed"].([]any); len(nc) != 1 || nc[0] != "2026-10-09" {
		t.Errorf("never_closed = %v", m["never_closed"])
	}
}

func TestSelectStderrLineMatchesRunRecord(t *testing.T) {
	f := effectsScenario(t)
	_, errOut, err := sel(t, effectsReply, "--apply")
	mustOK(t, err)
	var line string
	for _, l := range strings.Split(errOut, "\n") {
		if strings.HasPrefix(l, "{") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no structured stderr line in:\n%s", errOut)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("the stderr line is not JSON: %v\n%s", err, line)
	}
	r := f.lastRun(t)
	if got["run_id"] != r.RunID || got["verb"] != r.Verb || got["exit_code"] != float64(r.Exit.Int64) {
		t.Errorf("stderr line %v does not match the run record %+v", got, r)
	}
	for k, v := range r.Counts {
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("counts member %s: stderr %v, run record %v", k, got[k], v)
		}
	}
}

func TestSelectSkipsGoToStderrNotTheEcho(t *testing.T) {
	f := newSelFixture(t)
	a := f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.lock(t, showToday, 6, a)
	out, errOut, err := sel(t, "+1", "--dry-run")
	mustOK(t, err)
	mustContain(t, errOut, "skipped pr:o/r#1: already-selected")
	if strings.Contains(out, "already-selected") {
		t.Errorf("a routine skip is in the echo table:\n%s", out)
	}
}

func TestSelectTextAndJSONAgree(t *testing.T) {
	effectsScenario(t)
	out, _, err := sel(t, effectsReply, "--dry-run")
	mustOK(t, err)
	jout, _, err := sel(t, effectsReply, "--dry-run", "--json")
	mustOK(t, err)
	var m struct {
		Changes []struct{ Key, Title, Consequence, Direction string } `json:"changes"`
		Echo    []struct{ Key, Token, Title, Consequence string }     `json:"echo"`
	}
	if err := json.Unmarshal([]byte(jout), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Changes) == 0 || len(m.Echo) == 0 {
		t.Fatalf("empty changes or echo: %s", jout)
	}
	changes := out[strings.Index(out, "CHANGES\n"):strings.Index(out, "ECHO\n")]
	echo := out[strings.Index(out, "ECHO\n"):]
	for _, c := range m.Changes {
		for _, w := range []string{c.Key, c.Consequence} {
			if !strings.Contains(changes, w) {
				t.Errorf("CHANGES lacks %q of %+v:\n%s", w, c, changes)
			}
		}
		if c.Title != "" && !strings.Contains(changes, c.Title) {
			t.Errorf("CHANGES lacks the title %q", c.Title)
		}
	}
	for _, e := range m.Echo {
		for _, w := range []string{e.Token, e.Key, e.Consequence} {
			if w != "" && !strings.Contains(echo, w) {
				t.Errorf("echo lacks %q of %+v:\n%s", w, e, echo)
			}
		}
	}
	rows := func(s string) int { return strings.Count(strings.TrimSpace(s), "\n") }
	if got, want := rows(changes), len(m.Changes)+1; got != want {
		t.Errorf("CHANGES has %d lines after the heading, JSON %d changes (+ header row)", got, want-1)
	}
}

func TestSelectJSONGolden(t *testing.T) {
	f := newSelFixture(t)
	f.pr(t, 1, "alpha", "2026-10-09T08:00:00Z")
	f.pr(t, 2, "bravo", "2026-10-09T09:00:00Z")
	f.showDraft(t)
	out, _, err := sel(t, "ok", "--dry-run", "--json")
	mustOK(t, err)
	selectGolden(t, "TestSelectJSONGolden.json", out)
	// The write path carries run_id and the annotation outcomes.
	aout, _, err := sel(t, "ok", "--apply", "--json")
	mustOK(t, err)
	var m map[string]any
	if err := json.Unmarshal([]byte(aout), &m); err != nil {
		t.Fatal(err)
	}
	if m["run_id"] != f.lastRun(t).RunID || m["contract"] != "pg-desk.focus/v1" {
		t.Errorf("envelope = %v", m)
	}
	if _, ok := m["echo"].([]any); !ok {
		t.Errorf("no echo[] in %v", m)
	}
	var dry map[string]any
	if err := json.Unmarshal([]byte(out), &dry); err != nil {
		t.Fatal(err)
	}
	if _, has := dry["run_id"]; has {
		t.Errorf("a dry run wrote nothing yet carries run_id %v", dry["run_id"])
	}
}
