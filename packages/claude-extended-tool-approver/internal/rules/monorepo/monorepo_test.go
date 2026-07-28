package monorepo

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestMonorepo_Unknown_Abstain(t *testing.T) {
	pe := patheval.NewWithCWD("/home/user/monorepo", "/home/user/monorepo")
	r := New(pe, configrules.MonorepoConfig{ApprovedCommands: []string{"tc"}})
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:       "/home/user/monorepo",
		ToolInput: mustJSON(map[string]string{"command": "ls -la"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("ls -la: got %s, want abstain", got.Decision)
	}
}

func TestMonorepo_NonBash_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/monorepo")
	r := New(pe, configrules.MonorepoConfig{})
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": "/tmp/x"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("Read: got %s, want abstain", got.Decision)
	}
}

func TestMonorepo_Name(t *testing.T) {
	pe := patheval.New("/home/user/monorepo")
	r := New(pe, configrules.MonorepoConfig{})
	if got := r.Name(); got != "monorepo" {
		t.Errorf("Name() = %q, want monorepo", got)
	}
}

// TestMonorepo_EmptyConfigAbstains proves an unconfigured monorepo rule defers
// on a command that a configured rule would approve (safe base default).
func TestMonorepo_EmptyConfigAbstains(t *testing.T) {
	pe := patheval.NewWithCWD("/home/user/monorepo", "/home/user/monorepo")
	r := New(pe, configrules.MonorepoConfig{})
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:       "/home/user/monorepo",
		ToolInput: mustJSON(map[string]string{"command": "tc build"}),
	}
	if got := r.Evaluate(input); got.Decision != hookio.Abstain {
		t.Errorf("empty config `tc build`: got %s, want abstain", got.Decision)
	}
}

// TestMonorepo_ConfiguredApprove proves an approved command is approved, and the
// per-wrapper dangerous-env-var deferral withholds approval.
func TestMonorepo_ConfiguredApprove(t *testing.T) {
	pe := patheval.NewWithCWD("/home/user/monorepo", "/home/user/monorepo")
	r := New(pe, configrules.MonorepoConfig{
		ApprovedCommands:      []string{"tc", "uv"},
		DangerousEnvByWrapper: map[string][]string{"tc": {"TC_DANGER"}},
	})
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"approved command", "tc build", hookio.Approve},
		{"second approved command", "uv sync", hookio.Approve},
		{"approved with dangerous env defers", "TC_DANGER=1 tc build", hookio.Abstain},
		{"approved with benign env approves", "FOO=1 tc build", hookio.Approve},
		{"unapproved command abstains", "make all", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				CWD:       "/home/user/monorepo",
				ToolInput: mustJSON(map[string]string{"command": tt.command}),
			}
			if got := r.Evaluate(input); got.Decision != tt.want {
				t.Errorf("%q: got %s, want %s", tt.command, got.Decision, tt.want)
			}
		})
	}
}
