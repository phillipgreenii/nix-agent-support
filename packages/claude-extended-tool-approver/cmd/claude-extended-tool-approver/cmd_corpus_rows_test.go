package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/goldencorpus"
)

const (
	keyA = "tool=Bash|prog=ls|A"
	keyB = "tool=Bash|prog=ls|B"
	keyC = "tool=Bash|prog=ls|C"
	keyD = "tool=Bash|prog=ls|D"
	keyE = "tool=Bash|prog=curl|E"
	keyJ = "tool=Bash|prog=ls|J"
	keyK = "tool=Bash|prog=ls|K"
)

type corpusRowsFixture struct {
	db, replay, corpus, dir string
}

// newCorpusRowsFixture builds a throwaway ask log holding the representative
// rows of testdata/corpusrows/replay.jsonl and copies the fixture corpus.
func newCorpusRowsFixture(t *testing.T) corpusRowsFixture {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	db := filepath.Join(dir, "asks.db")
	store, err := asklog.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	type fx struct {
		id    int
		input string
		mode  string
	}
	for _, r := range []fx{
		{1, `{"command":"ls /a"}`, "acceptEdits"},
		{2, `{"command":"ls /b"}`, "plan"},
		{3, `{"command":"ls /c"}`, "weird"},
		{4, `{"command":"ls /d"}`, ""},
		{5, `{"command":"curl -H \"Authorization: Bearer abcdefghijklmnopqrstuvwxyz\" -u bob:hunter2 https://x"}`, "default"},
		{9, `{"command":"ls /i"}`, "default"},
		{10, `{"command":"ls /j"}`, "bypassPermissions"},
		{11, `{"command":"ls /k"}`, "default"},
	} {
		var mode any
		if r.mode != "" {
			mode = r.mode
		}
		_, err := store.DB().ExecContext(context.Background(), `INSERT INTO tool_decisions
			(id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary,
			 hook_decision, outcome, created_at, permission_mode)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			r.id, "s", "/work", "Bash", fmt.Sprintf("h%d", r.id), r.input, "x", "allow", "approved", "2026-10-05T00:00:00Z", mode)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	corpus := filepath.Join(dir, "corpus.json")
	data, err := os.ReadFile("testdata/corpusrows/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corpus, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return corpusRowsFixture{db: db, replay: "testdata/corpusrows/replay.jsonl", corpus: corpus, dir: dir}
}

func (f corpusRowsFixture) opts(families ...string) corpusRowsOpts {
	return corpusRowsOpts{
		packet: 99, families: families, replay: f.replay, db: f.db, corpus: f.corpus,
		src: sourceFlags{source: sourceCeta},
	}
}

func runRows(t *testing.T, o corpusRowsOpts) (stdout, stderr string, err error) {
	t.Helper()
	var so, se bytes.Buffer
	err = runCorpusRows(&so, &se, o)
	return so.String(), se.String(), err
}

func parseRows(t *testing.T, s string) []goldencorpus.Row {
	t.Helper()
	var rows []goldencorpus.Row
	if err := json.Unmarshal([]byte(s), &rows); err != nil {
		t.Fatalf("output is not a row array: %v\n%s", err, s)
	}
	return rows
}

func rowKeys(rows []goldencorpus.Row) []string {
	var out []string
	for _, r := range rows {
		for _, tg := range r.Tags {
			if k, ok := strings.CutPrefix(tg, "shape:"); ok {
				out = append(out, k)
			}
		}
	}
	return out
}

func exitCodeOf(err error) int {
	var ee exitCodeError
	if errors.As(err, &ee) {
		return ee.code
	}
	return cliErrorExitCode(err)
}

func TestCorpusRowsFamilySelectionAndOrder(t *testing.T) {
	f := newCorpusRowsFixture(t)
	out, _, err := runRows(t, f.opts("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	// A (5 rows), B stays out (new approve, old reject: needs judgment), D (3
	// rows), E (2 rows). I is claimed, G state-dependent, F/H do not disagree,
	// J is family beta.
	got := rowKeys(parseRows(t, out))
	want := []string{keyA, keyD, keyE}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("selected %v, want %v", got, want)
	}
}

func TestCorpusRowsShapesFromSelectsAcrossFamilies(t *testing.T) {
	f := newCorpusRowsFixture(t)
	list := filepath.Join(f.dir, "shapes.txt")
	if err := os.WriteFile(list, []byte("\n"+keyJ+"\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := f.opts()
	o.shapesFrom = list
	out, _, err := runRows(t, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := rowKeys(parseRows(t, out)); len(got) != 1 || got[0] != keyJ {
		t.Fatalf("selected %v, want [%s]", got, keyJ)
	}
}

func TestCorpusRowsSkipsAlreadyClaimedShape(t *testing.T) {
	f := newCorpusRowsFixture(t)
	out, _, err := runRows(t, f.opts("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range rowKeys(parseRows(t, out)) {
		if strings.HasSuffix(k, "|I") {
			t.Fatalf("claimed shape %s was emitted", k)
		}
	}
}

func TestCorpusRowsVerdictMappingAndConstruction(t *testing.T) {
	f := newCorpusRowsFixture(t)
	list := filepath.Join(f.dir, "shapes.txt")
	// J is old approve; A is old approve; D is old ask; E is unrecorded.
	if err := os.WriteFile(list, []byte(keyJ+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := f.opts("alpha")
	o.shapesFrom = list
	out, _, err := runRows(t, o)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]goldencorpus.Row{}
	for _, r := range parseRows(t, out) {
		byKey[rowKeys([]goldencorpus.Row{r})[0]] = r
	}
	wantVerdict := map[string]goldencorpus.Verdict{
		keyA: goldencorpus.Approve,    // old approve
		keyJ: goldencorpus.Approve,    // old approve
		keyD: goldencorpus.NotApprove, // old ask
		keyE: goldencorpus.NotApprove, // unrecorded
	}
	for k, v := range wantVerdict {
		if byKey[k].ExpectedVerdict != v {
			t.Errorf("%s: verdict %q, want %q", k, byKey[k].ExpectedVerdict, v)
		}
	}
	a := byKey[keyA]
	if a.Case != "spec99-"+shapeHash12(keyA) || len(shapeHash12(keyA)) != 12 {
		t.Errorf("case = %q", a.Case)
	}
	if a.Mode != goldencorpus.ModeAcceptEdits || a.CWDPathState.CWD != "/work" ||
		a.ToolInput.ToolName != "Bash" || a.ToolInput.ToolInput["command"] != "ls /a" ||
		a.Notes != "shape "+keyA {
		t.Errorf("row not built by the pinned rule: %+v", a)
	}
	if got := strings.Join(a.Tags, ","); got != "shape:"+keyA+",family:alpha,spec" {
		t.Errorf("tags = %q", got)
	}
	if byKey[keyD].Mode != goldencorpus.ModeDefault { // NULL mode
		t.Errorf("NULL mode mapped to %q", byKey[keyD].Mode)
	}
	if byKey[keyJ].Mode != goldencorpus.ModeBypassPermissions {
		t.Errorf("bypassPermissions mapped to %q", byKey[keyJ].Mode)
	}
}

func TestCorpusRowsRedactsCredentials(t *testing.T) {
	f := newCorpusRowsFixture(t)
	out, _, err := runRows(t, f.opts("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	var cmd string
	for _, r := range parseRows(t, out) {
		if rowKeys([]goldencorpus.Row{r})[0] == keyE {
			cmd, _ = r.ToolInput.ToolInput["command"].(string)
		}
	}
	want := `curl -H "Authorization: Bearer REDACTED" -u REDACTED https://x`
	if cmd != want {
		t.Fatalf("command = %q, want %q", cmd, want)
	}
}

func TestRedactString(t *testing.T) {
	for in, want := range map[string]string{
		"export API_KEY=abc123 && go":                 "export API_KEY=REDACTED && go",
		"password: hunter2":                           "password: REDACTED",
		`pw="s3cret" x`:                               `pw="s3cret" x`,
		`secret="s3cret" x`:                           `secret="REDACTED" x`,
		"Authorization: Bearer abcdefghijklmnopqrstu": "Authorization: Bearer REDACTED",
		"Authorization: abcdefghijklmnopqrstuvwxyz":   "Authorization: REDACTED",
		"curl -u me:pw url":                           "curl -u REDACTED url",
		"curl -u abcdefghijklmnopqrstuvwxyz url":      "curl -u REDACTED url",
		"git push -u origin main":                     "git push -u origin main",
		"sort -u file":                                "sort -u file",
		"ls -la":                                      "ls -la",
	} {
		if got := redactString(in); got != want {
			t.Errorf("redactString(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCorpusRowsNeedsJudgmentRouting(t *testing.T) {
	f := newCorpusRowsFixture(t)
	nj := filepath.Join(f.dir, "nj.jsonl")
	o := f.opts("alpha")
	o.needsJudgment = nj
	out, _, err := runRows(t, o)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range rowKeys(parseRows(t, out)) {
		if k == keyB || k == keyC || k == keyK {
			t.Fatalf("needs-judgment shape %s emitted as a row", k)
		}
	}
	entries, err := readJSONLines[needsJudgmentEntry](nj)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, e := range entries {
		keys = append(keys, e.ShapeKey)
		if e.Reason != needsJudgmentReason || e.NewDecision != "approve" || e.OldDecision == "approve" {
			t.Errorf("bad entry %+v", e)
		}
	}
	// Ordered by rows descending: B (4), C (3), K (1). F is unrecorded and is
	// never routed.
	if strings.Join(keys, ",") != strings.Join([]string{keyB, keyC, keyK}, ",") {
		t.Fatalf("needs-judgment = %v", keys)
	}
	if entries[1].ExampleRowID != 3 || entries[0].ExampleRowID != 2 {
		t.Errorf("example_row_id: %+v", entries)
	}
}

func TestCorpusRowsNeedsJudgmentDefaultsToStderr(t *testing.T) {
	f := newCorpusRowsFixture(t)
	_, errOut, err := runRows(t, f.opts("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, `"shape_key":"`+keyB+`"`) {
		t.Fatalf("stderr lacks needs-judgment lines: %q", errOut)
	}
}

func writeLabels(t *testing.T, dir string, lines ...string) string {
	t.Helper()
	p := filepath.Join(dir, "labels.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCorpusRowsLabelsEmitRowAndLeaveJudgmentList(t *testing.T) {
	f := newCorpusRowsFixture(t)
	nj := filepath.Join(f.dir, "nj.jsonl")
	o := f.opts("alpha")
	o.needsJudgment = nj
	o.labels = writeLabels(t, f.dir,
		`{"shape_key":"`+keyC+`","expected_verdict":"approve","explanation":"LLM-proposed: read-only listing"}`,
		`{"shape_key":"`+keyB+`","expected_verdict":"reject","explanation":"operator-reviewed: positive violation"}`)
	out, _, err := runRows(t, o)
	if err != nil {
		t.Fatal(err)
	}
	rows := parseRows(t, out)
	got := rowKeys(rows)
	if strings.Join(got, ",") != strings.Join([]string{keyA, keyB, keyC, keyD, keyE}, ",") {
		t.Fatalf("rows = %v", got)
	}
	for _, r := range rows {
		switch rowKeys([]goldencorpus.Row{r})[0] {
		case keyC:
			if r.ExpectedVerdict != goldencorpus.Approve || r.Notes != "shape "+keyC+"; LLM-proposed: read-only listing" {
				t.Errorf("C row: %+v", r)
			}
		case keyB:
			if r.ExpectedVerdict != goldencorpus.Reject || !strings.HasPrefix(r.Notes, "shape "+keyB+"; operator-reviewed:") {
				t.Errorf("B row: %+v", r)
			}
		}
	}
	entries, _ := readJSONLines[needsJudgmentEntry](nj)
	if len(entries) != 1 || entries[0].ShapeKey != keyK {
		t.Fatalf("remaining needs-judgment = %+v", entries)
	}
}

func TestCorpusRowsRejectsBadLabelsWritingNothing(t *testing.T) {
	good := `"explanation":"LLM-proposed: x"`
	for name, line := range map[string]string{
		"not needs-judgment":   `{"shape_key":"` + keyA + `","expected_verdict":"approve",` + good + `}`,
		"unknown key":          `{"shape_key":"nope","expected_verdict":"approve",` + good + `}`,
		"state dependent":      `{"shape_key":"tool=Bash|prog=ls|G","expected_verdict":"approve",` + good + `}`,
		"already claimed":      `{"shape_key":"tool=Bash|prog=ls|I","expected_verdict":"approve",` + good + `}`,
		"missing prefix":       `{"shape_key":"` + keyC + `","expected_verdict":"approve","explanation":"because"}`,
		"invalid verdict":      `{"shape_key":"` + keyC + `","expected_verdict":"allow",` + good + `}`,
		"outside the selected": `{"shape_key":"` + keyJ + `","expected_verdict":"approve",` + good + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newCorpusRowsFixture(t)
			before, _ := os.ReadFile(f.corpus)
			out := filepath.Join(f.dir, "out.json")
			o := f.opts("alpha")
			o.out = out
			o.appendRows = true
			o.labels = writeLabels(t, f.dir, line)
			_, _, err := runRows(t, o)
			if err == nil || exitCodeOf(err) != 2 {
				t.Fatalf("err = %v (exit %d), want exit 2", err, exitCodeOf(err))
			}
			after, _ := os.ReadFile(f.corpus)
			if !bytes.Equal(before, after) {
				t.Fatal("corpus written despite the error")
			}
			if _, statErr := os.Stat(out); statErr == nil {
				t.Fatal("--out written despite the error")
			}
		})
	}
}

func TestCorpusRowsUnknownSelectorsExit2NamingIt(t *testing.T) {
	f := newCorpusRowsFixture(t)
	var se bytes.Buffer
	err := runCorpusRows(&bytes.Buffer{}, &se, f.opts("nosuchfamily"))
	if exitCodeOf(err) != 2 || !strings.Contains(se.String(), "nosuchfamily") {
		t.Fatalf("family: exit %d stderr %q", exitCodeOf(err), se.String())
	}
	list := filepath.Join(f.dir, "s.txt")
	_ = os.WriteFile(list, []byte("tool=Bash|prog=missing\n"), 0o644)
	o := f.opts()
	o.shapesFrom = list
	se.Reset()
	err = runCorpusRows(&bytes.Buffer{}, &se, o)
	if exitCodeOf(err) != 2 || !strings.Contains(se.String(), "tool=Bash|prog=missing") {
		t.Fatalf("shapes-from: exit %d stderr %q", exitCodeOf(err), se.String())
	}
}

func TestCorpusRowsUnreadableInputsExit1(t *testing.T) {
	f := newCorpusRowsFixture(t)
	o := f.opts("alpha")
	o.replay = filepath.Join(f.dir, "missing.jsonl")
	if _, _, err := runRows(t, o); err == nil || exitCodeOf(err) != 1 {
		t.Fatalf("replay: err %v exit %d", err, exitCodeOf(err))
	}
	o = f.opts("alpha")
	o.corpus = filepath.Join(f.dir, "missing.json")
	if _, _, err := runRows(t, o); err == nil || exitCodeOf(err) != 1 {
		t.Fatalf("corpus: err %v exit %d", err, exitCodeOf(err))
	}
}

func TestCorpusRowsDeterministic(t *testing.T) {
	f := newCorpusRowsFixture(t)
	a, _, err := runRows(t, f.opts("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := runRows(t, f.opts("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if a != b || a == "" {
		t.Fatal("two runs over the same inputs differ")
	}
}

func TestCorpusRowsAppendKeepsExistingBytes(t *testing.T) {
	f := newCorpusRowsFixture(t)
	before, _ := os.ReadFile(f.corpus)
	o := f.opts("alpha")
	o.appendRows = true
	out, _, err := runRows(t, o)
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Fatalf("--append wrote to stdout: %q", out)
	}
	after, _ := os.ReadFile(f.corpus)
	end := bytes.LastIndexByte(before, ']')
	body := bytes.TrimRight(before[:end], " \t\r\n")
	if !bytes.HasPrefix(after, body) {
		t.Fatal("existing bytes changed")
	}
	var rows []goldencorpus.Row
	if err := json.Unmarshal(after, &rows); err != nil {
		t.Fatalf("result is not valid: %v", err)
	}
	if len(rows) != 2+3 {
		t.Fatalf("rows = %d, want 5", len(rows))
	}
	// A second run finds every shape claimed: nothing more is added, and the
	// file is byte-identical.
	_, _, err = runRows(t, o)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(f.corpus)
	if !bytes.Equal(after, again) {
		t.Fatal("second --append changed the corpus")
	}
}

func TestCorpusRowsAppendWithNoRowsIsByteIdentical(t *testing.T) {
	f := newCorpusRowsFixture(t)
	before, _ := os.ReadFile(f.corpus)
	list := filepath.Join(f.dir, "s.txt")
	_ = os.WriteFile(list, []byte("tool=Bash|prog=ls|H\n"), 0o644) // agrees, so nothing selected
	o := f.opts()
	o.shapesFrom = list
	o.appendRows = true
	if _, _, err := runRows(t, o); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(f.corpus)
	if !bytes.Equal(before, after) {
		t.Fatal("corpus changed with zero proposed rows")
	}
}

func TestCorpusRowsAppendIgnoresOut(t *testing.T) {
	f := newCorpusRowsFixture(t)
	out := filepath.Join(f.dir, "out.json")
	o := f.opts("alpha")
	o.appendRows = true
	o.out = out
	if _, _, err := runRows(t, o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("--append wrote --out")
	}
}

func TestCorpusRowsRequiresPacketAndSelector(t *testing.T) {
	f := newCorpusRowsFixture(t)
	o := f.opts("alpha")
	o.packet = 0
	if _, _, err := runRows(t, o); exitCodeOf(err) != 2 {
		t.Fatalf("packet: %v", err)
	}
	if _, _, err := runRows(t, f.opts()); exitCodeOf(err) != 2 {
		t.Fatalf("selector: %v", err)
	}
}
