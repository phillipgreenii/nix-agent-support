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
		{"source assume", "source assume dev/example-role", "Bash", hookio.Reject},
		{"dot assume", ". assume dev/example-role", "Bash", hookio.Reject},
		{"not assume", "ls -la", "Bash", hookio.NoOpinion},
		{"assume in arg", "echo assume", "Bash", hookio.NoOpinion},
		{"source other script", "source ./env.sh", "Bash", hookio.NoOpinion},
		{"non-bash tool", "", "Read", hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: tt.tool, ToolInput: mustJSON(tt.command)}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v", got.Decision, tt.want)
			}
		})
	}
}

// TestAssumeRule_ExecRejects pins the operator ruling (Phillip, 2026-11-17,
// "yes reject assume --exec like assume."): every --exec form, even with an
// inner command that is approvable on its own, rejects with the bare-assume
// message. The earlier structural delegation of the --exec payload is gone.
func TestAssumeRule_ExecRejects(t *testing.T) {
	r := New()
	bare := hookio.Verdict(r.Evaluate(&hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON("assume dev/example-role")}))
	if bare.Decision != hookio.Reject {
		t.Fatalf("bare assume: Decision = %v, want Reject", bare.Decision)
	}
	commands := []string{
		`assume dev/example-role --exec "bin/tool show x"`,
		`assume dev/example-role --exec "ls"`,
		`assume dev/example-role --exec=ls`,
		`assume --exec "bin/tool show x" dev/example-role`,
		`assume dev/example-role --exec`,
		`assume dev/example-role --exec "bin/tool show 'unterminated"`,
		`source assume dev/example-role --exec "bin/tool show x"`,
		`source assume dev/example-role --exec=ls`,
		`. assume dev/example-role --exec "bin/tool show x"`,
		`. assume dev/example-role --exec=ls`,
		`source assume dev/example-role --exec "rm -rf /"`,
		`/usr/local/bin/assume dev/example-role --exec "ls"`,
	}
	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(&hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(cmd)}))
			if got.Decision != hookio.Reject {
				t.Errorf("Decision = %v, want Reject (reason: %s)", got.Decision, got.Reason)
			}
			if got.Reason != bare.Reason {
				t.Errorf("Reason = %q, want the bare-assume message %q", got.Reason, bare.Reason)
			}
		})
	}
}
