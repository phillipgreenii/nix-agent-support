package znself

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestZnSelf_Build_Approve(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "zn-self-build"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("zn-self-build: got %s, want approve", got.Decision)
	}
}

func TestZnSelf_Check_Approve(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "zn-self-check"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("zn-self-check: got %s, want approve", got.Decision)
	}
}

func TestZnSelf_Apply_Reject(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "zn-self-apply"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Reject {
		t.Errorf("zn-self-apply: got %s, want reject", got.Decision)
	}
}

func TestZnSelf_Upgrade_Reject(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "zn-self-upgrade"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Reject {
		t.Errorf("zn-self-upgrade: got %s, want reject", got.Decision)
	}
}

func TestZnSelf_NonZnSelf_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "ls -la"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("ls: got %s, want abstain", got.Decision)
	}
}

func TestZnSelf_NonBash_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": "/tmp/x"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("Read tool: got %s, want abstain", got.Decision)
	}
}

func TestZnSelf_Name(t *testing.T) {
	r := New()
	if got := r.Name(); got != "znself" {
		t.Errorf("Name() = %q, want znself", got)
	}
}
