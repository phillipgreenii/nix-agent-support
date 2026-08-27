package assume

import (
	"encoding/json"
	"strings"
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
		{"not assume", "ls -la", "Bash", hookio.NoOpinion},
		{"assume in arg", "echo assume", "Bash", hookio.NoOpinion},
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

// mockEvaluator is a minimal hookio.Evaluator for exercising assume's
// `--exec "<inner>"` I13 structural delegation (pg2-bm0ai) without pulling in
// the real engine — mirrors internal/rules/kubectl's and
// internal/rules/safecmds' own mockEvaluator (map-keyed lookup on the exact
// source text, falling back to defaultResult). The map entry for
// "bin/stevedore show job_annotation/classify_title" below stands in for a
// real deployment's config-rules approval of `stevedore` (pg2-9cxtr's
// ZR apply-time config layer, out of this repo's scope) — see this bead's
// body for why that data change lives in phillipg-nix-ziprecruiter, not here.
type mockEvaluator struct {
	results       map[string]hookio.RuleResult
	defaultResult hookio.RuleResult
}

func (m *mockEvaluator) EvaluateExpression(expr string, stack []hookio.StackFrame, origin *hookio.HookInput) hookio.RuleResult {
	if r, ok := m.results[strings.TrimSpace(expr)]; ok {
		return r
	}
	return m.defaultResult
}

func (m *mockEvaluator) EvaluateStructure(source string, leaves any, stack []hookio.StackFrame, origin *hookio.HookInput) hookio.RuleResult {
	return m.EvaluateExpression(source, stack, origin)
}

// TestAssumeRule_ExecUnwrap covers pg2-bm0ai's acceptance criteria for the
// `assume ... --exec "<inner-command-string>"` delegation, reached via the
// `source` builtin (measured leaf shape: Executable "source", Args
// ["assume", ...] — see assumeArgs' doc for why a `bash -c`-wrapped spelling
// is a different, out-of-scope leaf shape).
func TestAssumeRule_ExecUnwrap(t *testing.T) {
	mockEval := &mockEvaluator{
		results: map[string]hookio.RuleResult{
			// Stands in for stevedore already being config-approved (pg2-9cxtr).
			"bin/stevedore show job_annotation/classify_title": {
				Decision: hookio.Approve, Reason: "config-rules: stevedore approved", Module: "mock",
			},
		},
		defaultResult: hookio.RuleResult{Decision: hookio.NoOpinion, Module: "mock"},
	}
	r := NewWithEvaluator(mockEval)

	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{
			"source assume --exec approvable inner -> approve",
			`source assume dev/developers-dev --exec "bin/stevedore show job_annotation/classify_title"`,
			hookio.Approve,
		},
		{
			// Regression: bare assume (no --exec) must still hard-Reject.
			"bare assume unchanged",
			"assume dev/developers-dev",
			hookio.Reject,
		},
		{
			// The inner command is not approvable by anything in the chain
			// (mock's defaultResult is NoOpinion) — must NOT silently approve.
			"source assume --exec non-approvable inner -> not approve",
			`source assume dev/developers-dev --exec "rm -rf /"`,
			hookio.NoOpinion,
		},
		{
			// Malformed --exec value: the OUTER command parses fine (the
			// unbalanced "'" is just a literal character inside the outer
			// double-quoted argument), but re-parsing the extracted VALUE on
			// its own as shell source fails (unclosed quote) — must fail
			// closed to Abstain, never Approve.
			"source assume --exec malformed value fails closed",
			`source assume dev/developers-dev --exec "bin/stevedore show 'unterminated"`,
			hookio.NoOpinion,
		},
		{
			// Direct (no source/.) spelling of the same shape also works.
			"direct assume --exec approvable inner -> approve",
			`assume dev/developers-dev --exec "bin/stevedore show job_annotation/classify_title"`,
			hookio.Approve,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(tt.command)}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != tt.want {
				t.Errorf("cmd %q: Decision = %v, want %v (reason: %s)", tt.command, got.Decision, tt.want, got.Reason)
			}
		})
	}
}

// TestAssumeRule_ExecUnwrap_NilEvaluator pins the New() (no evaluator)
// construction path for the --exec branch specifically: it MUST fail closed
// to NotApplicable (abstain), never silently approve and never panic on a nil
// r.exprEval — mirrors safecmds' TestSafecmds_XargsShC_NilEvaluator.
func TestAssumeRule_ExecUnwrap_NilEvaluator(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(`source assume dev/x --exec "bin/stevedore show y"`),
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("Decision = %v, want abstain (no evaluator wired, construction state, not a judgement)", got.Decision)
	}
}

// TestAssumeRule_ExecFlagValue_Glued pins the `--exec=<value>` glued spelling
// alongside the space-separated form.
func TestAssumeRule_ExecFlagValue_Glued(t *testing.T) {
	value, ok := execFlagValue([]string{"dev/x", "--exec=bin/stevedore show y"})
	if !ok || value != "bin/stevedore show y" {
		t.Errorf("execFlagValue glued = (%q, %v), want (%q, true)", value, ok, "bin/stevedore show y")
	}
}

// TestStructuralExecCommand_UnparseableFailsClosed pins the ok=false branch
// directly (kubectl's TestStructuralInnerCommand_UnparseableFailsClosed
// pattern): a value that cannot be parsed at all must not be handed to
// EvaluateStructure as an empty leaf set.
func TestStructuralExecCommand_UnparseableFailsClosed(t *testing.T) {
	_, ok := structuralExecCommand("echo $(unterminated")
	if ok {
		t.Fatalf("structuralExecCommand returned ok=true for an unparseable value")
	}
}

// TestStructuralExecCommand_Blank pins the "parses cleanly but names no
// command" branch (blank/comment-only --exec value) as ALSO ok=false.
func TestStructuralExecCommand_Blank(t *testing.T) {
	_, ok := structuralExecCommand("   ")
	if ok {
		t.Fatalf("structuralExecCommand returned ok=true for a blank value")
	}
}
