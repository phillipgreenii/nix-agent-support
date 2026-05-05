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

func TestMonorepo_GrazrVariations_Approve(t *testing.T) {
	pe := patheval.NewWithCWD("/home/user/monorepo", "/home/user/monorepo")
	r := New(pe)
	commands := []string{
		"bin/grazr lint",
		"./bin/grazr lint",
		"/home/user/monorepo/bin/grazr lint",
		"grazr lint",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			CWD:      "/home/user/monorepo",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestMonorepo_OtherApproved_Approve(t *testing.T) {
	pe := patheval.NewWithCWD("/home/user/monorepo", "/home/user/monorepo")
	r := New(pe)
	commands := []string{
		"bin/gozr build",
		"bin/pyzr run",
		"bin/shzr lint",
		"bin/stevedore show service",
		"bin/epoxy check",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			CWD:      "/home/user/monorepo",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
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

func TestMonorepo_PyzrSafeEnv_Approve(t *testing.T) {
	pe := patheval.NewWithCWD("/home/user/monorepo", "/home/user/monorepo")
	r := New(pe)
	commands := []string{
		"PYTHONPATH=/foo bin/pyzr run test",
		"VIRTUAL_ENV=/venv bin/pyzr run test",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			CWD:       "/home/user/monorepo",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestMonorepo_PyzrDangerousEnv_Abstain(t *testing.T) {
	pe := patheval.NewWithCWD("/home/user/monorepo", "/home/user/monorepo")
	r := New(pe)
	commands := []string{
		"PYTHONSTARTUP=/evil.py bin/pyzr run test",
		"PYTHONHOME=/evil bin/pyzr run test",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			CWD:       "/home/user/monorepo",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s, want abstain", cmd, got.Decision)
		}
	}
}

func TestMonorepo_GozrDangerousEnv_Abstain(t *testing.T) {
	pe := patheval.NewWithCWD("/home/user/monorepo", "/home/user/monorepo")
	r := New(pe)
	commands := []string{
		"GOROOT=/evil bin/gozr test ./...",
		"GOPROXY=http://evil bin/gozr test ./...",
		"GONOSUMCHECK=* bin/gozr test ./...",
		"GONOSUMDB=* bin/gozr test ./...",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			CWD:       "/home/user/monorepo",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s, want abstain", cmd, got.Decision)
		}
	}
}

func TestMonorepo_GozrSafeEnv_Approve(t *testing.T) {
	pe := patheval.NewWithCWD("/home/user/monorepo", "/home/user/monorepo")
	r := New(pe)
	commands := []string{
		"GOFLAGS=-count=1 bin/gozr test ./...",
		"GOPATH=/go bin/gozr build",
		"GOBIN=/go/bin bin/gozr install",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			CWD:       "/home/user/monorepo",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestMonorepo_GrazrDangerousEnv_Abstain(t *testing.T) {
	pe := patheval.NewWithCWD("/home/user/monorepo", "/home/user/monorepo")
	r := New(pe)
	commands := []string{
		"GRADLE_USER_HOME=/evil bin/grazr build",
		"GRADLE_OPTS=-javaagent:evil bin/grazr build",
		"JAVA_HOME=/evil bin/grazr build",
		"JAVA_OPTS=-javaagent:evil bin/grazr test",
		"JAVA_TOOL_OPTIONS=-javaagent:evil bin/grazr build",
		"_JAVA_OPTIONS=-javaagent:evil bin/grazr build",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			CWD:       "/home/user/monorepo",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := r.Evaluate(input)
		if got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s, want abstain", cmd, got.Decision)
		}
	}
}
