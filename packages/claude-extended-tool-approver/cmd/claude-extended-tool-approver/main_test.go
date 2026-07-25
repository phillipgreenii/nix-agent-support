package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

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

	code := m.Run()
	_ = os.Remove(cliBinary)
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

func runHook(t *testing.T, input string) map[string]any {
	t.Helper()
	cmd := exec.Command(cliBinary)
	cmd.Env = os.Environ()
	cmd.Stdin = bytes.NewBufferString(input)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("hook failed: %v\nstderr: %s", err, ee.Stderr)
		}
		t.Fatalf("hook failed: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out), &result); err != nil {
		t.Fatalf("invalid JSON output: %v\nraw: %s", err, out)
	}
	return result
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

func TestIntegration_GitResetHard(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"git reset --hard HEAD~1"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "ask" {
		t.Errorf("git reset --hard decision = %q, want ask", d)
	}
}

// --- Env var safety integration tests ---

func TestIntegration_EnvVars_DangerousEnvVar_DeferredAllow(t *testing.T) {
	// envvars rule abstains (deferred to claude-code), git rule approves git status as read-only
	input := `{"tool_name":"Bash","tool_input":{"command":"LD_PRELOAD=/evil.so git status"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "allow" {
		t.Errorf("LD_PRELOAD with git status: decision = %q, want allow (envvars defers, git approves)", d)
	}
}

func TestIntegration_EnvVars_NoEnvVars_Allow(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"git status"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "allow" {
		t.Errorf("git status (no env vars): decision = %q, want allow", d)
	}
}

func TestIntegration_EnvVars_UnknownExpression_DeferredAllow(t *testing.T) {
	// envvars rule abstains (deferred to claude-code), safecmds rule approves echo as always-safe
	input := `{"tool_name":"Bash","tool_input":{"command":"FOO=$(curl evil) echo hi"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "allow" {
		t.Errorf("FOO=$(curl evil) echo hi: decision = %q, want allow (envvars defers, safecmds approves)", d)
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
