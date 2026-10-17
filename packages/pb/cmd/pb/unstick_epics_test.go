package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pb/internal/run"
	"github.com/phillipgreenii/pb/internal/unstick"
)

// epicFixture builds a synthetic export plus its bd ready list for the
// finished-epic candidate plumbing. Layout:
//
//	t-1 ready task; t-2 blocked on t-1 (LIVE)         -> the targets partition is non-trivial
//	e-01..e-07 epics, every child closed              -> dispatched candidates (e-07 is human-labelled)
//	e-08 epic with a valid sweep marker               -> candidate, marker-skipped
//	x-01 open child, x-02 no children, x-03 assigned,
//	x-04 focus-item, x-05 template, x-06 in_progress  -> NOT candidates
//	x-07 child epic with an open child, x-08 its parent -> neither is a candidate (a parent whose
//	                                                        child epic is still open waits for the next sweep)
func epicFixture() (export, ready string) { return epicFixtureWith(nil) }

// epicFixtureWith is epicFixture with per-id field overrides applied to the
// export row (used to build the post-sweep export).
func epicFixtureWith(over map[string]map[string]any) (export, ready string) {
	var ex, rd []string
	add := func(row map[string]any, inReady bool) {
		for k, v := range over[row["id"].(string)] {
			row[k] = v
		}
		tpl, isTpl := row["_template"]
		delete(row, "_template")
		b, _ := json.Marshal(row)
		ex = append(ex, string(b))
		if inReady {
			r := map[string]any{"id": row["id"], "status": row["status"], "issue_type": row["issue_type"]}
			if l, ok := row["labels"]; ok {
				r["labels"] = l
			}
			if a, ok := row["assignee"]; ok {
				r["assignee"] = a
			}
			if isTpl {
				r["is_template"] = tpl
			}
			rd = append(rd, mustJSON(r))
		}
	}
	task := func(id, status string, extra map[string]any) map[string]any {
		r := map[string]any{
			"_type": "issue", "id": id, "title": id, "status": status, "priority": 2, "issue_type": "task",
			"created_at": "2026-10-01T00:00:00Z", "updated_at": "2026-10-10T09:00:00Z",
		}
		for k, v := range extra {
			r[k] = v
		}
		return r
	}
	epic := func(id string, extra map[string]any) map[string]any {
		return task(id, "open", mergeMap(map[string]any{"issue_type": "epic"}, extra))
	}
	kid := func(id, parent string) map[string]any {
		return task(id, "closed", map[string]any{
			"closed_at": "2026-10-02T00:00:00Z", "close_reason": "shipped " + id,
			"dependencies": []any{map[string]any{"issue_id": id, "depends_on_id": parent, "type": "parent-child"}},
		})
	}
	add(task("t-1", "open", nil), true)
	add(task("t-2", "blocked", map[string]any{"dependencies": []any{map[string]any{"issue_id": "t-2", "depends_on_id": "t-1", "type": "blocks"}}}), false)
	for i := 1; i <= 7; i++ {
		id := fmt.Sprintf("e-%02d", i)
		extra := map[string]any{}
		if i == 7 {
			extra["labels"] = []string{"human"}
		}
		add(epic(id, extra), true)
		add(kid(id+".1", id), false)
		if i == 1 {
			add(kid(id+".2", id), false)
		}
	}
	add(epic("e-08", map[string]any{
		"notes":      "[unstick 2026-10-10T10:00:00Z] unchanged: scope not done; recheck-when: 2027-01-01",
		"updated_at": "2026-10-10T10:05:00Z",
	}), true)
	add(kid("e-08.1", "e-08"), false)
	// Not candidates.
	add(epic("x-01", nil), true)
	add(task("x-01.1", "open", map[string]any{"dependencies": []any{map[string]any{"issue_id": "x-01.1", "depends_on_id": "x-01", "type": "parent-child"}}}), true)
	add(epic("x-02", nil), true)
	add(epic("x-03", map[string]any{"assignee": "worker-9"}), true)
	add(kid("x-03.1", "x-03"), false)
	add(epic("x-04", map[string]any{"labels": []string{"focus-item"}}), true)
	add(kid("x-04.1", "x-04"), false)
	add(epic("x-05", map[string]any{"_template": true}), true)
	add(kid("x-05.1", "x-05"), false)
	add(epic("x-06", map[string]any{"status": "in_progress", "assignee": "worker-9"}), false)
	add(kid("x-06.1", "x-06"), false)
	// Nested: x-08's only child is the epic x-07, which still has an open child.
	add(epic("x-08", nil), true)
	add(epic("x-07", map[string]any{"dependencies": []any{map[string]any{"issue_id": "x-07", "depends_on_id": "x-08", "type": "parent-child"}}}), true)
	add(task("x-07.1", "open", map[string]any{"dependencies": []any{map[string]any{"issue_id": "x-07.1", "depends_on_id": "x-07", "type": "parent-child"}}}), true)
	return strings.Join(ex, "\n") + "\n", `{"data":[` + strings.Join(rd, ",") + `],"schema_version":1}`
}

func mergeMap(a, b map[string]any) map[string]any {
	for k, v := range b {
		a[k] = v
	}
	return a
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func epicPrepareEnv(t *testing.T) (unstickEnv, *run.FakeRunner, string) {
	t.Helper()
	exp, rdy := epicFixture()
	w := filepath.Join(t.TempDir(), "w")
	f := run.NewFakeRunner()
	bdScript(f, w, rdy)
	return testEnv(&exportRunner{FakeRunner: f, content: exp}), f, w
}

var epicDispatched = []string{"e-01", "e-02", "e-03", "e-04", "e-05", "e-06", "e-07"}

func TestPrepareEpics_candidatesBatchesAndPartition(t *testing.T) {
	env, f, w := epicPrepareEnv(t)
	out, _, err := execUnstick(env, "", "prepare", "--root", unstickRoot, "--workdir", w, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(f.Calls()); n != 2 {
		t.Errorf("epic candidates must need no extra bd calls: got %d calls, want 2 (export, ready)", n)
	}
	var sum prepareSummary
	if err := json.Unmarshal([]byte(out), &sum); err != nil {
		t.Fatal(err)
	}
	all := append(append([]string(nil), epicDispatched...), "e-08")
	if !reflect.DeepEqual(sum.EpicCandidates, all) || !reflect.DeepEqual(sum.EpicMarkerSkip, []string{"e-08"}) {
		t.Errorf("epic_candidates = %v, epic_marker_skip = %v", sum.EpicCandidates, sum.EpicMarkerSkip)
	}
	if sum.Counts.EpicCandidates != 8 || sum.Counts.EpicMarkerSkip != 1 {
		t.Errorf("counts = %+v", sum.Counts)
	}
	// The partition line is the one the REVIEW sweep always had.
	if sum.Partition != "targets 1 = live 1 + marker 0 + review 0" {
		t.Errorf("partition = %q", sum.Partition)
	}
	if sum.Counts.Targets != 1 || sum.Counts.LiveSkip != 1 || sum.Counts.Review != 0 {
		t.Errorf("target counts = %+v", sum.Counts)
	}

	// E batches: 7 dispatched epics -> E01 (4) + E02 (3), no REVIEW batches.
	wantBatches := []batchSummary{{Name: "E01", Kind: "epic", Size: 4}, {Name: "E02", Kind: "epic", Size: 3}}
	if !reflect.DeepEqual(sum.Batches, wantBatches) {
		t.Errorf("batches = %+v, want %+v", sum.Batches, wantBatches)
	}
	e1 := readFile(t, filepath.Join(w, "batches", "E01"))
	e2 := readFile(t, filepath.Join(w, "batches", "E02"))
	if e1 != "e-01\ne-02\ne-03\ne-04\n" || e2 != "e-05\ne-06\ne-07\n" {
		t.Errorf("E01 = %q E02 = %q", e1, e2)
	}
	var seen []string
	for _, name := range []string{"E01", "E02"} {
		ids := strings.Fields(readFile(t, filepath.Join(w, "batches", name)))
		if len(ids) > unstick.EpicMaxBatch {
			t.Errorf("%s holds %d epics, max %d", name, len(ids), unstick.EpicMaxBatch)
		}
		seen = append(seen, ids...)
		if _, err := os.Stat(filepath.Join(w, "facts", name+".json")); err != nil {
			t.Errorf("missing facts/%s.json: %v", name, err)
		}
	}
	if !reflect.DeepEqual(seen, epicDispatched) {
		t.Errorf("batched epics = %v, want %v (marker-skipped e-08 must not be batched)", seen, epicDispatched)
	}
	if _, err := os.Stat(filepath.Join(w, "batches", "B01")); err == nil {
		t.Error("an empty REVIEW set must write no B batch")
	}

	if got := readFile(t, filepath.Join(w, "triage-epic-candidates.txt")); got != strings.Join(all, "\n")+"\n" {
		t.Errorf("triage-epic-candidates.txt = %q", got)
	}
	if got := readFile(t, filepath.Join(w, "triage-targets.txt")); got != "t-2\n" {
		t.Errorf("epic candidates must not be targets: triage-targets.txt = %q", got)
	}
	if !containsStr(sum.Written, "triage-epic-candidates.txt") || !containsStr(sum.Written, "batches/E01") || !containsStr(sum.Written, "facts/E02.json") {
		t.Errorf("written = %v", sum.Written)
	}

	// Facts of an E batch show each child's close_reason.
	facts := readFile(t, filepath.Join(w, "facts", "E01.json"))
	if !strings.Contains(facts, `"close_reason": "shipped e-01.2"`) {
		t.Errorf("facts/E01.json lacks the child close_reason:\n%.600s", facts)
	}

	// prepare.json records the candidates; the Review list stays REVIEW-only.
	st, err := unstick.ReadPrepare(filepath.Join(w, "prepare.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st.EpicCandidates, all) || !reflect.DeepEqual(st.EpicMarkerSkip, []string{"e-08"}) || len(st.Review) != 0 {
		t.Errorf("prepare.json = %+v", st)
	}
	if st.Counts["epic_candidates"] != 8 || st.Counts["epic_marker_skip"] != 1 || st.Counts["targets"] != 1 {
		t.Errorf("prepare.json counts = %v", st.Counts)
	}
}

func TestPrepareEpics_workerPoolSharedWithReviewBatches(t *testing.T) {
	// REVIEW beads and epic candidates together: B and E batches are listed
	// in one batches array (the orchestrator's pool of at most 4 workers is
	// filled from it), B first.
	exp, rdy := epicFixture()
	exp += `{"_type":"issue","id":"r-1","title":"stuck","status":"deferred","priority":2,"issue_type":"task","defer_until":"2026-01-01T00:00:00Z","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-02-01T00:00:00Z"}` + "\n"
	w := filepath.Join(t.TempDir(), "w")
	f := run.NewFakeRunner()
	bdScript(f, w, rdy)
	env := testEnv(&exportRunner{FakeRunner: f, content: exp})
	out, _, err := execUnstick(env, "", "prepare", "--root", unstickRoot, "--workdir", w, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var sum prepareSummary
	if err := json.Unmarshal([]byte(out), &sum); err != nil {
		t.Fatal(err)
	}
	want := []batchSummary{{"B01", "review", 1}, {"E01", "epic", 4}, {"E02", "epic", 3}}
	if !reflect.DeepEqual(sum.Batches, want) {
		t.Errorf("batches = %+v, want %+v", sum.Batches, want)
	}
	if sum.Partition != "targets 2 = live 1 + marker 0 + review 1" {
		t.Errorf("partition = %q", sum.Partition)
	}
	if got := readFile(t, filepath.Join(w, "batches", "B01")); got != "r-1\n" {
		t.Errorf("B01 = %q (epics must never join a REVIEW batch)", got)
	}
}

func TestPrepareEpics_narrowingAndFull(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantCands  []string
		wantSkip   []string
		wantBatchs int
	}{
		{"--id-prefix narrows candidates", []string{"--id-prefix", "e-0"}, append(append([]string(nil), epicDispatched...), "e-08"), []string{"e-08"}, 2},
		{"--id-prefix to one epic", []string{"--id-prefix", "e-03"}, []string{"e-03"}, nil, 1},
		{"--label narrows candidates", []string{"--label", "human"}, []string{"e-07"}, nil, 1},
		{"--full ignores markers", []string{"--full"}, append(append([]string(nil), epicDispatched...), "e-08"), nil, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env, _, w := epicPrepareEnv(t)
			args := append([]string{"prepare", "--root", unstickRoot, "--workdir", w, "--json"}, c.args...)
			out, _, err := execUnstick(env, "", args...)
			if err != nil {
				t.Fatal(err)
			}
			var sum prepareSummary
			if err := json.Unmarshal([]byte(out), &sum); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(sum.EpicCandidates, c.wantCands) {
				t.Errorf("epic_candidates = %v, want %v", sum.EpicCandidates, c.wantCands)
			}
			if len(sum.EpicMarkerSkip) != len(c.wantSkip) || (len(c.wantSkip) > 0 && !reflect.DeepEqual(sum.EpicMarkerSkip, c.wantSkip)) {
				t.Errorf("epic_marker_skip = %v, want %v", sum.EpicMarkerSkip, c.wantSkip)
			}
			if len(sum.Batches) != c.wantBatchs {
				t.Errorf("batches = %+v, want %d", sum.Batches, c.wantBatchs)
			}
		})
	}
}

func TestPrepareEpics_summaryGoldens(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		golden string
	}{
		{"json", []string{"--json"}, "prepare.epics.summary.golden.txt"},
		{"human", nil, "prepare.epics.human.golden.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _, w := epicPrepareEnv(t)
			args := append([]string{"prepare", "--root", unstickRoot, "--workdir", w}, tc.args...)
			out, _, err := execUnstick(env, "", args...)
			if err != nil {
				t.Fatal(err)
			}
			checkCmdGolden(t, tc.golden, []byte(normalizeWorkdir(out, w)))
		})
	}
}

func TestCheckEpicBatches(t *testing.T) {
	if err := checkEpicBatches([]string{"a", "b", "c"}, [][]string{{"a", "b", "c"}}); err != nil {
		t.Errorf("valid batches rejected: %v", err)
	}
	if err := checkEpicBatches(nil, nil); err != nil {
		t.Errorf("no epics must be fine: %v", err)
	}
	six := []string{"a", "b", "c", "d", "e", "f"}
	for name, c := range map[string]struct {
		epics   []string
		batches [][]string
	}{
		"missing":   {[]string{"a", "b"}, [][]string{{"a"}}},
		"duplicate": {[]string{"a", "b"}, [][]string{{"a", "b"}, {"b"}}},
		"extra":     {[]string{"a"}, [][]string{{"a", "z"}}},
		"oversize":  {six, [][]string{six}},
	} {
		if err := checkEpicBatches(c.epics, c.batches); err == nil {
			t.Errorf("%s: violation not detected", name)
		}
	}
}

// TestReportEpics_arithmeticWithClosedEpic runs prepare then report over the epic fixture, with the
// post-sweep export built from per-id overrides.
func TestReportEpics_arithmeticWithClosedEpic(t *testing.T) {
	env, _, w := epicPrepareEnv(t)
	if _, _, err := execUnstick(env, "", "prepare", "--root", unstickRoot, "--workdir", w); err != nil {
		t.Fatal(err)
	}
	// E01 was reviewed: e-01 closed by the worker, e-02 unchanged, e-03
	// needs-children; e-04 left alone (unresolved); e-05 closed by a peer.
	pre, _ := epicFixture()
	post, rdy := epicFixtureWith(map[string]map[string]any{
		"e-01": {"status": "closed", "closed_at": "2026-10-10T12:30:00Z", "close_reason": "epic complete"},
		"e-05": {"status": "closed", "closed_at": "2026-10-10T12:31:00Z", "close_reason": "peer"},
		"e-02": {"notes": "[unstick 2026-10-10T12:20:00Z] unchanged: scope remains; recheck-when: on-change"},
		"e-03": {"notes": "[unstick 2026-10-10T12:21:00Z] needs-children: scope lists unbuilt work; recheck-when: on-change"},
	})
	if post == pre {
		t.Fatal("test bug: post export identical to pre export")
	}
	if err := os.WriteFile(filepath.Join(w, "results", "E01.md"), []byte("closed e-01: epic complete\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", unstickRoot, "export", "-o", filepath.Join(w, unstick.ExportPostFile)}, run.Result{}, nil)
	f.AddResponse("bd", []string{"-C", unstickRoot, "ready", "-n", "0", "--json"}, run.Result{Stdout: rdy}, nil)
	renv := testEnv(&exportRunner{FakeRunner: f, content: post})
	out, _, err := execUnstick(renv, "", "report", "--workdir", w, "--root", unstickRoot, "--json", "--now", reportNow)
	if err != nil {
		t.Fatal(err)
	}
	var rep unstick.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	ep := rep.Epics
	want := "epic candidates 8 = closed 2 + unchanged 1 + needs-children 1 + marker-skipped 1 + unresolved 3"
	if ep.Arithmetic != want {
		t.Errorf("arithmetic = %q, want %q", ep.Arithmetic, want)
	}
	attr := map[string]string{}
	for _, c := range ep.Closed {
		attr[c.ID] = c.Attribution
	}
	if len(attr) != 2 || attr["e-01"] != unstick.AttribSweep || attr["e-05"] != unstick.AttribPeer {
		t.Errorf("closed epics = %+v", ep.Closed)
	}
	if !reflect.DeepEqual(ep.NeedsChildren, []string{"e-03"}) || !reflect.DeepEqual(ep.Unresolved, []string{"e-04", "e-06", "e-07"}) {
		t.Errorf("needs-children = %v unresolved = %v", ep.NeedsChildren, ep.Unresolved)
	}
	hout, _, err := execUnstick(postReportEnv(t, w, post, rdy), "", "report", "--workdir", w, "--root", unstickRoot, "--now", reportNow)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hout, want) {
		t.Errorf("human report lacks the epic arithmetic:\n%s", hout)
	}
}

func postReportEnv(t *testing.T, w, post, rdy string) unstickEnv {
	t.Helper()
	f := run.NewFakeRunner()
	f.AddResponse("bd", []string{"-C", unstickRoot, "export", "-o", filepath.Join(w, unstick.ExportPostFile)}, run.Result{}, nil)
	f.AddResponse("bd", []string{"-C", unstickRoot, "ready", "-n", "0", "--json"}, run.Result{Stdout: rdy}, nil)
	return testEnv(&exportRunner{FakeRunner: f, content: post})
}

func TestReportEpics_oldPrepareJSONWithoutEpicFields(t *testing.T) {
	// A prepare.json written before epic candidates existed has no epic keys:
	// report must still run and show a zero epic line.
	w := preparedWorkdir(t)
	p := filepath.Join(w, "prepare.json")
	var m map[string]any
	if err := json.Unmarshal([]byte(readFile(t, p)), &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "epic_candidates")
	delete(m, "epic_marker_skip")
	if c, ok := m["counts"].(map[string]any); ok {
		delete(c, "epic_candidates")
		delete(c, "epic_marker_skip")
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFile(t, p), "epic_") {
		t.Fatal("test bug: epic keys still present")
	}
	env, _ := reportEnv(w, unstickPostReady, nil)
	out, _, err := execUnstick(env, "", "report", "--workdir", w, "--root", unstickRoot, "--json", "--now", reportNow)
	if err != nil {
		t.Fatalf("old prepare.json must still load: %v", err)
	}
	var rep unstick.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Epics.Candidates != 0 || rep.Epics.Arithmetic != "epic candidates 0 = closed 0 + unchanged 0 + needs-children 0 + marker-skipped 0" {
		t.Errorf("epics = %+v", rep.Epics)
	}
}
