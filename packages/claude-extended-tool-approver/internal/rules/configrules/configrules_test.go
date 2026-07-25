package configrules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func writeConfig(t *testing.T, dir string, cfg Config) {
	t.Helper()
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(dir, "rules.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestConfigRules_ApprovedCommand(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, Config{ApprovedCommands: []string{"mytool", "mytool2"}})
	r := NewFromFile(filepath.Join(dir, "rules.json"))
	got := r.Evaluate(&hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "mytool test ./..."}),
	})
	if got.Decision != hookio.Approve {
		t.Errorf("mytool: got %s, want approve", got.Decision)
	}
}

func TestConfigRules_BlockedCommand(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, Config{BlockedCommands: []string{"my-self-apply", "my-self-upgrade"}})
	r := NewFromFile(filepath.Join(dir, "rules.json"))
	got := r.Evaluate(&hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "my-self-apply"}),
	})
	if got.Decision != hookio.Reject {
		t.Errorf("my-self-apply: got %s, want reject", got.Decision)
	}
}

func TestConfigRules_ApprovedCommandWithEnvVars_Abstains(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, Config{ApprovedCommands: []string{"mytool", "pytool"}})
	r := NewFromFile(filepath.Join(dir, "rules.json"))
	got := r.Evaluate(&hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "PYTHONSTARTUP=/evil.py bin/pytool run"}),
	})
	if got.Decision != hookio.Abstain {
		t.Errorf("pytool with env var: got %s, want abstain", got.Decision)
	}
}

func TestConfigRules_AbstainForUnknown(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, Config{ApprovedCommands: []string{"mytool"}, BlockedCommands: []string{"my-self-apply"}})
	r := NewFromFile(filepath.Join(dir, "rules.json"))
	got := r.Evaluate(&hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "git status"}),
	})
	if got.Decision != hookio.Abstain {
		t.Errorf("git status: got %s, want abstain", got.Decision)
	}
}

func TestConfigRules_AbstainWhenFileAbsent(t *testing.T) {
	r := NewFromFile("/nonexistent/path/rules.json")
	got := r.Evaluate(&hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "mytool test ./..."}),
	})
	if got.Decision != hookio.Abstain {
		t.Errorf("missing file: got %s, want abstain", got.Decision)
	}
}

func TestConfigRules_NonBashAbstains(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, Config{ApprovedCommands: []string{"mytool"}})
	r := NewFromFile(filepath.Join(dir, "rules.json"))
	got := r.Evaluate(&hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"path": "/foo"}),
	})
	if got.Decision != hookio.Abstain {
		t.Errorf("non-bash: got %s, want abstain", got.Decision)
	}
}

// TestLoad_ParsesKubectlBuildtoolsBlocks proves the loader parses the structured
// kubectl{}/buildtools{} sub-configs from the ZR fixture — the schema the kubectl
// and build-tools rules consume via DI (ADR 0033).
func TestLoad_ParsesKubectlBuildtoolsBlocks(t *testing.T) {
	cfg := Load("testdata/zr-rules.json")

	k := cfg.Kubectl
	if got := k.ExecutableAliases; len(got) != 1 || got[0] != "kc" {
		t.Errorf("ExecutableAliases = %v, want [kc]", got)
	}
	if len(k.ReadOnlyVerbs) != 3 {
		t.Errorf("ReadOnlyVerbs = %v, want 3 (wslogs/zrlog/wsfirstpod)", k.ReadOnlyVerbs)
	}
	if len(k.ExecVerbs) != 3 {
		t.Errorf("ExecVerbs = %v, want 3 (exe/shell/wsexec)", k.ExecVerbs)
	}
	if len(k.ScopedApproveVerbs) != 3 || len(k.PositionalWorkspaceVerbs) != 2 {
		t.Errorf("scoped=%v positional=%v, want 3 and 2", k.ScopedApproveVerbs, k.PositionalWorkspaceVerbs)
	}
	if k.ClusterEnvVar != "KC_CLUSTER" || k.DevWorkspacePrefix != "d-" {
		t.Errorf("clusterEnvVar=%q devWorkspacePrefix=%q, want KC_CLUSTER and d-", k.ClusterEnvVar, k.DevWorkspacePrefix)
	}
	if len(k.DevClusterPrefixes) != 2 || len(k.DevWorkspaceFlags) != 2 || len(k.NonDevAccounts) != 7 {
		t.Errorf("clusterPfx=%v wsFlags=%v nonDev=%v; want 2/2/7", k.DevClusterPrefixes, k.DevWorkspaceFlags, k.NonDevAccounts)
	}

	b := cfg.Buildtools
	if len(b.ApprovedTools) != 2 {
		t.Errorf("buildtools.ApprovedTools = %v, want 2 (prove/yath)", b.ApprovedTools)
	}
	if len(b.ApprovedScripts) != 5 {
		t.Errorf("buildtools.ApprovedScripts = %v, want 5", b.ApprovedScripts)
	}

	// The 5 migrated scripts MUST NOT remain in the flat approvedCommands.
	for _, cmd := range cfg.ApprovedCommands {
		switch cmd {
		case "zr-proto-regenerate.sh", "pre-merge-protobuf-check", "fix-ai-tools-ownership", "pre-merge-py-check", "generate-build-deps":
			t.Errorf("migrated script %q must be removed from flat approvedCommands", cmd)
		}
	}
}

// TestLoad_ParsesVerbScopedApprovals proves the verbScopedApprovals schema parses.
func TestLoad_ParsesVerbScopedApprovals(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.json")
	body := `{"buildtools":{"verbScopedApprovals":[{"tool":"mytool","verb":"check"}]}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Load(path)
	if len(cfg.Buildtools.VerbScopedApprovals) != 1 ||
		cfg.Buildtools.VerbScopedApprovals[0].Tool != "mytool" ||
		cfg.Buildtools.VerbScopedApprovals[0].Verb != "check" {
		t.Errorf("VerbScopedApprovals = %+v, want [{mytool check}]", cfg.Buildtools.VerbScopedApprovals)
	}
}

// TestLoad_AbsentAndMalformed returns a zero Config (base behavior) safely.
func TestLoad_AbsentAndMalformed(t *testing.T) {
	if cfg := Load("/nonexistent/rules.json"); cfg == nil || len(cfg.Kubectl.ExecutableAliases) != 0 {
		t.Errorf("absent file: want zero Config, got %+v", cfg)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg := Load(path); cfg == nil || len(cfg.ApprovedCommands) != 0 {
		t.Errorf("malformed file: want zero Config, got %+v", cfg)
	}
}
