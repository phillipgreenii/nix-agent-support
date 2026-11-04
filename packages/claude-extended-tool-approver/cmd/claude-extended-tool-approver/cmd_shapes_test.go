package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
)

func TestShapeCollapseLine(t *testing.T) {
	for _, tc := range []struct {
		rows, shapes int
		want         string
	}{
		{0, 0, "rows=0 shapes=0 collapse_ratio=0/0=n/a baseline=77114/84581"},
		{4, 3, "rows=4 shapes=3 collapse_ratio=3/4=0.7500 baseline=77114/84581"},
		{3, 1, "rows=3 shapes=1 collapse_ratio=1/3=0.3333 baseline=77114/84581"},
		{3, 2, "rows=3 shapes=2 collapse_ratio=2/3=0.6667 baseline=77114/84581"}, // 0.66666 rounds up
		{16, 1, "rows=16 shapes=1 collapse_ratio=1/16=0.0625 baseline=77114/84581"},
		{32, 1, "rows=32 shapes=1 collapse_ratio=1/32=0.0313 baseline=77114/84581"}, // 0.03125 is a half: rounds up
		{5, 5, "rows=5 shapes=5 collapse_ratio=5/5=1.0000 baseline=77114/84581"},
	} {
		if got := collapseLine(tc.rows, tc.shapes); got != tc.want {
			t.Errorf("collapseLine(%d,%d) = %q, want %q", tc.rows, tc.shapes, got, tc.want)
		}
	}
}

func shapeTestRow(id int, tool, input string) asklog.DecisionRow {
	return asklog.DecisionRow{ID: id, ToolName: tool, ToolInputJSON: input, CWD: "/work/repo"}
}

func TestShapeTableCountsEveryRowAndSortsByRowsThenKey(t *testing.T) {
	rows := []asklog.DecisionRow{
		shapeTestRow(9, "Bash", `{"command":"rm -rf ./a"}`),
		shapeTestRow(3, "Bash", `{"command":"rm -rf ./b"}`),
		shapeTestRow(1, "Bash", `{"command":"rm -rf ./c"}`),
		shapeTestRow(2, "Bash", `{"command":"rm -rf ./d"}`),
		shapeTestRow(4, "Bash", `{"command":"rm -rf ./e"}`),
		shapeTestRow(5, "Bash", `{"command":"rm -rf ./f"}`),
		shapeTestRow(6, "Bash", `{"command":"ls x"}`),
		shapeTestRow(7, "Monitor", `{"command":"tail -f x"}`),
		shapeTestRow(8, "Skill", `{"skill":"s1"}`),
		shapeTestRow(10, "Skill", `{"skill":"s2"}`),
	}
	table := buildShapeTable(rows)
	total := 0
	for _, e := range table {
		total += e.Rows
	}
	if total != len(rows) {
		t.Fatalf("shape rows sum to %d, want %d: no row (non-shell tools included) may be dropped", total, len(rows))
	}
	if len(table) != 5 {
		t.Fatalf("got %d shapes, want 5 (rm, ls, Monitor, Skill s1, Skill s2): %+v", len(table), table)
	}
	top := table[0]
	if top.Rows != 6 || top.Program != "rm" || top.Family != "rm" {
		t.Fatalf("top shape = %+v", top)
	}
	if want := []int{1, 2, 3, 4, 5}; !reflect.DeepEqual(top.ExampleRowIDs, want) {
		t.Fatalf("example_row_ids = %v, want the five lowest ids %v", top.ExampleRowIDs, want)
	}
	for i := 1; i < len(table); i++ {
		a, b := table[i-1], table[i]
		if a.Rows < b.Rows || (a.Rows == b.Rows && a.ShapeKey >= b.ShapeKey) {
			t.Fatalf("not sorted by rows desc then key asc at %d: %q(%d) before %q(%d)", i, a.ShapeKey, a.Rows, b.ShapeKey, b.Rows)
		}
	}
	var sawMonitor bool
	for _, e := range table {
		if e.Tool == "Monitor" {
			sawMonitor = true
			if e.Family != "Monitor" || e.Program != "" || len(e.Programs) != 0 {
				t.Fatalf("Monitor entry = %+v", e)
			}
		}
	}
	if !sawMonitor {
		t.Fatal("Monitor row dropped")
	}
}

func TestShapeJSONLCarriesEveryPinnedFieldAndEmptyArraysNotNull(t *testing.T) {
	table := buildShapeTable([]asklog.DecisionRow{shapeTestRow(1, "Monitor", `{}`)})
	var buf bytes.Buffer
	if err := writeShapesJSONL(&buf, table); err != nil {
		t.Fatal(err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &obj); err != nil {
		t.Fatalf("line is not one JSON object: %v: %s", err, buf.String())
	}
	for _, k := range []string{"shape_key", "tool", "program", "subcommand", "flags", "path_classes", "env_names", "programs", "family", "rows", "example_row_ids"} {
		if _, ok := obj[k]; !ok {
			t.Errorf("missing field %q in %s", k, buf.String())
		}
	}
	for _, k := range []string{"flags", "path_classes", "env_names", "programs"} {
		if string(obj[k]) != "[]" {
			t.Errorf("%s = %s, want []", k, obj[k])
		}
	}
}

// TestShapeCommandWritesArtifactAndNeverTouchesTheAskLog drives the command
// in process over a real fixture ask log, with XDG_STATE_HOME pointing at a
// temp directory so ART never resolves to the real one.
func TestShapeCommandWritesArtifactAndNeverTouchesTheAskLog(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)

	db := filepath.Join(t.TempDir(), "asks.db")
	store, err := asklog.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.DB().Exec(`INSERT INTO tool_decisions
		(id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary,
		 hook_decision, outcome, created_at)
		VALUES
		 (1,'s','/work/repo','Bash','h1','{"command":"rm -rf ./a"}','x','allow','approved','2026-10-05T00:00:00Z'),
		 (2,'s','/work/repo','Bash','h2','{"command":"rm -rf ./b"}','x','deny','denied','2026-10-06T00:00:00Z'),
		 (3,'s','/work/repo','Monitor','h3','{"command":"tail -f x"}','x','abstain','unresolved','2026-10-07T00:00:00Z'),
		 (4,'s','/work/repo','Bash','h4','{"command":"ls y"}','x','allow','approved','2026-09-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("seed rows: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	before, err := os.Stat(db)
	if err != nil {
		t.Fatal(err)
	}
	beforeBytes, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := runShapes(&stdout, shapesOpts{since: "2026-10-02", db: db, src: sourceFlags{source: sourceCeta}}); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if got, want := lines[len(lines)-1], "rows=3 shapes=2 collapse_ratio=2/3=0.6667 baseline=77114/84581"; got != want {
		t.Fatalf("last stdout line = %q, want %q (the row before --since must be excluded)", got, want)
	}

	art := filepath.Join(state, "claude-extended-tool-approver", "phase4", "shapes.jsonl")
	f, err := os.Open(art)
	if err != nil {
		t.Fatalf("ART/shapes.jsonl not written: %v", err)
	}
	defer func() { _ = f.Close() }()
	var got []shapeEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e shapeEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		got = append(got, e)
	}
	if len(got) != 2 || got[0].Rows != 2 || got[0].Program != "rm" || got[1].Tool != "Monitor" {
		t.Fatalf("shapes.jsonl = %+v", got)
	}

	after, err := os.Stat(db)
	if err != nil {
		t.Fatal(err)
	}
	afterBytes, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || !bytes.Equal(beforeBytes, afterBytes) {
		t.Fatal("shapes modified the ask-log database")
	}
}

func TestShapeCommandEmptyWindow(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	db := filepath.Join(t.TempDir(), "asks.db")
	store, err := asklog.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	out := filepath.Join(t.TempDir(), "sub", "shapes.jsonl")
	var stdout bytes.Buffer
	if err := runShapes(&stdout, shapesOpts{since: "2026-10-02", db: db, out: out, src: sourceFlags{source: sourceCeta}}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	if got, want := lines[len(lines)-1], "rows=0 shapes=0 collapse_ratio=0/0=n/a baseline=77114/84581"; got != want {
		t.Fatalf("last line = %q, want %q", got, want)
	}
	if b, err := os.ReadFile(out); err != nil || len(b) != 0 {
		t.Fatalf("empty window must write an empty file, got %q err=%v", b, err)
	}
}

func TestShapeCommandRejectsUnknownSource(t *testing.T) {
	err := runShapes(&bytes.Buffer{}, shapesOpts{src: sourceFlags{source: "bogus"}})
	if err == nil || !strings.Contains(err.Error(), "--source") {
		t.Fatalf("want a --source error, got %v", err)
	}
}

func TestShapeSubcommandRegistered(t *testing.T) {
	if !knownSubcommands["shapes"] {
		t.Fatal("shapes missing from knownSubcommands")
	}
	cmd, _, err := newRootCmd().Find([]string{"shapes"})
	if err != nil || cmd.Name() != "shapes" {
		t.Fatalf("shapes not on the root command: %v", err)
	}
}
