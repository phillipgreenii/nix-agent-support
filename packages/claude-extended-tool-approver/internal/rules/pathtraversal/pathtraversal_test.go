package pathtraversal

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

func TestPathTraversal(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
		tool    string
		want    hookio.Decision
	}{
		{"double dot escape", "cat ../../etc/passwd", "Bash", hookio.Ask},
		{"cd escape", "cd ../../..", "Bash", hookio.Ask},
		{"deep escape", "ls ../../../secrets", "Bash", hookio.Ask},
		{"workspace-root navigation", "cd ../.. && ls", "Bash", hookio.Ask},
		{"single level ok", "cat ../README.md", "Bash", hookio.NoOpinion},
		{"no traversal", "ls -la", "Bash", hookio.NoOpinion},
		{"non-bash", "", "Read", hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: tt.tool, ToolInput: mustJSON(tt.command)}
			if got := hookio.Verdict(r.Evaluate(input)).Decision; got != tt.want {
				t.Errorf("Decision = %v, want %v", got, tt.want)
			}
		})
	}
}
