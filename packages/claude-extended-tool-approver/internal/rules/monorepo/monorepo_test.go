package monorepo

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestMonorepo_Unknown_Abstain(t *testing.T) {
	pe := patheval.NewWithCWD("/home/user/monorepo", "/home/user/monorepo")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		CWD:      "/home/user/monorepo",
		ToolInput: mustJSON(map[string]string{"command": "ls -la"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("ls -la: got %s, want abstain", got.Decision)
	}
}

func TestMonorepo_NonBash_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/monorepo")
	r := New(pe)
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
	r := New(pe)
	if got := r.Name(); got != "monorepo" {
		t.Errorf("Name() = %q, want monorepo", got)
	}
}

