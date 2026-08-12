package killshell

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// fakeStore is an in-memory ShellStore for tests.
type fakeStore map[string]string

func (f fakeStore) ShellOwner(shellID string) (string, bool) {
	owner, ok := f[shellID]
	return owner, ok
}

func killShellJSON(shellID string) json.RawMessage {
	if shellID == "" {
		return json.RawMessage(`{}`)
	}
	b, _ := json.Marshal(map[string]string{"shell_id": shellID})
	return b
}

func TestKillShell(t *testing.T) {
	tests := []struct {
		name    string
		tool    string
		shellID string
		store   ShellStore
		want    hookio.Decision
	}{
		{"agent-owned shell approved", "KillShell", "shell-1", fakeStore{"shell-1": "agent"}, hookio.Approve},
		{"untracked shell asks", "KillShell", "shell-2", fakeStore{"shell-1": "agent"}, hookio.Ask},
		{"missing shell_id asks", "KillShell", "", fakeStore{}, hookio.Ask},
		{"nil store fails secure (ask)", "KillShell", "shell-1", nil, hookio.Ask},
		{"non-agent owner asks", "KillShell", "shell-3", fakeStore{"shell-3": "user"}, hookio.Ask},
		{"non-killshell tool abstains", "Bash", "shell-1", fakeStore{"shell-1": "agent"}, hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New(tt.store)
			input := &hookio.HookInput{ToolName: tt.tool, ToolInput: killShellJSON(tt.shellID)}
			if got := hookio.Verdict(r.Evaluate(input)).Decision; got != tt.want {
				t.Errorf("Decision = %v, want %v", got, tt.want)
			}
		})
	}
}
