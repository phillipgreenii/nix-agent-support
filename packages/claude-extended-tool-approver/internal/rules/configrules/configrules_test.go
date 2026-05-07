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
