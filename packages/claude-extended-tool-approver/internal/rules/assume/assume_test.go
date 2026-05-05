package assume

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

func TestAssumeRule(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
		tool    string
		want    hookio.Decision
	}{
		{"bare assume", "assume", "Bash", hookio.Reject},
		{"assume with args", "assume my-role", "Bash", hookio.Reject},
		{"full path assume", "/usr/local/bin/assume", "Bash", hookio.Reject},
		{"not assume", "ls -la", "Bash", hookio.Abstain},
		{"assume in arg", "echo assume", "Bash", hookio.Abstain},
		{"non-bash tool", "", "Read", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: tt.tool, ToolInput: mustJSON(tt.command)}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v", got.Decision, tt.want)
			}
		})
	}
}
