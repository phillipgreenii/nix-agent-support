package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
)

var cliBinary string

func TestMain(m *testing.M) {
	root, err := findModuleRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	f, err := os.CreateTemp("", "claude-extended-tool-approver-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = f.Close()
	cliBinary = f.Name()

	build := exec.Command("go", "build", "-o", cliBinary, "./cmd/claude-extended-tool-approver")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build failed: %v\n%s\n", err, out)
		_ = os.Remove(cliBinary)
		os.Exit(1)
	}

	// Make every store THIS PROCESS opens non-durable. The helpers in
	// cmd_show_test.go and cmd_evaluate_test.go each build a throwaway SQLite
	// database under t.TempDir() via asklog.NewStore, and creating one costs 11
	// durable flushes. fsync latency is a host-filesystem property spanning
	// orders of magnitude — measured at 1.1-3.6s per fsync on the loaded QEMU VM
	// that builds monorepod — so a single setupShowTestDB there cost 16.07s
	// against 12ms for the exec'd binary it then drives (tc-fqu7). The suite's
	// wall clock was a multiple of an unbounded host property; this removes the
	// flushes rather than budgeting for them. See synchronousPragma in
	// internal/asklog/store.go for the full write-up.
	//
	// This reaches ONLY this process, and that is deliberate: the exec'd binary
	// is the SHIPPED one and keeps the SHIPPED durability, because no env var or
	// flag may change how the real binary behaves (16e1fd4d, pg2-iay90). The
	// residual cost is therefore the child's, and it is bounded by how many
	// databases the child has to CREATE: a test that inherits the XDG_DATA_HOME
	// set below opens an already-migrated database (5 flushes), while one that
	// overrides it with its own t.TempDir() makes the child build a schema from
	// scratch (11). Measured over the whole package with
	// `strace -f -c -e trace=fsync`: 445 flushes before this line, 188 after.
	// Prefer inheriting unless a test actually asserts on ask-log CONTENT.
	asklog.SetSynchronousForTests("OFF")

	// Isolate the ask-log for the WHOLE package. The hook-mode tests below run
	// the real binary, which opens asklog.DefaultDBPath() and INSERTS a row per
	// invocation. Without this, every `go test` run wrote synthetic rows into the
	// developer's real ~/.local/share/claude-extended-tool-approver/asks.db —
	// permanently polluting the corpus that `evaluate` treats as ground truth,
	// and running schema migrations against it as a side effect of testing.
	//
	// Setting it here (rather than in each test) makes isolation the default, so
	// a newly added hook-mode test cannot reintroduce the leak. Individual tests
	// that need their own store still override it with t.Setenv, which wins.
	dataHome, err := os.MkdirTemp("", "claude-extended-tool-approver-xdg-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.Remove(cliBinary)
		os.Exit(1)
	}
	if err := os.Setenv("XDG_DATA_HOME", dataHome); err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.Remove(cliBinary)
		_ = os.RemoveAll(dataHome)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.Remove(cliBinary)
	_ = os.RemoveAll(dataHome)
	os.Exit(code)
}

func findModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found")
		}
		dir = parent
	}
}

// hookAttempts bounds how many times runHook re-runs the binary when the only
// thing that went wrong was the input processor's exec being killed by the
// shipped deadline. Three makes a single lost CPU slice a non-event while still
// failing, loudly and finitely, on a machine that cannot spawn a two-line shell
// script at all.
const hookAttempts = 3

// inputProcDeadlineKilled reports whether the binary's stderr says the input
// processor's exec hit its deadline. That line is the ONLY way the fact crosses
// the process boundary: Process() collapses "killed" and "declined" into the
// same (string, bool), so from out here a kill is indistinguishable from a
// processor that chose not to rewrite. The needle is DERIVED from the sentinel
// rather than typed out, so a stdlib rewording cannot make this stop matching
// silently; the prefix pins it to the input processor specifically, since other
// subsystems have deadlines of their own. The proof that the real binary still
// emits a matching line is
// TestIntegration_InputProcessor_DeadlineKillIsVisibleOnStderr.
func inputProcDeadlineKilled(stderr string) bool {
	return strings.Contains(stderr, "input processor: "+context.DeadlineExceeded.Error())
}

// runHook runs the binary and retries iff the input processor's exec was killed
// by its deadline — see TestIntegration_InputProcessor_RewritesBashApprove for
// why that is the remedy here rather than a bigger deadline. Retrying is safe
// because the trigger is not an assertion failure: a processor that actually ran
// and produced the wrong answer fails on the first attempt. The retry lives here
// rather than in the one test that needs it today for the same reason TestMain
// isolates XDG_DATA_HOME package-wide — a newly added hook-mode test that
// configures a processor cannot reintroduce the flake by forgetting to opt in.
// For every test that configures no processor this is an unconditional no-op.
func runHook(t *testing.T, input string) map[string]any {
	t.Helper()
	var lastStderr string
	for attempt := 1; attempt <= hookAttempts; attempt++ {
		result, stderr := runHookOnce(t, input)
		if !inputProcDeadlineKilled(stderr) {
			return result
		}
		lastStderr = strings.TrimSpace(stderr)
		t.Logf("attempt %d/%d: the input processor exec was killed by the shipped deadline; retrying, because this is the environment failing to spawn the mock and not the code: %s", attempt, hookAttempts, lastStderr)
	}
	t.Fatalf("the input processor exec was killed by the shipped deadline on all %d attempts: this environment could not spawn the mock processor in time, which is NOT a logic failure: %s", hookAttempts, lastStderr)
	return nil
}

// runHookOnce runs the binary once and returns its parsed stdout ALONGSIDE its
// stderr. Capturing stderr is what makes the retry above possible: cmd.Output()
// keeps stderr only to attach to an ExitError, so on the exit-0 path — which is
// every hook run, including one whose input processor was killed — the binary's
// own diagnosis was being discarded.
func runHookOnce(t *testing.T, input string) (map[string]any, string) {
	t.Helper()
	cmd := exec.Command(cliBinary)
	cmd.Env = os.Environ()
	cmd.Stdin = bytes.NewBufferString(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("hook failed: %v\nstderr: %s", err, stderr.String())
	}
	var result map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &result); err != nil {
		t.Fatalf("invalid JSON output: %v\nraw: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	return result, stderr.String()
}

func getDecision(result map[string]any) string {
	hso, ok := result["hookSpecificOutput"].(map[string]any)
	if !ok {
		return ""
	}
	d, _ := hso["permissionDecision"].(string)
	return d
}

func TestIntegration_GitLog(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"git log --oneline"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "allow" {
		t.Errorf("git log decision = %q, want allow", d)
	}
}

func TestIntegration_AskQuestion(t *testing.T) {
	input := `{"tool_name":"AskQuestion","tool_input":{},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "allow" {
		t.Errorf("AskQuestion decision = %q, want allow", d)
	}
}

func TestIntegration_UnknownCommand(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"some-random-command"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if len(result) != 0 {
		t.Errorf("unknown command should return empty JSON, got %v", result)
	}
}

func TestIntegration_BadJSON(t *testing.T) {
	cmd := exec.Command(cliBinary)
	cmd.Env = os.Environ()
	cmd.Stdin = bytes.NewBufferString("not json")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hook should not fail on bad json: %v", err)
	}
	if string(bytes.TrimSpace(out)) != "{}" {
		t.Errorf("bad json should return {}, got %s", out)
	}
}

func TestIntegration_MCPTool(t *testing.T) {
	input := `{"tool_name":"mcp__Atlassian-MCP-Server__getJiraIssue","tool_input":{},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "allow" {
		t.Errorf("MCP tool decision = %q, want allow", d)
	}
}

// assertEmptyHookOutput pins the two halves of "the hook emitted the empty
// object": the decoded output carries NOTHING (so the binary printed `{}`), and in
// particular it does not carry `permissionDecision: "allow"`. The second check is
// redundant with the first by construction and is asserted anyway, because "allow"
// is the ONE wrong answer that would be indistinguishable from the right one in a
// test that only compared decision strings — an Abstain and a missing key both read
// as "".
func assertEmptyHookOutput(t *testing.T, cmd string, result map[string]any) {
	t.Helper()
	if len(result) != 0 {
		t.Errorf("cmd %q: emitted %v, want the empty object {} — an abstaining rule must hand the decision to claude-code, not answer it", cmd, result)
	}
	if d := getDecision(result); d == "allow" {
		t.Errorf("cmd %q: emitted permissionDecision=%q — the abstain was re-approved by a later rule, which is the exact regression this test exists for", cmd, d)
	}
}

// TestIntegration_GitResetHard_EmitsEmptyObject is the BOUNDARY assertion for the
// operator ruling of pg2-4yy4r item 4 (implemented by pg2-ur9zc): `git reset --hard`
// is an Abstain in the git rule, and what the HOOK emits for it must be `{}`.
//
// A rule-level assertion on RuleResult.Decision is NOT sufficient coverage and the
// acceptance criteria say so: the git rule is mid-chain, so only running the real
// binary proves no later rule re-approves the leaf and that hookio.FormatOutput
// turns the verdict into `{}` rather than an `allow`. `--har` is included because
// git's parse-options accepts it as `--hard` (pg2-os1kq), so the ruling has to reach
// every abbreviation, not just the canonical spelling.
func TestIntegration_GitResetHard_EmitsEmptyObject(t *testing.T) {
	for _, cmd := range []string{
		"git reset --hard",
		"git reset --hard HEAD~1",
		"git reset --hard origin/main",
		"git reset --har HEAD~1",
	} {
		input := fmt.Sprintf(`{"tool_name":"Bash","tool_input":{"command":%q},"cwd":"/tmp"}`, cmd)
		assertEmptyHookOutput(t, cmd, runHook(t, input))
	}
}

// TestIntegration_GitResetHardCompound_EmitsEmptyObject pins the COMPOUND case
// separately, because it exercises a different machine: `&&` makes the engine fold
// the leaves through hookio.MostRestrictive, whose seed is Approve. The trailing
// `echo ok` is approvable on its own, so the emitted verdict is `{}` only because
// Abstain outranks Approve in that fold (pg2-t4uyx). If that ordering ever moved,
// the single-leaf test above would still pass while a hard reset inside a compound
// answered `allow`.
func TestIntegration_GitResetHardCompound_EmitsEmptyObject(t *testing.T) {
	cmd := "git reset --hard && echo ok"
	input := fmt.Sprintf(`{"tool_name":"Bash","tool_input":{"command":%q},"cwd":"/tmp"}`, cmd)
	assertEmptyHookOutput(t, cmd, runHook(t, input))
}

// TestIntegration_GitResetRedirectedContext_StillAsks is the other half of the
// reorder pg2-ur9zc made inside the reset arm. The redirect test now precedes the
// `--hard` test, so a GIT_DIR-redirected reset keeps its always-prompting `ask` for
// BOTH spellings. Asserted at the boundary rather than on Decision alone because the
// defect it guards against is a verdict INVERSION — the hard form emitting the
// weaker `{}` while the soft form emitted `ask` — and only the emitted output shows
// the two side by side.
func TestIntegration_GitResetRedirectedContext_StillAsks(t *testing.T) {
	for _, cmd := range []string{
		"GIT_DIR=/other git reset --hard HEAD~1",
		"GIT_DIR=/other git reset --soft HEAD~1",
	} {
		input := fmt.Sprintf(`{"tool_name":"Bash","tool_input":{"command":%q},"cwd":"/tmp"}`, cmd)
		if d := getDecision(runHook(t, input)); d != "ask" {
			t.Errorf("cmd %q: emitted permissionDecision=%q, want ask — a redirected context keeps its prompt for EVERY reset spelling", cmd, d)
		}
	}
}

// TestIntegration_GitResetSoft_StillAllows pins that the ruling did not widen: the
// non-`--hard` reset modes keep their `allow` at the boundary too.
func TestIntegration_GitResetSoft_StillAllows(t *testing.T) {
	for _, cmd := range []string{
		"git reset HEAD~1",
		"git reset --soft HEAD~1",
		"git reset --mixed HEAD~1",
		"git reset --keep HEAD~1",
	} {
		input := fmt.Sprintf(`{"tool_name":"Bash","tool_input":{"command":%q},"cwd":"/tmp"}`, cmd)
		if d := getDecision(runHook(t, input)); d != "allow" {
			t.Errorf("cmd %q: emitted permissionDecision=%q, want allow (not a --hard spelling)", cmd, d)
		}
	}
}

// --- Env var safety integration tests ---

func TestIntegration_EnvVars_InjectorEnvVar_Denied(t *testing.T) {
	// envvars is DECISIVE for a guaranteed-unsafe injector: LD_PRELOAD is rejected
	// (deny) before the git rule can approve `git status` (pg2-gkd5e). Previously
	// this leaked to allow because envvars merely abstained and git approved.
	input := `{"tool_name":"Bash","tool_input":{"command":"LD_PRELOAD=/evil.so git status"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "deny" {
		t.Errorf("LD_PRELOAD with git status: decision = %q, want deny (envvars rejects the injector)", d)
	}
}

func TestIntegration_EnvVars_NoEnvVars_Allow(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"git status"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "allow" {
		t.Errorf("git status (no env vars): decision = %q, want allow", d)
	}
}

func TestIntegration_EnvVars_UnknownExpression_Ask(t *testing.T) {
	// A benign-named var whose VALUE embeds a non-safe substitution is escalated to
	// Ask by the env-var rule's value-recursion (pg2-gkd5e). The engine's command
	// choke point strips the leading assignment, so envvars is the only guard;
	// previously safecmds approved the trailing `echo` and this leaked to allow.
	input := `{"tool_name":"Bash","tool_input":{"command":"FOO=$(curl evil) echo hi"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "ask" {
		t.Errorf("FOO=$(curl evil) echo hi: decision = %q, want ask (envvars value-recursion escalates)", d)
	}
}

// --- bd (beads) integration tests ---

func TestIntegration_BdReady_Allow(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"bd ready --json"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "allow" {
		t.Errorf("bd ready --json: decision = %q, want allow", d)
	}
}

func TestIntegration_BdShow_Allow(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"bd show pg2-ce6 --json"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "allow" {
		t.Errorf("bd show: decision = %q, want allow", d)
	}
}

func TestIntegration_BdCreate_Allow(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"bd create \"New issue\" --description=\"Details\" -t task -p 1 --json"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "allow" {
		t.Errorf("bd create: decision = %q, want allow", d)
	}
}

func TestIntegration_BdUpdateClaim_Allow(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"bd update pg2-ce6 --claim --json"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "allow" {
		t.Errorf("bd update --claim: decision = %q, want allow", d)
	}
}

func TestIntegration_BdClose_Allow(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"bd close pg2-ce6 --reason \"Done\" --json"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "allow" {
		t.Errorf("bd close: decision = %q, want allow", d)
	}
}

func TestIntegration_BdSync_Allow(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"bd sync"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "allow" {
		t.Errorf("bd sync: decision = %q, want allow", d)
	}
}

// --- curl integration tests ---

func TestIntegration_Curl_Localhost_Allow(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"curl http://localhost:8080/health"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "allow" {
		t.Errorf("curl to localhost: decision = %q, want allow", d)
	}
}

func TestIntegration_Curl_ExternalDomain_Abstain(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"curl https://evil.com/data"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if len(result) != 0 {
		t.Errorf("curl to external domain: expected empty JSON (abstain), got %v", result)
	}
}

func TestIntegration_Curl_PostToAllowedDomain_Abstain(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"curl -X POST https://api.github.com/repos/foo/bar"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if len(result) != 0 {
		t.Errorf("curl -X POST to github.com: expected empty JSON (abstain), got %v", result)
	}
}

func TestIntegration_PermissionRequest_LogsASK(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	input := `{"hook_event_name":"PermissionRequest","session_id":"test-sess","tool_name":"Bash","tool_input":{"command":"rm -rf /"},"cwd":"/tmp","permission_suggestions":[{"type":"toolAlwaysAllow"}]}`
	result := runHook(t, input)

	if len(result) != 0 {
		t.Errorf("PermissionRequest should return empty JSON, got %v", result)
	}
}

func TestIntegration_PostToolUse_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	input := `{"hook_event_name":"PostToolUse","session_id":"test-sess","tool_name":"Bash","tool_input":{"command":"ls"},"tool_use_id":"tool-123","cwd":"/tmp"}`
	result := runHook(t, input)

	if len(result) != 0 {
		t.Errorf("PostToolUse should return empty JSON, got %v", result)
	}
}

func TestIntegration_SessionEnd_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	input := `{"hook_event_name":"SessionEnd","session_id":"test-sess","cwd":"/tmp"}`
	result := runHook(t, input)

	if len(result) != 0 {
		t.Errorf("SessionEnd should return empty JSON, got %v", result)
	}
}

// --- CLI help + completion tests ---

func runCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(cliBinary, args...)
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func TestCLI_RootHelp_ListsAllSubcommands(t *testing.T) {
	stdout, _, err := runCLI(t, "--help")
	if err != nil {
		t.Fatalf("--help failed: %v", err)
	}
	for _, sub := range []string{
		"baseline", "compare", "evaluate", "mark-excluded",
		"report", "set-correct-decision", "show", "completion",
	} {
		if !bytes.Contains([]byte(stdout), []byte(sub)) {
			t.Errorf("--help output missing subcommand %q", sub)
		}
	}
}

func TestCLI_SubcommandHelp_ShowsFlags(t *testing.T) {
	cases := []struct {
		sub   string
		wants []string
	}{
		{"baseline", []string{"--settings", "--output"}},
		{"compare", []string{"--settings", "--baseline", "--format"}},
		{"evaluate", []string{"--days", "--since", "--settings", "--format", "--misses-only", "--approval-source"}},
		{"mark-excluded", []string{"--reason"}},
		{"report", []string{"--group-by", "--misses-only", "--format", "--days", "--since"}},
		{"set-correct-decision", []string{"--decision", "--explanation"}},
		{"show", []string{"--format"}},
	}
	for _, tc := range cases {
		t.Run(tc.sub, func(t *testing.T) {
			stdout, _, err := runCLI(t, tc.sub, "--help")
			if err != nil {
				t.Fatalf("%s --help failed: %v", tc.sub, err)
			}
			for _, flag := range tc.wants {
				if !bytes.Contains([]byte(stdout), []byte(flag)) {
					t.Errorf("%s --help missing flag %q\nout: %s", tc.sub, flag, stdout)
				}
			}
		})
	}
}

func TestCLI_Evaluate_JSON_ApprovalSourceAndFilter(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	store, err := asklog.NewStore(asklog.DefaultDBPath())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	// cwd must exist so rows are not classified stale-cwd.
	_, err = store.DB().Exec(`INSERT INTO tool_decisions
		(id, session_id, cwd, tool_name, tool_input_hash, tool_input_json, tool_summary,
		 hook_decision, outcome, created_at,
		 permission_mode, agent_type, outcome_notes, tool_response, prompt_id)
		VALUES
		 (1,'s',?,'Bash','h1','{"command":"ls"}','ls','allow','denied','2026-01-01T00:00:00Z',
		  'auto','Explore','auto_mode_classifier: x','{"is_error":true}',NULL),
		 (2,'s',?,'Bash','h2','{"command":"pwd"}','pwd','allow','approved','2026-01-01T00:00:00Z',
		  'bypassPermissions',NULL,NULL,'{"is_error":false}',NULL)`, dir, dir)
	if err != nil {
		t.Fatalf("seed rows: %v", err)
	}
	_ = store.Close()

	rowByID := func(out string) map[float64]map[string]any {
		t.Helper()
		var arr []map[string]any
		if err := json.Unmarshal([]byte(out), &arr); err != nil {
			t.Fatalf("unmarshal evaluate json: %v\nraw: %s", err, out)
		}
		m := map[float64]map[string]any{}
		for _, r := range arr {
			m[r["id"].(float64)] = r
		}
		return m
	}

	// Full JSON: both rows, each carrying approval_source + the four raw fields.
	stdout, stderr, err := runCLI(t, "evaluate", "--format=json")
	if err != nil {
		t.Fatalf("evaluate --format=json: %v\nstderr: %s", err, stderr)
	}
	all := rowByID(stdout)
	if len(all) != 2 {
		t.Fatalf("evaluate json rows = %d, want 2\n%s", len(all), stdout)
	}
	r1 := all[1]
	if r1["approval_source"] != "auto" {
		t.Errorf("row1 approval_source = %v, want auto (denied row still bucketed)", r1["approval_source"])
	}
	if r1["permission_mode"] != "auto" {
		t.Errorf("row1 permission_mode = %v, want auto", r1["permission_mode"])
	}
	if r1["agent_type"] != "Explore" {
		t.Errorf("row1 agent_type = %v, want Explore", r1["agent_type"])
	}
	if r1["outcome_notes"] != "auto_mode_classifier: x" {
		t.Errorf("row1 outcome_notes = %v", r1["outcome_notes"])
	}
	tr, ok := r1["tool_response"].(map[string]any)
	if !ok || tr["is_error"] != true {
		t.Errorf("row1 tool_response = %v, want a nested object with is_error=true", r1["tool_response"])
	}
	if all[2]["approval_source"] != "bypass" {
		t.Errorf("row2 approval_source = %v, want bypass", all[2]["approval_source"])
	}
	if all[2]["agent_type"] != nil {
		t.Errorf("row2 agent_type = %v, want null (main agent)", all[2]["agent_type"])
	}

	// --approval-source=auto restricts the evaluation to the auto bucket only.
	stdout, stderr, err = runCLI(t, "evaluate", "--format=json", "--approval-source=auto")
	if err != nil {
		t.Fatalf("evaluate --approval-source=auto: %v\nstderr: %s", err, stderr)
	}
	filtered := rowByID(stdout)
	if len(filtered) != 1 {
		t.Fatalf("filtered rows = %d, want 1\n%s", len(filtered), stdout)
	}
	if _, ok := filtered[1]; !ok {
		t.Errorf("filtered output missing the auto row (id 1)\n%s", stdout)
	}
}

func TestCLI_CompletionBash_EmitsScript(t *testing.T) {
	stdout, _, err := runCLI(t, "completion", "bash")
	if err != nil {
		t.Fatalf("completion bash failed: %v", err)
	}
	if !bytes.Contains([]byte(stdout), []byte("bash completion")) {
		t.Errorf("completion bash output does not look like a bash completion script")
	}
}

func TestCLI_CompletionZsh_EmitsScript(t *testing.T) {
	stdout, _, err := runCLI(t, "completion", "zsh")
	if err != nil {
		t.Fatalf("completion zsh failed: %v", err)
	}
	if !bytes.Contains([]byte(stdout), []byte("#compdef claude-extended-tool-approver")) {
		t.Errorf("completion zsh output does not look like a zsh completion script")
	}
}

func TestIntegration_PreToolUse_StillWorks(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	input := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git log --oneline"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "allow" {
		t.Errorf("PreToolUse git log = %q, want allow", d)
	}
}

// TestIntegration_InputProcessor_RewritesBashApprove is the module's only test
// that spawns an input processor through the BUILT binary, so it is the only
// place the SHIPPED 3s exec deadline is still in force. internal/inputproc's fix
// for the same flake widened a package var from TestMain; a package var cannot
// cross a process boundary, so it structurally could not reach here (pg2-iay90,
// discovered from pg2-tl0ry).
//
// DECISION — keep the shipped 3s budget and make this test survive a slow
// sandbox, rather than adding a knob. Recorded here so the next reader does not
// re-derive it:
//   - A production CETA_INPUT_PROCESSOR_TIMEOUT was rejected. The only reason to
//     add it today would be "a test needs it", which is the wrong reason to grow
//     the configurable surface of the binary that decides whether a command may
//     run. A shorter value would silently stop wrapping commands; a longer one
//     would stall every Bash tool call.
//   - A test-only env knob was rejected: at runtime it is indistinguishable from
//     the production one above, only undocumented.
//   - Dropping /bin/sh from this mock was rejected on measurement (pg2-tl0ry,
//     200 spawns each): /bin/sh at 5.0-7.5ms against a re-exec'd Go binary at
//     3.6ms buys ~3ms against a 3000ms blowout. The cause is tail scheduling
//     latency, not mean shell startup.
//
// What closes it instead is in runHook: the binary's stderr is captured so a
// deadline kill is recognisable out here, and the run is retried on that one
// signature. The residual risk is a sandbox that cannot spawn this two-line
// script within 3s on three consecutive tries, which fails naming the
// environment rather than blaming this assertion.
func TestIntegration_InputProcessor_RewritesBashApprove(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	procScript := filepath.Join(dir, "mock-processor")
	if err := os.WriteFile(procScript, []byte("#!/bin/sh\necho \"wrapped $1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CETA_INPUT_PROCESSOR", procScript)

	input := `{"tool_name":"Bash","tool_input":{"command":"git status"},"cwd":"/tmp"}`
	result := runHook(t, input)

	if d := getDecision(result); d != "allow" {
		t.Errorf("decision = %q, want allow", d)
	}
	hso := result["hookSpecificOutput"].(map[string]any)
	ui, ok := hso["updatedInput"].(map[string]any)
	if !ok {
		t.Fatal("updatedInput missing from output")
	}
	if cmd := ui["command"].(string); cmd != "wrapped git status" {
		t.Errorf("updatedInput.command = %q, want %q", cmd, "wrapped git status")
	}
}

// TestIntegration_InputProcessor_DeadlineKillIsVisibleOnStderr is the guard on
// runHook's retry: it proves the REAL binary emits a line inputProcDeadlineKilled
// matches, end to end across the process boundary. Without it, a reworded
// diagnostic in internal/inputproc would leave the detector matching nothing and
// the retry would quietly stop working — the flake would return with no signal
// that the guard had rotted.
//
// It deliberately calls runHookOnce, not runHook, so it observes the kill instead
// of retrying past it. It costs the full shipped deadline (~3s) in wall time,
// because refusing a knob means refusing one here too. It cannot become the next
// flake: sandbox load makes the deadline fire MORE readily, and firing is what it
// asserts.
//
// The mock runs `sleep 30` as an ordinary child, and that too is load-bearing.
// It used to `exec` it (pg2-iay90) because CommandContext killed only the process
// it started, so a FORKED sleep survived holding the inherited stdout pipe and
// Output() blocked until it exited — 30s against a 3s deadline. pg2-15uhy fixed
// that in internal/inputproc by putting the processor in its own process group and
// killing the group, with a WaitDelay backstop, so the fork is now the shape worth
// running here: this is the only place a FORKING processor meets the SHIPPED 3s
// deadline and the real process-group isolation, since internal/inputproc's tests
// install their deadline through a package var that cannot cross a process
// boundary.
//
// processorOverheadBound is therefore an assertion, not a comment: it is what the
// fix buys end to end. Measured 3.02-3.06s over five runs, against a ceiling of
// the 3s deadline plus a 250ms grace plus a spawn; the defect returned at the
// mock's full 30s. 15s sits ~4x above the former and 2x below the latter, so
// neither sandbox load nor a fast machine decides the verdict.
//
// WHAT IS MEASURED IS A DIFFERENCE, NOT THE HOOK'S TOTAL WALL CLOCK — and that
// distinction is the whole reason this test stopped being trustworthy (tc-fqu7).
// The bound was previously applied to the raw elapsed time of one hook run, which
// also contains the ask log's durable writes. fsync latency is a HOST property
// spanning orders of magnitude (1.1-3.6s per fsync on the loaded QEMU VM that
// builds monorepod, ~0.8us on tmpfs; see synchronousPragma in
// internal/asklog/store.go), and a hook run performs several. On 2026-08-12 that
// put a perfectly healthy run at 16.21s and failed the build: the 3s deadline had
// worked exactly as designed and ~13s of disk had been charged to its account.
//
// So the run is measured against a CONTROL run of the same binary, same input and
// same ask-log work with NO processor configured. Everything host-dependent is
// present in both and cancels; what remains is the processor's marginal cost,
// which is precisely what the 3s deadline governs. The bound itself is UNCHANGED
// at 15s — this is not a third widening of a bound that has been widened twice
// (pg2-tl0ry, pg2-iay90). The number is the same; it is finally applied to the
// quantity it was always describing.
//
// The warm-up run before the control exists so both measured runs open an
// ALREADY-CREATED database. Without it the control would pay schema creation that
// the subject does not, understating the difference and biasing the test toward
// passing — a false negative is worse here than a false positive, because the
// defect this guards degrades every gated Bash tool call.
//
// It also pins the consequence that makes the 3s budget worth caring about: a
// killed processor degrades to no rewrite, so the ORIGINAL command is what
// Claude Code is told to run.
func TestIntegration_InputProcessor_DeadlineKillIsVisibleOnStderr(t *testing.T) {
	const processorOverheadBound = 15 * time.Second

	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	// Pinned empty rather than assumed absent: an ambient CETA_INPUT_PROCESSOR
	// inherited from the developer's shell would run a REAL processor during the
	// control, inflating it and shrinking the difference — i.e. biasing the test
	// toward passing, the one direction that must not happen silently. Empty is
	// how "not configured" is spelled here (TestIntegration_InputProcessor_NotConfigured).
	t.Setenv("CETA_INPUT_PROCESSOR", "")

	input := `{"tool_name":"Bash","tool_input":{"command":"git status"},"cwd":"/tmp"}`

	// Warm-up: creates the ask-log schema so neither measured run pays for it.
	runHookOnce(t, input)

	// Control: identical work MINUS the input processor.
	controlStart := time.Now()
	runHookOnce(t, input)
	control := time.Since(controlStart)

	procScript := filepath.Join(dir, "mock-processor")
	if err := os.WriteFile(procScript, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CETA_INPUT_PROCESSOR", procScript)

	start := time.Now()
	result, stderr := runHookOnce(t, input)
	elapsed := time.Since(start)

	if overhead := elapsed - control; overhead > processorOverheadBound {
		t.Errorf("the input processor added %v to the hook (run %v, control %v), want under %v: the shipped deadline killed the input processor but a process it forked kept the output pipe open, so every gated Bash tool call can outlast the budget\nstderr: %s", overhead, elapsed, control, processorOverheadBound, stderr)
	}
	if !inputProcDeadlineKilled(stderr) {
		t.Fatalf("stderr does not report a deadline kill, so runHook's retry can no longer recognise one: reconcile inputProcDeadlineKilled with internal/inputproc's diagnostic\nstderr: %s", stderr)
	}
	if d := getDecision(result); d != "allow" {
		t.Errorf("decision = %q, want allow: a killed processor must not change the decision", d)
	}
	hso, ok := result["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("hookSpecificOutput missing from output: %v", result)
	}
	if _, ok := hso["updatedInput"]; ok {
		t.Error("updatedInput should not be present when the processor was killed before it could rewrite")
	}
}

func TestIntegration_InputProcessor_SkipsNonBash(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	procScript := filepath.Join(dir, "mock-processor")
	if err := os.WriteFile(procScript, []byte("#!/bin/sh\necho \"wrapped $1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CETA_INPUT_PROCESSOR", procScript)

	input := `{"tool_name":"AskQuestion","tool_input":{},"cwd":"/tmp"}`
	result := runHook(t, input)

	if d := getDecision(result); d != "allow" {
		t.Errorf("decision = %q, want allow", d)
	}
	hso := result["hookSpecificOutput"].(map[string]any)
	if _, ok := hso["updatedInput"]; ok {
		t.Error("updatedInput should not be present for non-Bash tool")
	}
}

func TestIntegration_InputProcessor_SkipsDeny(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	procScript := filepath.Join(dir, "mock-processor")
	if err := os.WriteFile(procScript, []byte("#!/bin/sh\necho \"wrapped $1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CETA_INPUT_PROCESSOR", procScript)

	// nix rule rejects darwin-rebuild switch
	input := `{"tool_name":"Bash","tool_input":{"command":"darwin-rebuild switch"},"cwd":"/tmp"}`
	result := runHook(t, input)

	d := getDecision(result)
	if d == "" {
		// Abstain — no hookSpecificOutput, so no updatedInput possible
		return
	}
	hso := result["hookSpecificOutput"].(map[string]any)
	if _, ok := hso["updatedInput"]; ok {
		t.Error("updatedInput should not be present for denied/abstained command")
	}
}

func TestIntegration_InputProcessor_NotConfigured(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv("CETA_INPUT_PROCESSOR", "")

	input := `{"tool_name":"Bash","tool_input":{"command":"git status"},"cwd":"/tmp"}`
	result := runHook(t, input)

	if d := getDecision(result); d != "allow" {
		t.Errorf("decision = %q, want allow", d)
	}
	hso := result["hookSpecificOutput"].(map[string]any)
	if _, ok := hso["updatedInput"]; ok {
		t.Error("updatedInput should not be present when processor is not configured")
	}
}
