package gh

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

type stubResolver struct {
	currentBranch string
	runBranch     string
	currentErr    error
	runErr        error
}

func (s *stubResolver) CurrentBranch(cwd string) (string, error) {
	return s.currentBranch, s.currentErr
}

func (s *stubResolver) RunBranch(runID string) (string, error) {
	return s.runBranch, s.runErr
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestGH_ReadOnly_Approve(t *testing.T) {
	readOnly := []string{
		"gh pr view", "gh pr list", "gh pr status", "gh pr diff", "gh pr checks",
		"gh issue view", "gh issue list", "gh issue status",
		"gh repo view", "gh repo list",
		"gh run view", "gh run list",
		"gh release view", "gh release list",
		"gh search issues",
		"gh status",
		"gh auth status",
		"gh api /repos",
	}
	r := New(nil)
	for _, cmd := range readOnly {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestGH_Modifying_Ask(t *testing.T) {
	modifying := []string{
		"gh pr create",
		"gh issue create",
	}
	r := New(nil)
	for _, cmd := range modifying {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s, want ask", cmd, got.Decision)
		}
	}
}

func TestGH_PrMerge_Ask(t *testing.T) {
	r := New(nil)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "gh pr merge"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Ask {
		t.Errorf("gh pr merge: got %s, want ask", got.Decision)
	}
}

func TestGH_PrMergeAuto_Abstain(t *testing.T) {
	r := New(nil)
	commands := []string{
		"gh pr merge --auto",
		"gh pr merge --squash --auto 1234",
		"gh pr merge --auto --delete-branch",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s, want abstain", cmd, got.Decision)
		}
	}
}

func TestGH_PrMergeAutoMerge_Ask(t *testing.T) {
	// --auto-merge is not a real gh flag, but guard against substring matching
	r := New(nil)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "gh pr merge --auto-merge"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Ask {
		t.Errorf("gh pr merge --auto-merge: got %s, want ask", got.Decision)
	}
}

func TestGH_NonGh_Abstain(t *testing.T) {
	r := New(nil)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "git status"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("git status: got %s, want abstain", got.Decision)
	}
}

func TestGH_NonBash_Abstain(t *testing.T) {
	r := New(nil)
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": "/tmp/x"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("Read: got %s, want abstain", got.Decision)
	}
}

func TestGH_Name(t *testing.T) {
	r := New(nil)
	if got := r.Name(); got != "gh" {
		t.Errorf("Name() = %q, want gh", got)
	}
}

func TestGH_RunRerun(t *testing.T) {
	errFailed := errors.New("simulated failure")

	tests := []struct {
		name     string
		cmd      string
		resolver BranchResolver
		want     hookio.Decision
	}{
		{
			name:     "branches match",
			cmd:      "gh run rerun 12345",
			resolver: &stubResolver{currentBranch: "feature-x", runBranch: "feature-x"},
			want:     hookio.Approve,
		},
		{
			name:     "branches differ",
			cmd:      "gh run rerun 12345",
			resolver: &stubResolver{currentBranch: "feature-x", runBranch: "main"},
			want:     hookio.Abstain,
		},
		{
			name:     "current branch error",
			cmd:      "gh run rerun 12345",
			resolver: &stubResolver{currentErr: errFailed},
			want:     hookio.Abstain,
		},
		{
			name:     "run branch error (timeout)",
			cmd:      "gh run rerun 12345",
			resolver: &stubResolver{currentBranch: "feature-x", runErr: errFailed},
			want:     hookio.Abstain,
		},
		{
			name:     "flags before run ID",
			cmd:      "gh run rerun --failed 12345",
			resolver: &stubResolver{currentBranch: "feature-x", runBranch: "feature-x"},
			want:     hookio.Approve,
		},
		{
			name:     "flags after run ID",
			cmd:      "gh run rerun 12345 --failed",
			resolver: &stubResolver{currentBranch: "feature-x", runBranch: "feature-x"},
			want:     hookio.Approve,
		},
		{
			name:     "no run ID",
			cmd:      "gh run rerun",
			resolver: &stubResolver{currentBranch: "feature-x", runBranch: "feature-x"},
			want:     hookio.Abstain,
		},
		{
			name:     "nil resolver",
			cmd:      "gh run rerun 12345",
			resolver: nil,
			want:     hookio.Abstain,
		},
		{
			name:     "non-numeric run ID",
			cmd:      "gh run rerun not-a-number",
			resolver: &stubResolver{currentBranch: "feature-x", runBranch: "feature-x"},
			want:     hookio.Abstain,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New(tt.resolver)
			input := &hookio.HookInput{
				ToolName:  "Bash",
				ToolInput: mustJSON(map[string]string{"command": tt.cmd}),
				CWD:       "/tmp/test-repo",
			}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("cmd %q: got %s, want %s (reason: %s)", tt.cmd, got.Decision, tt.want, got.Reason)
			}
		})
	}
}
