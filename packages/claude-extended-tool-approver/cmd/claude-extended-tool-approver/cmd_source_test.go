package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
)

// The golden files under testdata/source-ceta/ were captured from the build
// BEFORE --source existed, with:
//
//	go test ./cmd/claude-extended-tool-approver -run TestSourceCeta_Golden -update-source-goldens
//
// run on that unchanged build. They pin that the default (--source ceta) output
// of evaluate, report and show never changes. Re-running with the flag on a
// later build would overwrite them, so only do it deliberately.
var updateSourceGoldens = flag.Bool("update-source-goldens", false, "rewrite testdata/source-ceta goldens from the current build")

// runCLI runs the real cobra root in-process and returns what it printed on
// stdout. The commands print with fmt to os.Stdout, so it is swapped for a pipe.
func runCLI(t *testing.T, args ...string) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()
	root := newRootCmd()
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	execErr := root.Execute()
	_ = w.Close()
	os.Stdout = old
	out := <-done
	if execErr != nil {
		t.Fatalf("%v: %v", args, execErr)
	}
	return string(out)
}

// seedCetaHistory writes a small CETA ask log into a fresh XDG_DATA_HOME and
// returns the cwd its rows use (a real directory so nothing is stale-cwd).
func seedCetaHistory(t *testing.T) string {
	t.Helper()
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	cwd := t.TempDir()
	store, err := asklog.NewStore(filepath.Join(data, "claude-extended-tool-approver", "asks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	_, err = store.DB().Exec(`INSERT INTO tool_decisions
		(id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary,
		 hook_decision, outcome, outcome_notes, permission_mode, created_at)
		VALUES
		 (1,'s1',?,'Bash','h1','{"command":"unknown-cmd-xyz"}','unknown-cmd-xyz','abstain','approved',NULL,'default','2026-01-01T00:00:00Z'),
		 (2,'s1',?,'Bash','h2','{"command":"unknown-cmd-xyz"}','unknown-cmd-xyz','abstain','denied','auto_mode_classifier: user declined','auto','2026-01-01T00:00:01Z'),
		 (3,'s1',?,'Bash','h3','{"command":"unknown-cmd-xyz"}','unknown-cmd-xyz','allow','pending',NULL,'default','2026-01-01T00:00:02Z'),
		 (4,'s2',?,'Bash','h4','{"command":"unknown-cmd-xyz"}','unknown-cmd-xyz','deny','rejected',NULL,'default','2026-01-01T00:00:03Z')`,
		cwd, cwd, cwd, cwd)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return cwd
}

func TestSourceCeta_Golden(t *testing.T) {
	seedCetaHistory(t)
	cases := []struct {
		name string
		args []string
	}{
		{"evaluate.json", []string{"evaluate", "--format", "json"}},
		{"evaluate.summary", []string{"evaluate"}},
		{"report.json", []string{"report", "--format", "json", "--group-by", "outcome"}},
		{"report.table", []string{"report", "--group-by", "tool_name"}},
		{"show.table", []string{"show", "1", "2", "4"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			golden := filepath.Join("testdata", "source-ceta", tc.name+".golden")
			// the explicit flag value must match the default byte for byte
			defaultOut := runCLI(t, tc.args...)
			explicitOut := defaultOut
			if !*updateSourceGoldens {
				explicitOut = runCLI(t, append(append([]string{}, tc.args...), "--source", "ceta")...)
			}
			if *updateSourceGoldens {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, []byte(defaultOut), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal([]byte(defaultOut), want) {
				t.Errorf("default output differs from golden %s:\n%s", golden, defaultOut)
			}
			if explicitOut != defaultOut {
				t.Errorf("--source ceta differs from the default:\n%s", explicitOut)
			}
		})
	}
}

// --- --source tracker ---

// The tracker fixtures are built by the real claude-tool-tracker ingest (SQL dumps loaded into a temp database; see
// internal/trackeradapter/testdata/gen.sh). Goldens under testdata/source-tracker
// are rewritten with -update-tracker-goldens and MUST be reviewed by hand.
var updateTrackerGoldens = flag.Bool("update-tracker-goldens", false, "rewrite testdata/source-tracker goldens from the current build")

func trackerFixture(t *testing.T, name string) string {
	t.Helper()
	dump, err := os.ReadFile(filepath.Join("..", "..", "internal", "trackeradapter", "testdata", strings.TrimSuffix(name, ".db")+".sql"))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), name)
	db, err := sql.Open("sqlite", "file:"+p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(dump)); err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func checkTrackerGolden(t *testing.T, name, got string) {
	t.Helper()
	golden := filepath.Join("testdata", "source-tracker", name+".golden")
	if *updateTrackerGoldens {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("output differs from golden %s:\n%s", golden, got)
	}
}

func TestSourceTracker_Golden(t *testing.T) {
	// a CETA ask log full of OTHER calls: --source tracker must never show them
	seedCetaHistory(t)
	db := trackerFixture(t, "tracker.db")
	cases := []struct {
		name string
		args []string
	}{
		{"evaluate.json", []string{"evaluate", "--format", "json"}},
		{"evaluate.summary", []string{"evaluate"}},
		{"report.outcome.json", []string{"report", "--format", "json", "--group-by", "outcome"}},
		{"report.tool.table", []string{"report", "--group-by", "tool_name"}},
		{"show.json", []string{"show", "--format", "json", "16", "4"}},
		{"show.table", []string{"show", "1", "21", "25"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := runCLI(t, append(append([]string{}, tc.args...), "--source", "tracker", "--tracker-db", db)...)
			checkTrackerGolden(t, tc.name, out)
		})
	}
}

func TestSourceFlag_RejectsUnknownSource(t *testing.T) {
	seedCetaHistory(t)
	for _, args := range [][]string{
		{"evaluate", "--source", "nope"},
		{"report", "--source", "nope"},
		{"show", "1", "--source", "nope"},
		{"set-correct-decision", "1", "--decision", "allow", "--explanation", "x", "--source", "nope"},
		{"mark-excluded", "1", "--reason", "x", "--source", "nope"},
	} {
		root := newRootCmd()
		root.SetArgs(args)
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		if err := root.Execute(); err == nil {
			t.Errorf("%v: an unknown --source must be an error", args)
		}
	}
}

// Under --source tracker the ids are the tracker's call_ids, and a label keyed
// by session_id + tool_use_id survives a re-ingest that renumbers every call_id.
func TestSourceTracker_LabelsSurviveReingest(t *testing.T) {
	seedCetaHistory(t)
	first := trackerFixture(t, "tracker.db")
	reingested := trackerFixture(t, "tracker_reingest.db")

	// sess-b/tu-b1 is call_id 16 in the first database and 1 in the second
	out := runCLI(t, "set-correct-decision", "16", "--decision", "deny", "--explanation", "never fetch", "--source", "tracker", "--tracker-db", first)
	if out != "Set correct_hook_decision=\"deny\" on 1 row(s)\n" {
		t.Errorf("set-correct-decision output: %q", out)
	}
	runCLI(t, "mark-excluded", "4", "--reason", "scratch call", "--source", "tracker", "--tracker-db", first)

	showJSON := func(db string, id string) []showResult {
		var res []showResult
		raw := runCLI(t, "show", id, "--format", "json", "--source", "tracker", "--tracker-db", db)
		if err := json.Unmarshal([]byte(raw), &res); err != nil {
			t.Fatalf("%v\n%s", err, raw)
		}
		return res
	}
	for _, c := range []struct{ db, label, id string }{{first, "first", "16"}, {reingested, "re-ingested", "1"}} {
		got := showJSON(c.db, c.id)
		if len(got) != 1 || got[0].SessionID != "sess-b" || got[0].CorrectDec != "deny" || got[0].CorrectDecExplanation != "never fetch" {
			t.Errorf("%s database: label missing for call_id %s: %+v", c.label, c.id, got)
		}
	}
	// the exclusion of sess-a/tu-a2 (call_id 4 / 13) follows it too, and the
	// excluded call leaves evaluate but stays reachable through show
	for _, c := range []struct{ db, id string }{{first, "4"}, {reingested, "13"}} {
		got := showJSON(c.db, c.id)
		if len(got) != 1 || got[0].Excluded != 1 || got[0].ExcludedReason != "scratch call" {
			t.Errorf("exclusion missing for call_id %s: %+v", c.id, got)
		}
		var rows []evalResult
		raw := runCLI(t, "evaluate", "--format", "json", "--source", "tracker", "--tracker-db", c.db)
		if err := json.Unmarshal([]byte(raw), &rows); err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.ID == mustAtoi(t, c.id) {
				t.Errorf("excluded call %s still in evaluate", c.id)
			}
		}
		if len(rows) != 4 {
			t.Errorf("evaluate rows = %d, want 4", len(rows))
		}
	}
	// labels under tracker never leak into CETA's own history
	var cetaRows []showResult
	if err := json.Unmarshal([]byte(runCLI(t, "show", "1", "2", "3", "4", "--format", "json")), &cetaRows); err != nil {
		t.Fatal(err)
	}
	for _, r := range cetaRows {
		if r.CorrectDec != "" || r.Excluded != 0 {
			t.Errorf("a tracker label reached CETA row %d", r.ID)
		}
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// evaluate --baseline under --source tracker keys rows by the tracker's call_id
// and refuses a baseline captured from the other history.
func TestSourceTracker_BaselineUsesCallIDs(t *testing.T) {
	seedCetaHistory(t)
	db := trackerFixture(t, "tracker.db")
	base := filepath.Join(t.TempDir(), "base.json")
	runCLI(t, "evaluate", "--format", "json", "--baseline", base, "--source", "tracker", "--tracker-db", db)
	raw, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	var env evaluateReport
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	var ids []int
	for _, r := range env.Results {
		ids = append(ids, r.ID)
	}
	sort.Ints(ids)
	if fmt.Sprint(ids) != "[1 4 16 21 25]" || env.Filters.Source != "tracker" {
		t.Errorf("baseline ids %v filters %+v", ids, env.Filters)
	}
	// a second run compares against it without complaint
	runCLI(t, "evaluate", "--format", "json", "--baseline", base, "--source", "tracker", "--tracker-db", db)
}
