package asklog

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRegisterAndLookupShell(t *testing.T) {
	s := newTestStore(t)

	if _, known := s.ShellOwner("missing"); known {
		t.Fatal("unknown shell reported as known")
	}

	if err := s.RegisterBackgroundShell("shell-1", "sess-1"); err != nil {
		t.Fatalf("RegisterBackgroundShell: %v", err)
	}
	owner, known := s.ShellOwner("shell-1")
	if !known || owner != "agent" {
		t.Fatalf("ShellOwner = (%q, %v), want (\"agent\", true)", owner, known)
	}

	// Upsert on the same id must not error.
	if err := s.RegisterBackgroundShell("shell-1", "sess-2"); err != nil {
		t.Fatalf("re-register: %v", err)
	}
}

func bashInput(cmd string, background bool) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"command": cmd, "run_in_background": background})
	return b
}

func TestRegisterBackgroundShellFromPost(t *testing.T) {
	s := newTestStore(t)

	tests := []struct {
		name     string
		toolName string
		input    json.RawMessage
		response json.RawMessage
		shellID  string // expected tracked id, "" means none tracked
	}{
		{
			name:     "background bash with shell_id tracked",
			toolName: "Bash",
			input:    bashInput("sleep 100", true),
			response: json.RawMessage(`{"shell_id":"bg-1"}`),
			shellID:  "bg-1",
		},
		{
			name:     "foreground bash not tracked",
			toolName: "Bash",
			input:    bashInput("ls", false),
			response: json.RawMessage(`{"shell_id":"bg-2"}`),
			shellID:  "",
		},
		{
			name:     "background bash without shell_id not tracked",
			toolName: "Bash",
			input:    bashInput("sleep 100", true),
			response: json.RawMessage(`{}`),
			shellID:  "",
		},
		{
			name:     "non-bash tool ignored",
			toolName: "Read",
			input:    json.RawMessage(`{"file_path":"x"}`),
			response: json.RawMessage(`{"shell_id":"bg-3"}`),
			shellID:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:     tt.toolName,
				ToolInput:    tt.input,
				ToolResponse: tt.response,
				SessionID:    "sess",
			}
			if err := RegisterBackgroundShellFromPost(s, input); err != nil {
				t.Fatalf("RegisterBackgroundShellFromPost: %v", err)
			}
			if tt.shellID != "" {
				if _, known := s.ShellOwner(tt.shellID); !known {
					t.Errorf("expected %q tracked", tt.shellID)
				}
			}
		})
	}
}
