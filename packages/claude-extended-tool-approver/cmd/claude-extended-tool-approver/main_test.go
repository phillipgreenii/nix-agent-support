package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
	f.Close()
	cliBinary = f.Name()

	build := exec.Command("go", "build", "-o", cliBinary, "./cmd/claude-extended-tool-approver")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build failed: %v\n%s\n", err, out)
		os.Remove(cliBinary)
		os.Exit(1)
	}

	code := m.Run()
	os.Remove(cliBinary)
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

func TestIntegration_EnvVars_SafeEnvOnApprovedCommand_Allow(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"PYTHONPATH=/foo bin/pyzr run test"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "allow" {
		t.Errorf("PYTHONPATH bin/pyzr run test: decision = %q, want allow", d)
	}
}

func TestIntegration_EnvVars_WrapperDangerousEnv_Abstain(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"PYTHONSTARTUP=/evil.py bin/pyzr run"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if len(result) != 0 {
		t.Errorf("PYTHONSTARTUP with bin/pyzr: expected empty JSON (abstain), got %v", result)
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

func TestIntegration_Curl_ZrOrg_Allow(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"curl https://captains-log.zr.org/api/v1/builds/foo"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "allow" {
		t.Errorf("curl to zr.org: decision = %q, want allow", d)
	}
}

func TestIntegration_Curl_ZiprecruiterCom_Allow(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"curl https://api.ziprecruiter.com/path"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "allow" {
		t.Errorf("curl to ziprecruiter.com: decision = %q, want allow", d)
	}
}

func TestIntegration_CloudflaredAccessCurl_ZrOrg_Allow(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"cloudflared access curl \"https://captains-log.zr.org/api/v1/builds/foo\" 2>/dev/null | jq '.'"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if d := getDecision(result); d != "allow" {
		t.Errorf("cloudflared access curl to zr.org: decision = %q, want allow", d)
	}
}

func TestIntegration_Curl_ExternalDomain_Abstain(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"curl https://evil.com/data"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if len(result) != 0 {
		t.Errorf("curl to external domain: expected empty JSON (abstain), got %v", result)
	}
}

func TestIntegration_Curl_PostToZrOrg_Abstain(t *testing.T) {
	input := `{"tool_name":"Bash","tool_input":{"command":"curl -X POST https://captains-log.zr.org/api"},"cwd":"/tmp"}`
	result := runHook(t, input)
	if len(result) != 0 {
		t.Errorf("curl -X POST to zr.org: expected empty JSON (abstain), got %v", result)
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
		{"evaluate", []string{"--days", "--since", "--settings", "--format", "--misses-only"}},
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

	// znself rule rejects zn-self-apply/zn-self-upgrade
	input := `{"tool_name":"Bash","tool_input":{"command":"zn-self-apply"},"cwd":"/tmp"}`
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
