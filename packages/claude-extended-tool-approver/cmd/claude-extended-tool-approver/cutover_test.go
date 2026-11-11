package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cutover"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/metrics"
)

// The per-machine cut-over switch (internal/cutover). With it OFF, CETA's
// recorded rows must equal testdata/cutover/rows-switch-off.golden, which was
// captured from the build BEFORE the switch existed, with:
//
//	go test ./cmd/claude-extended-tool-approver -run TestCutover_SwitchOffRowsGolden -update-cutover-goldens
//
// run on that unchanged build. Re-running with the flag on a later build would
// overwrite it, so only do it deliberately.
var updateCutoverGoldens = flag.Bool("update-cutover-goldens", false, "rewrite testdata/cutover goldens from the current build")

func cutoverHookInput(event, toolUseID, command string) *hookio.HookInput {
	ti, _ := json.Marshal(map[string]string{"command": command})
	return &hookio.HookInput{
		SessionID: "sess-cutover", CWD: "/tmp", ToolName: "Bash", ToolUseID: toolUseID,
		ToolInput: ti, PermissionMode: "default", HookEventName: event,
	}
}

// runCutoverScenario drives every handler once against a fresh throwaway
// database and returns the path of that database and of the state dir the
// ceta.* events went to. flip is "1" for the switch on and "" for off.
func runCutoverScenario(t *testing.T, flip string) (dbPath, stateDir string) {
	t.Helper()
	data := t.TempDir()
	stateDir = t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_STATE_HOME", stateDir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv(cutover.EnvVar, flip)
	dbPath = asklog.DefaultDBPath()

	metrics.DefaultRuleErrors.Reset()
	metrics.DefaultRuleErrors.Record("cutover-test-rule", errors.New("synthetic rule failure"))
	t.Cleanup(metrics.DefaultRuleErrors.Reset)

	handlePreToolUse(cutoverHookInput("PreToolUse", "tu-pre", "git status"))
	handlePermissionRequest(cutoverHookInput("PermissionRequest", "tu-ask", "some-unknown-tool --flag"))
	handlePostToolUse(cutoverHookInput("PostToolUse", "tu-pre", "git status"))

	bg := cutoverHookInput("PostToolUse", "tu-bg", "sleep 100")
	bg.ToolInput = json.RawMessage(`{"command":"sleep 100","run_in_background":true}`)
	bg.ToolResponse = json.RawMessage(`{"shell_id":"shell-cutover-1"}`)
	handlePostToolUse(bg)

	handlePermissionDenied(cutoverHookInput("PermissionDenied", "tu-denied", "rm -rf /nowhere"))
	handlePreToolUse(cutoverHookInput("PreToolUse", "tu-fail", "git log"))
	handlePostToolUseFailure(cutoverHookInput("PostToolUseFailure", "tu-fail", "git log"))
	handleSessionEnd(cutoverHookInput("SessionEnd", "", ""))
	return dbPath, stateDir
}

// dumpRows renders every row of the tables the cut-over concerns, with the
// volatile columns (ids and timestamps) dropped, one stable line per row.
func dumpRows(t *testing.T, dbPath string) string {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var b strings.Builder
	for _, table := range []string{"tool_decisions", "decision_trace_entries", "rule_errors", "background_shells"} {
		rows, err := db.Query("SELECT * FROM " + table + " ORDER BY 1")
		if err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		cols, _ := rows.Columns()
		n := 0
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			rec := map[string]string{}
			for i, c := range cols {
				if c == "id" || c == "tool_decision_id" || strings.HasSuffix(c, "_at") || strings.HasSuffix(c, "_ms") {
					continue
				}
				switch v := vals[i].(type) {
				case nil:
					rec[c] = "NULL"
				case []byte:
					rec[c] = string(v)
				default:
					rec[c] = fmt.Sprint(v)
				}
			}
			keys := make([]string, 0, len(rec))
			for k := range rec {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts := make([]string, 0, len(keys))
			for _, k := range keys {
				parts = append(parts, k+"="+rec[k])
			}
			fmt.Fprintf(&b, "%s | %s\n", table, strings.Join(parts, " | "))
			n++
		}
		_ = rows.Close()
		fmt.Fprintf(&b, "-- %s: %d rows\n", table, n)
	}
	return b.String()
}

func TestCutover_SwitchOffRowsGolden(t *testing.T) {
	db, _ := runCutoverScenario(t, "")
	got := dumpRows(t, db)
	golden := filepath.Join("testdata", "cutover", "rows-switch-off.golden")
	if *updateCutoverGoldens {
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
		t.Errorf("with the switch off, CETA's recorded rows changed:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	// Guard against a vacuous golden: every recording path must have produced rows.
	for _, need := range []string{"-- tool_decisions: 4 rows", "-- rule_errors: 2 rows", "-- background_shells: 1 rows"} {
		if !strings.Contains(got, need) {
			t.Errorf("golden lacks %q; the scenario no longer exercises that path", need)
		}
	}
}

func TestCutover_SwitchOnStopsRecording(t *testing.T) {
	db, state := runCutoverScenario(t, "1")
	got := dumpRows(t, db)
	for _, tbl := range []string{"tool_decisions", "decision_trace_entries", "rule_errors"} {
		if want := "-- " + tbl + ": 0 rows"; !strings.Contains(got, want) {
			t.Errorf("switch on: want %q, dump:\n%s", want, got)
		}
	}
	// Background-shell registration (PostToolUse) must remain: the killshell rule needs it.
	if !strings.Contains(got, "-- background_shells: 1 rows") {
		t.Errorf("switch on: background shell registration stopped, dump:\n%s", got)
	}
	// The ceta.* events, rule_errors included, replace the database flush.
	matches, _ := filepath.Glob(filepath.Join(state, "claude-tool-tracker", "events", "*.ceta.jsonl"))
	if len(matches) == 0 {
		t.Fatal("switch on: no ceta.* event file written")
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"ceta.rule_errors"`) || !strings.Contains(string(raw), `"ceta.verdict"`) {
		t.Errorf("switch on: ceta.verdict and ceta.rule_errors events must still be emitted:\n%s", raw)
	}
}

func TestCutover_SwitchOnSpoolsNothing(t *testing.T) {
	db, _ := runCutoverScenario(t, "1")
	if n := asklog.SpoolLen(db); n != 0 {
		t.Errorf("switch on: %d decisions spooled, want 0", n)
	}
}

func TestCutover_DefaultSource(t *testing.T) {
	for _, tc := range []struct{ env, want string }{{"", sourceCeta}, {"0", sourceCeta}, {"1", sourceTracker}} {
		t.Setenv(cutover.EnvVar, tc.env)
		root := newEvaluateCmd()
		if got := root.Flag("source").DefValue; got != tc.want {
			t.Errorf("%s=%q: --source default = %q, want %q", cutover.EnvVar, tc.env, got, tc.want)
		}
	}
}

// With the switch on, CETA's old database stays readable under --source ceta.
func TestCutover_OldDatabaseReadableUnderSourceCeta(t *testing.T) {
	seedCetaHistory(t)
	t.Setenv(cutover.EnvVar, "")
	off := runCLI(t, "show", "1", "--format", "json")
	t.Setenv(cutover.EnvVar, "1")
	on := runCLI(t, "show", "1", "--format", "json", "--source", "ceta")
	if on != off || !strings.Contains(on, "tool_name") && len(on) < 10 {
		t.Errorf("--source ceta with the switch on differs from the switch-off read:\non:  %s\noff: %s", on, off)
	}
}
