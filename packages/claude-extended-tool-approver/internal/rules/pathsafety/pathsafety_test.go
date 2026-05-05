package pathsafety

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

func TestPathSafety_ReadInProject_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": "/home/user/project/foo.go"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Read in project: got %s, want approve", got.Decision)
	}
}

func TestPathSafety_WriteInProject_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Write",
		ToolInput: mustJSON(map[string]string{"file_path": "/home/user/project/foo.go", "content": "x"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Write in project: got %s, want approve", got.Decision)
	}
}

func TestPathSafety_ReadNixStore_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": "/nix/store/abc123-foo"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Read /nix/store: got %s, want approve (read-only paths support reads)", got.Decision)
	}
}

func TestPathSafety_WriteNixStore_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Write",
		ToolInput: mustJSON(map[string]string{"file_path": "/nix/store/abc123-foo", "content": "x"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("Write /nix/store: got %s, want abstain (read-only path, deferred to claude-code)", got.Decision)
	}
}

func TestPathSafety_WriteUnknownPath_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Write",
		ToolInput: mustJSON(map[string]string{"file_path": "/etc/hosts", "content": "x"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("Write unknown path: got %s, want abstain", got.Decision)
	}
}

func TestPathSafety_ReadUnknownPath_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": "/usr/bin/ls"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("Read /usr/bin/ls: got %s, want abstain (unknown path)", got.Decision)
	}
}

func TestPathSafety_DeleteInProject_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Delete",
		ToolInput: mustJSON(map[string]string{"file_path": "/home/user/project/foo.go"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Delete in project: got %s, want approve", got.Decision)
	}
}

func TestPathSafety_Bash_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "echo hello"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("Bash: got %s, want abstain", got.Decision)
	}
}

func TestPathSafety_WriteTmp_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Write",
		ToolInput: mustJSON(map[string]string{"file_path": "/tmp/foo.txt", "content": "x"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Write /tmp: got %s, want approve", got.Decision)
	}
}

func TestPathSafety_Name(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	if got := r.Name(); got != "path-safety" {
		t.Errorf("Name() = %q, want path-safety", got)
	}
}

func TestPathSafety_DenyRead_Rejects(t *testing.T) {
	pe := patheval.NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{
		DenyRead: []string{"/Users/phillipg/.ssh"},
	})
	rule := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": "/Users/phillipg/.ssh/id_rsa"}),
	}
	result := rule.Evaluate(input)
	if result.Decision != hookio.Reject {
		t.Errorf("Decision = %v, want Reject for denyRead path", result.Decision)
	}
}

func TestPathSafety_DenyWrite_Rejects(t *testing.T) {
	pe := patheval.NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{
		DenyWrite: []string{"/Users/phillipg/.ssh"},
	})
	rule := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Edit",
		ToolInput: mustJSON(map[string]string{"file_path": "/Users/phillipg/.ssh/known_hosts"}),
	}
	result := rule.Evaluate(input)
	if result.Decision != hookio.Reject {
		t.Errorf("Decision = %v, want Reject for denyWrite path", result.Decision)
	}
}

func TestPathSafety_DenyWrite_CWD_Rejects(t *testing.T) {
	pe := patheval.NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{
		DenyWrite: []string{"/project/secrets"},
	})
	rule := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Write",
		ToolInput: mustJSON(map[string]string{"file_path": "/project/secrets/key.pem", "content": "x"}),
	}
	result := rule.Evaluate(input)
	if result.Decision != hookio.Reject {
		t.Errorf("Decision = %v, want Reject for denyWrite path under CWD", result.Decision)
	}
}

func TestPathSafety_AllowWrite_Approves(t *testing.T) {
	pe := patheval.NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{
		AllowWrite: []string{"/Users/phillipg/.local/share/contained-claude"},
	})
	rule := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Write",
		ToolInput: mustJSON(map[string]string{"file_path": "/Users/phillipg/.local/share/contained-claude/state.json", "content": "x"}),
	}
	result := rule.Evaluate(input)
	if result.Decision != hookio.Approve {
		t.Errorf("Decision = %v, want Approve for allowWrite path", result.Decision)
	}
}

func TestPathSafety_GlobInProject_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Glob",
		ToolInput: mustJSON(map[string]string{"pattern": "**/*.go", "path": "/home/user/project/src"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Glob in project: got %s (%s), want approve", got.Decision, got.Reason)
	}
}

func TestPathSafety_GlobNoPath_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Glob",
		ToolInput: mustJSON(map[string]string{"pattern": "**/*.go"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Glob no path (defaults to CWD): got %s (%s), want approve", got.Decision, got.Reason)
	}
}

func TestPathSafety_GrepInProject_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Grep",
		ToolInput: mustJSON(map[string]string{"pattern": "TODO", "path": "/home/user/project/src"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Grep in project: got %s (%s), want approve", got.Decision, got.Reason)
	}
}

func TestPathSafety_GrepNoPath_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Grep",
		ToolInput: mustJSON(map[string]string{"pattern": "TODO"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Grep no path (defaults to CWD): got %s (%s), want approve", got.Decision, got.Reason)
	}
}

func TestPathSafety_GlobNixStore_Approve(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Glob",
		ToolInput: mustJSON(map[string]string{"pattern": "**/*.nix", "path": "/nix/store/abc123-foo"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Approve {
		t.Errorf("Glob /nix/store: got %s (%s), want approve (read-only paths support search)", got.Decision, got.Reason)
	}
}

func TestPathSafety_GlobUnknownPath_Abstain(t *testing.T) {
	pe := patheval.New("/home/user/project")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Glob",
		ToolInput: mustJSON(map[string]string{"pattern": "*", "path": "/usr/local/bin"}),
		CWD:       "/home/user/project",
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("Glob /usr/local/bin: got %s, want abstain (unknown path)", got.Decision)
	}
}

func TestPathSafety_GlobDenyRead_Reject(t *testing.T) {
	pe := patheval.NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{
		DenyRead: []string{"/Users/phillipg/.ssh"},
	})
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Glob",
		ToolInput: mustJSON(map[string]string{"pattern": "*", "path": "/Users/phillipg/.ssh"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Reject {
		t.Errorf("Glob deny-read path: got %s, want reject", got.Decision)
	}
}

func TestPathSafety_GrepDenyRead_Reject(t *testing.T) {
	pe := patheval.NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&patheval.SandboxFilesystemConfig{
		DenyRead: []string{"/Users/phillipg/.ssh"},
	})
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Grep",
		ToolInput: mustJSON(map[string]string{"pattern": "password", "path": "/Users/phillipg/.ssh"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Reject {
		t.Errorf("Grep deny-read path: got %s, want reject", got.Decision)
	}
}
