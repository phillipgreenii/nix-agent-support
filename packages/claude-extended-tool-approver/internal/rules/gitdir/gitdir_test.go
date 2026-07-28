package gitdir

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func bashJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

func fileJSON(path string) json.RawMessage {
	b, _ := json.Marshal(hookio.FileToolInput{FilePath: path})
	return b
}

func searchJSON(path string) json.RawMessage {
	b, _ := json.Marshal(hookio.SearchToolInput{Pattern: "x", Path: path})
	return b
}

func TestGitDir_Bash(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"cat .git config", "cat .git/config", hookio.Reject},
		{"nested .git", "cat repo/.git/HEAD", hookio.Reject},
		{"write into .git", "echo x > .git/hooks/pre-commit", hookio.Reject},
		{"rm .git dir", "rm -rf .git/", hookio.Reject},
		{"trailing /.git", "ls foo/.git", hookio.Reject},
		{"bare git command not blocked", "git status", hookio.Abstain},
		{"git config not blocked", "git config user.name", hookio.Abstain},
		{"gitignore not blocked", "cat .gitignore", hookio.Abstain},
		{"unrelated", "ls -la", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: bashJSON(tt.command)}
			if got := r.Evaluate(input).Decision; got != tt.want {
				t.Errorf("Decision = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGitDir_FileTools(t *testing.T) {
	r := New()
	tests := []struct {
		name string
		tool string
		path string
		want hookio.Decision
	}{
		{"read .git config", "Read", "/repo/.git/config", hookio.Reject},
		{"write .git ref", "Write", ".git/refs/heads/main", hookio.Reject},
		{"edit non-git", "Edit", "/repo/src/main.go", hookio.Abstain},
		{"grep .git", "Grep", "/repo/.git/objects", hookio.Reject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ti json.RawMessage
			if tt.tool == "Grep" {
				ti = searchJSON(tt.path)
			} else {
				ti = fileJSON(tt.path)
			}
			input := &hookio.HookInput{ToolName: tt.tool, ToolInput: ti}
			if got := r.Evaluate(input).Decision; got != tt.want {
				t.Errorf("Decision = %v, want %v", got, tt.want)
			}
		})
	}
}
