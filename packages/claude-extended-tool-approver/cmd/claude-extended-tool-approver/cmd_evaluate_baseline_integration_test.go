//go:build integration

// Integration tests for `evaluate --baseline`. Each seeds a real SQLite ask
// log (an isolated, synthetic fixture under t.TempDir() — never the real
// production asklog, per pg2-f1vss) and drives the COMPILED binary over it, so
// they carry the `integration` tag. See main_integration_test.go for the full
// rationale and how to run them:
//
//	go test -tags integration ./...

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
)

// setupBaselineFlipDB seeds three rows whose replay verdict is driven purely
// by the consumer rules.json config, so re-running `evaluate` after rewriting
// that file (no rebuild needed) reproduces "the same command, run before and
// after a rule change": `ls` replays allow (safecmds) unless blockedCommands
// blocks it; `cat .env` replays ask (secrets) unless approvedCommands exempts
// it; `unknown-cmd-xyz` replays abstain regardless (no rule and no config key
// below ever touches it), so it pins the "does not move" case.
func setupBaselineFlipDB(t *testing.T) (dataDir, cwd string) {
	t.Helper()
	dataDir = t.TempDir()
	cwd = t.TempDir()
	store, err := asklog.NewStore(filepath.Join(dataDir, "claude-extended-tool-approver", "asks.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.DB().Exec(`INSERT INTO tool_decisions
		(id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary, hook_decision, outcome, created_at)
		VALUES
		 (1,'s',?,'Bash','h1','{"command":"ls"}','ls','allow','approved','2026-01-01T00:00:00Z'),
		 (2,'s',?,'Bash','h2','{"command":"cat .env"}','cat .env','ask','approved','2026-01-01T00:00:00Z'),
		 (3,'s',?,'Bash','h3','{"command":"unknown-cmd-xyz"}','unknown-cmd-xyz','abstain','approved','2026-01-01T00:00:00Z')`,
		cwd, cwd, cwd)
	if err != nil {
		t.Fatalf("seed rows: %v", err)
	}
	_ = store.Close()
	return dataDir, cwd
}

// writeRulesJSON writes the consumer config that flips row 1 (ls) to deny and
// row 2 (cat .env) to allow, at $XDG_CONFIG_HOME/claude-extended-tool-approver/rules.json.
func writeRulesJSON(t *testing.T, configHome string) {
	t.Helper()
	dir := filepath.Join(configHome, "claude-extended-tool-approver")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"approvedCommands": ["cat"], "blockedCommands": ["ls"]}`
	if err := os.WriteFile(filepath.Join(dir, "rules.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// runEvaluateWithConfig execs the binary with an explicit, caller-controlled
// XDG_CONFIG_HOME (so the test can rewrite rules.json between two calls
// without rebuilding), returning stdout+stderr and the process exit code (0
// on success, matching the raw code on a non-zero exit).
func runEvaluateWithConfig(t *testing.T, dataDir, configHome string, args ...string) (output string, exitCode int) {
	t.Helper()
	cmd := exec.Command(testBinary(t), append([]string{"evaluate"}, args...)...)
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+dataDir, "XDG_CONFIG_HOME="+configHome)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return string(out), exitErr.ExitCode()
	}
	t.Fatalf("evaluate did not run: %v\noutput: %s", err, out)
	return "", -1
}

// decodeJSONValue locates the first JSON value (object or array) in output
// that also carries the engine's unconditional per-rule stderr lines
// (CombinedOutput interleaves them ahead of the JSON — see engine.go's
// unconditional "module -> decision: reason" logging, the same reason
// existing evaluate/show integration tests skip to the first delimiter
// rather than assuming byte 0), and decodes exactly that one value, ignoring
// anything printed after it (e.g. the capture-mode "baseline captured to..."
// stderr line).
func decodeJSONValue(t *testing.T, out string, v interface{}) {
	t.Helper()
	objIdx := strings.IndexByte(out, '{')
	arrIdx := strings.IndexByte(out, '[')
	idx := objIdx
	if idx == -1 || (arrIdx != -1 && arrIdx < idx) {
		idx = arrIdx
	}
	if idx == -1 {
		t.Fatalf("no JSON value found in output: %s", out)
	}
	if err := json.NewDecoder(strings.NewReader(out[idx:])).Decode(v); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, out)
	}
}

// TestEvaluateBaseline_CaptureThenCompare_EndToEnd drives the documented
// workflow through the real CLI: the same `--baseline <file>` invocation run
// twice, with a config-only change in between standing in for "the rule
// change". It must report the ls row (allow -> deny) as more-restrictive, the
// `cat .env` row (ask -> allow) as less-restrictive, leave the unmoved row out
// of the delta, and exit non-zero (2) because a less-restrictive move exists —
// the invariant the ceta beads recite ("no row moves in the less-restrictive
// direction") failing loudly rather than silently.
func TestEvaluateBaseline_CaptureThenCompare_EndToEnd(t *testing.T) {
	dataDir, _ := setupBaselineFlipDB(t)
	configHome := t.TempDir()
	baselinePath := filepath.Join(t.TempDir(), "baseline.json")

	// BEFORE: no consumer config, so ls=allow, cat .env=ask, unknown=abstain.
	// The file does not exist yet, so this call CAPTURES.
	out, code := runEvaluateWithConfig(t, dataDir, configHome, "--format=json", "--baseline="+baselinePath)
	if code != 0 {
		t.Fatalf("capture run exited %d, want 0\noutput: %s", code, out)
	}
	if _, err := os.Stat(baselinePath); err != nil {
		t.Fatalf("baseline file was not captured: %v", err)
	}
	var captured evaluateReport
	decodeJSONValue(t, out, &captured)
	if captured.Delta != nil {
		t.Errorf("capture run reported a delta = %+v, want nil (nothing to compare against yet)", captured.Delta)
	}

	// The "rule change": flip ls to blocked and cat to approved via config.
	writeRulesJSON(t, configHome)

	// AFTER: same invocation, same baseline path — now compares.
	out, code = runEvaluateWithConfig(t, dataDir, configHome, "--format=json", "--baseline="+baselinePath)
	if code != 2 {
		t.Fatalf("compare run exited %d, want 2 (a less-restrictive move exists)\noutput: %s", code, out)
	}
	var report evaluateReport
	decodeJSONValue(t, out, &report)
	if report.Delta == nil {
		t.Fatal("compare run reported no delta at all")
	}

	byID := map[int]movedRow{}
	for _, m := range report.Delta.Moved {
		byID[m.ID] = m
	}
	if m, ok := byID[1]; !ok {
		t.Error("row 1 (ls: allow -> deny) missing from the delta")
	} else if m.FromVerdict != "allow" || m.ToVerdict != "deny" || m.Direction != "more-restrictive" {
		t.Errorf("row 1 = %+v, want allow -> deny, more-restrictive", m)
	}
	if m, ok := byID[2]; !ok {
		t.Error("row 2 (cat .env: ask -> allow) missing from the delta")
	} else if m.FromVerdict != "ask" || m.ToVerdict != "allow" || m.Direction != "less-restrictive" {
		t.Errorf("row 2 = %+v, want ask -> allow, less-restrictive", m)
	}
	if _, moved := byID[3]; moved {
		t.Errorf("row 3 (unknown-cmd-xyz) did not move but appeared in the delta: %+v", byID[3])
	}
	if len(report.Delta.Added) != 0 || len(report.Delta.Removed) != 0 {
		t.Errorf("same corpus both runs: Added=%v Removed=%v, want both empty", report.Delta.Added, report.Delta.Removed)
	}
}

// TestEvaluateBaseline_MismatchedFilters_Refuses: a baseline captured under
// --misses-only compared against a run WITHOUT it must refuse rather than
// silently diff two incomparable row sets (pg2-f1vss's core safety
// requirement — a false "no rows moved" is the worst output a regression gate
// can produce).
func TestEvaluateBaseline_MismatchedFilters_Refuses(t *testing.T) {
	dataDir, _ := setupBaselineFlipDB(t)
	configHome := t.TempDir()
	baselinePath := filepath.Join(t.TempDir(), "baseline.json")

	out, code := runEvaluateWithConfig(t, dataDir, configHome, "--format=json", "--misses-only", "--baseline="+baselinePath)
	if code != 0 {
		t.Fatalf("capture run exited %d, want 0\noutput: %s", code, out)
	}

	out, code = runEvaluateWithConfig(t, dataDir, configHome, "--format=json", "--baseline="+baselinePath)
	if code == 0 {
		t.Fatalf("compare run with mismatched --misses-only exited 0, want a refusal\noutput: %s", out)
	}
	if !strings.Contains(out, "different filters") {
		t.Errorf("output does not name the filter mismatch: %s", out)
	}
	if strings.Contains(out, `"delta"`) {
		t.Errorf("a refused comparison must not also emit a delta: %s", out)
	}
}

// TestEvaluate_DefaultJSONShapeIsBareArray pins the compatibility requirement:
// without --baseline, `--format json` stays a bare top-level array (what the
// command's own `jq '.[] | ...'` Long-help recipe depends on), never the
// --baseline envelope object.
func TestEvaluate_DefaultJSONShapeIsBareArray(t *testing.T) {
	dataDir, _ := setupBaselineFlipDB(t)

	out, code := runEvaluateWithConfig(t, dataDir, t.TempDir(), "--format=json")
	if code != 0 {
		t.Fatalf("evaluate exited %d, want 0\noutput: %s", code, out)
	}
	arrIdx := strings.IndexByte(out, '[')
	objIdx := strings.IndexByte(out, '{')
	if arrIdx == -1 || (objIdx != -1 && objIdx < arrIdx) {
		t.Fatalf("default --format json output's first JSON delimiter is not '[': %s", out)
	}
	var rows []map[string]interface{}
	decodeJSONValue(t, out, &rows)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
}
