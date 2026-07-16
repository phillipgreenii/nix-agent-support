package secrets

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

func bashInput(cmd string) *hookio.HookInput {
	ti, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return &hookio.HookInput{ToolName: "Bash", ToolInput: ti}
}

func fileInput(tool, path string) *hookio.HookInput {
	ti, _ := json.Marshal(hookio.FileToolInput{FilePath: path})
	return &hookio.HookInput{ToolName: tool, ToolInput: ti}
}

func searchInput(tool, pattern, path string) *hookio.HookInput {
	ti, _ := json.Marshal(hookio.SearchToolInput{Pattern: pattern, Path: path})
	return &hookio.HookInput{ToolName: tool, ToolInput: ti}
}

func TestRule(t *testing.T) {
	// No sandbox deny config → a secret path is Ask (not Reject).
	r := New(patheval.NewWithCWD("/project", "/project"))
	tests := []struct {
		name  string
		input *hookio.HookInput
		want  hookio.Decision
	}{
		// Bash reads of secrets → Ask
		{"cat claude credentials", bashInput("cat ~/.claude/.credentials"), hookio.Ask},
		{"cat linux credentials json", bashInput("cat ~/.claude/.credentials.json"), hookio.Ask},
		{"cat bare dotenv", bashInput("cat .env"), hookio.Ask},
		{"cat secrets json", bashInput("cat secrets/svc/prod.json"), hookio.Ask},
		{"head ssh config", bashInput("head -n 5 ~/.ssh/config"), hookio.Ask},
		// grep whose FILE arg is a secret (pattern is not) → Ask
		{"grep into ssh config", bashInput("grep Host ~/.ssh/config"), hookio.Ask},
		// stdin redirect read of a secret must not bypass the check
		{"cat stdin-redirect from secrets", bashInput("cat < secrets/prod.json"), hookio.Ask},
		// sh/bash -c '<inner>' must not bypass the check
		{"bash -c cat dotenv", bashInput("bash -c 'cat .env'"), hookio.Ask},
		{"sh -c cat credentials", bashInput("sh -c \"cat ~/.claude/.credentials\""), hookio.Ask},

		// Bash without a secret path → Abstain (defer to rest of chain)
		{"cat readme", bashInput("cat README.md"), hookio.Abstain},
		{"echo hello", bashInput("echo hello"), hookio.Abstain},
		{"ls tmp", bashInput("ls /tmp"), hookio.Abstain},
		// bare "secrets" word (kubectl subcommand) must NOT be flagged
		{"kubectl get secrets not flagged", bashInput("kubectl get secrets"), hookio.Abstain},

		// File tools
		{"Read credentials", fileInput("Read", "~/.claude/.credentials"), hookio.Ask},
		{"Read normal file", fileInput("Read", "internal/main.go"), hookio.Abstain},
		{"Write to secret", fileInput("Write", "~/.ssh/authorized_keys"), hookio.Ask},
		{"Edit normal file", fileInput("Edit", "src/app.go"), hookio.Abstain},

		// Search tools
		{"Grep in secrets dir", searchInput("Grep", "password", "secrets/"), hookio.Ask},
		{"Grep normal dir", searchInput("Grep", "TODO", "internal/"), hookio.Abstain},
		{"Glob no path", searchInput("Glob", "**/*.go", ""), hookio.Abstain},

		// Unrelated tools → Abstain
		{"WebFetch", &hookio.HookInput{ToolName: "WebFetch"}, hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.Evaluate(tt.input)
			if got.Decision != tt.want {
				t.Errorf("Evaluate(%s) decision = %v, want %v (reason %q)", tt.name, got.Decision, tt.want, got.Reason)
			}
			if got.Decision != hookio.Abstain && got.Module != r.Name() {
				t.Errorf("Evaluate(%s) module = %q, want %q", tt.name, got.Module, r.Name())
			}
		})
	}
}

// TestRule_DenyListedSecretRejects verifies that when a secret path is ALSO
// deny-listed the rule returns Reject (preserving the hard block path-safety
// would give, since this rule runs before path-safety) rather than downgrading
// it to Ask.
func TestRule_DenyListedSecretRejects(t *testing.T) {
	pe := patheval.NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{
		DenyRead: []string{"/Users/testuser/.ssh"},
	})
	r := New(pe)
	tests := []struct {
		name  string
		input *hookio.HookInput
		want  hookio.Decision
	}{
		{"Read deny-listed ssh key", fileInput("Read", "/Users/testuser/.ssh/id_rsa"), hookio.Reject},
		{"cat deny-listed ssh key", bashInput("cat /Users/testuser/.ssh/id_rsa"), hookio.Reject},
		// A secret NOT under the deny path still only prompts.
		{"Read non-denied secret", fileInput("Read", "/Users/testuser/.claude/.credentials"), hookio.Ask},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Evaluate(tt.input); got.Decision != tt.want {
				t.Errorf("Evaluate(%s) = %v, want %v (reason %q)", tt.name, got.Decision, tt.want, got.Reason)
			}
		})
	}
}
