package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/shapes"
)

type labelFixture struct {
	db, in, out string
}

func bashShapeKey(cmd string) string {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return shapes.Normalize(asklog.DecisionRow{ToolName: "Bash", ToolInputJSON: string(b)}).Key()
}

// newLabelFixture builds a throwaway ask log (never the production one):
// rows 1-3 are shape "ls", rows 4-5 shape "echo hi", row 6 shape "pwd".
func newLabelFixture(t *testing.T) labelFixture {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	f := labelFixture{
		db:  filepath.Join(dir, "asks.db"),
		in:  filepath.Join(dir, "proposals-input.jsonl"),
		out: filepath.Join(dir, "proposals.jsonl"),
	}
	store, err := asklog.NewStore(f.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	cmds := map[int]string{1: "ls -la", 2: "ls -la", 3: "ls -la", 4: "echo hi", 5: "echo hi", 6: "pwd"}
	for id := 1; id <= 6; id++ {
		in, _ := json.Marshal(map[string]string{"command": cmds[id]})
		_, err := store.DB().Exec(`INSERT INTO tool_decisions
			(id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary,
			 hook_decision, outcome, created_at, permission_mode)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			id, "s", "/tmp", "Bash", fmt.Sprintf("h%d", id), string(in), "x", "allow", "approved", "2026-10-05T00:00:00Z", "default")
		if err != nil {
			t.Fatalf("seed row %d: %v", id, err)
		}
	}
	return f
}

func (f labelFixture) writeInput(t *testing.T, lines ...string) {
	t.Helper()
	if err := os.WriteFile(f.in, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func prop(shape, proposed, reason string) string {
	b, _ := json.Marshal(map[string]string{"shape_key": shape, "proposed": proposed, "reason": reason})
	return string(b)
}

func (f labelFixture) run(t *testing.T, dry, force bool) (string, string, error) {
	t.Helper()
	var so, se bytes.Buffer
	err := runLabelPropose(&so, &se, labelProposeOpts{
		since: "2026-10-02", db: f.db, in: f.in, out: f.out, dryRun: dry, force: force,
		src: sourceFlags{source: sourceCeta},
	})
	return so.String(), se.String(), err
}

type rowState struct {
	decision, explanation string
	excluded              int
}

func (f labelFixture) state(t *testing.T) map[int]rowState {
	t.Helper()
	store, err := asklog.NewReadOnlyStore(f.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	rs, err := store.DB().Query(`SELECT id, COALESCE(correct_hook_decision,''),
		COALESCE(correct_hook_decision_explanation,''), excluded FROM tool_decisions`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rs.Close() }()
	out := map[int]rowState{}
	for rs.Next() {
		var id int
		var s rowState
		if err := rs.Scan(&id, &s.decision, &s.explanation, &s.excluded); err != nil {
			t.Fatal(err)
		}
		out[id] = s
	}
	return out
}

func (f labelFixture) setLabel(t *testing.T, id int, decision, explanation string) {
	t.Helper()
	store, err := asklog.NewStore(f.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if err := store.SetCorrectDecision([]int{id}, decision, explanation); err != nil {
		t.Fatal(err)
	}
}

func (f labelFixture) manifest(t *testing.T) map[string]proposalEntry {
	t.Helper()
	es, err := readManifest(f.out)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]proposalEntry{}
	for _, e := range es {
		out[e.ShapeKey] = e
	}
	return out
}

func TestLabelProposeLabelsEveryRowOfShape(t *testing.T) {
	f := newLabelFixture(t)
	ls := bashShapeKey("ls -la")
	f.writeInput(t, prop(ls, "approve", "read-only listing"), prop(bashShapeKey("echo hi"), "reject", "because"))
	stdout, _, err := f.run(t, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "labeled shapes=2 rows=5") {
		t.Errorf("stdout = %q", stdout)
	}
	st := f.state(t)
	for _, id := range []int{1, 2, 3} {
		if st[id].decision != "allow" || st[id].explanation != "LLM-proposed: read-only listing" {
			t.Errorf("row %d = %+v", id, st[id])
		}
	}
	for _, id := range []int{4, 5} {
		if st[id].decision != "deny" || !strings.HasPrefix(st[id].explanation, llmProposedPrefix) {
			t.Errorf("row %d = %+v", id, st[id])
		}
	}
	if st[6].decision != "" || st[6].explanation != "" {
		t.Errorf("row 6 must be untouched: %+v", st[6])
	}
	m := f.manifest(t)
	if e := m[ls]; fmt.Sprint(e.RowIDsLabled) != "[1 2 3]" || e.Source != "ceta" || e.Proposed != "approve" {
		t.Errorf("manifest entry = %+v", e)
	}

	// A re-run skips the labeled shapes and keeps their manifest entries.
	stdout, _, err = f.run(t, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "labeled shapes=0 rows=0 skipped=2") {
		t.Errorf("re-run stdout = %q", stdout)
	}
	if e := f.manifest(t)[ls]; fmt.Sprint(e.RowIDsLabled) != "[1 2 3]" {
		t.Errorf("manifest changed by re-run: %+v", e)
	}
}

func TestLabelProposeForceRelabelsLLMButNeverOperatorReviewed(t *testing.T) {
	f := newLabelFixture(t)
	ls := bashShapeKey("ls -la")
	f.writeInput(t, prop(ls, "approve", "first"))
	if _, _, err := f.run(t, false, false); err != nil {
		t.Fatal(err)
	}
	// The operator reviews row 2 afterwards and a hand label lands on row 3.
	f.setLabel(t, 2, "deny", "operator-reviewed: not fine")
	f.setLabel(t, 3, "abstain", "set by hand")

	f.writeInput(t, prop(ls, "reject", "second"))
	if _, _, err := f.run(t, false, false); err != nil {
		t.Fatal(err)
	}
	if st := f.state(t); st[1].explanation != "LLM-proposed: first" {
		t.Errorf("without --force nothing may change: %+v", st[1])
	}

	if _, _, err := f.run(t, false, true); err != nil {
		t.Fatal(err)
	}
	st := f.state(t)
	if st[1].decision != "deny" || st[1].explanation != "LLM-proposed: second" {
		t.Errorf("row 1 should be relabeled: %+v", st[1])
	}
	if st[2].decision != "deny" || st[2].explanation != "operator-reviewed: not fine" {
		t.Errorf("operator-reviewed row overwritten: %+v", st[2])
	}
	if st[3].decision != "abstain" || st[3].explanation != "set by hand" {
		t.Errorf("hand label overwritten: %+v", st[3])
	}
	if e := f.manifest(t)[ls]; fmt.Sprint(e.RowIDsLabled) != "[1]" && fmt.Sprint(e.RowIDsLabled) != "[1 2 3]" {
		t.Errorf("manifest ids = %v", e.RowIDsLabled)
	} else if !strings.Contains(fmt.Sprint(e.RowIDsLabled), "1") {
		t.Errorf("manifest lost row 1: %v", e.RowIDsLabled)
	}
}

func TestLabelProposeOperatorReviewedShapeSkippedEvenWithForceOnItsRow(t *testing.T) {
	f := newLabelFixture(t)
	for _, id := range []int{4, 5} {
		f.setLabel(t, id, "allow", "operator-reviewed: ok")
	}
	f.writeInput(t, prop(bashShapeKey("echo hi"), "reject", "no"))
	for _, force := range []bool{false, true} {
		if _, _, err := f.run(t, false, force); err != nil {
			t.Fatal(err)
		}
		st := f.state(t)
		if st[4].decision != "allow" || st[5].explanation != "operator-reviewed: ok" {
			t.Fatalf("force=%v overwrote operator rows: %+v %+v", force, st[4], st[5])
		}
	}
}

func TestLabelProposeDryRunWritesOnlyManifest(t *testing.T) {
	f := newLabelFixture(t)
	ls := bashShapeKey("ls -la")
	f.writeInput(t, prop(ls, "approve", "x"), prop(bashShapeKey("pwd"), "exclude", "not a command"))
	stdout, _, err := f.run(t, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "would label shapes=2 rows=4") {
		t.Errorf("stdout = %q", stdout)
	}
	for id, s := range f.state(t) {
		if s.decision != "" || s.explanation != "" || s.excluded != 0 {
			t.Errorf("dry run wrote to row %d: %+v", id, s)
		}
	}
	if e := f.manifest(t)[ls]; fmt.Sprint(e.RowIDsLabled) != "[1 2 3]" {
		t.Errorf("dry-run manifest = %+v", e)
	}
}

func TestLabelProposeAskAndUnknownVerdictsRejected(t *testing.T) {
	f := newLabelFixture(t)
	f.writeInput(
		t,
		prop(bashShapeKey("ls -la"), "ask", "needs a human"),
		prop(bashShapeKey("echo hi"), "allow", "wrong vocabulary"),
		prop(bashShapeKey("pwd"), "approve", "fine"),
	)
	_, stderr, err := f.run(t, false, false)
	if err == nil {
		t.Fatal("expected a non-zero result")
	}
	if !strings.Contains(stderr, "line 1:") || !strings.Contains(stderr, "line 2:") {
		t.Errorf("stderr = %q", stderr)
	}
	st := f.state(t)
	for _, id := range []int{1, 2, 3, 4, 5} {
		if st[id].decision != "" || st[id].explanation != "" {
			t.Errorf("row %d written for a rejected line: %+v", id, st[id])
		}
	}
	if st[6].decision != "allow" {
		t.Errorf("the valid line must still be applied: %+v", st[6])
	}
	if _, ok := f.manifest(t)[bashShapeKey("ls -la")]; ok {
		t.Error("rejected line reached the manifest")
	}
}

func TestLabelProposeBadLinesAreReportedAndSkipped(t *testing.T) {
	f := newLabelFixture(t)
	f.writeInput(
		t,
		"{not json",
		`{"shape_key":"x","proposed":"approve"}`,
		prop("no-such-shape", "approve", "r"),
		prop(bashShapeKey("pwd"), "approve", "fine"),
	)
	_, stderr, err := f.run(t, false, false)
	if err == nil {
		t.Fatal("expected a non-zero result")
	}
	for _, want := range []string{"line 1: malformed JSON", "line 2: missing reason", "line 3:"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q: %q", want, stderr)
		}
	}
	if f.state(t)[6].decision != "allow" {
		t.Error("the valid line was not applied")
	}
}

func TestLabelProposeLaterLineForSameShapeWins(t *testing.T) {
	f := newLabelFixture(t)
	pwd := bashShapeKey("pwd")
	f.writeInput(t, prop(pwd, "approve", "first"), prop(pwd, "reject", "second"))
	if _, _, err := f.run(t, false, false); err != nil {
		t.Fatal(err)
	}
	if s := f.state(t)[6]; s.decision != "deny" || s.explanation != "LLM-proposed: second" {
		t.Errorf("row 6 = %+v", s)
	}
}

func TestLabelProposeExcludeMarksRowsAndIsIdempotent(t *testing.T) {
	f := newLabelFixture(t)
	pwd := bashShapeKey("pwd")
	f.writeInput(t, prop(pwd, "exclude", "not a real command"))
	if _, _, err := f.run(t, false, false); err != nil {
		t.Fatal(err)
	}
	s := f.state(t)[6]
	if s.excluded != 1 || strings.HasPrefix(s.explanation, llmProposedPrefix) || s.decision != "" {
		t.Errorf("row 6 = %+v", s)
	}
	if e := f.manifest(t)[pwd]; e.Proposed != "exclude" || fmt.Sprint(e.RowIDsLabled) != "[6]" {
		t.Errorf("manifest = %+v", e)
	}
	// QueryRows no longer returns the shape; the re-run is a clean skip.
	stdout, _, err := f.run(t, false, false)
	if err != nil {
		t.Fatalf("re-run must not fail: %v", err)
	}
	if !strings.Contains(stdout, "skipped=1") {
		t.Errorf("stdout = %q", stdout)
	}
	if e := f.manifest(t)[pwd]; fmt.Sprint(e.RowIDsLabled) != "[6]" {
		t.Errorf("manifest changed: %+v", e)
	}
}
