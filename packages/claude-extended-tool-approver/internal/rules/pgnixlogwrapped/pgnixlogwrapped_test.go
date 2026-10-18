package pgnixlogwrapped

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func bash(cmd string) *hookio.HookInput {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return &hookio.HookInput{ToolName: "Bash", ToolInput: b}
}

// stubEval approves "nix ..." and rejects "darwin-rebuild switch ...", keyed
// on the delegated leaf, to prove the verdict is forwarded from the wrapped
// command rather than decided by the wrapper.
type stubEval struct{ seen []string }

func (s *stubEval) EvaluateExpression(string, []hookio.StackFrame, *hookio.HookInput) hookio.RuleResult {
	return hookio.RuleResult{Decision: hookio.NoOpinion}
}

func (s *stubEval) EvaluateStructure(_ string, leaves []cmdparse.ParsedCommand, _ []hookio.StackFrame, _ *hookio.HookInput, _, _ map[string]string) hookio.RuleResult {
	l := leaves[0]
	s.seen = append(s.seen, strings.Join(append([]string{l.Executable}, l.Args...), " "))
	switch {
	case l.Executable == "nix":
		return hookio.RuleResult{Decision: hookio.Approve, Module: "stub"}
	case l.Executable == "darwin-rebuild" && len(l.Args) > 0 && l.Args[0] == "switch":
		return hookio.RuleResult{Decision: hookio.Reject, Module: "stub"}
	}
	return hookio.RuleResult{Decision: hookio.NoOpinion, Provenance: hookio.ProvenanceExhaustion}
}

func TestForwardsWrappedVerdict(t *testing.T) {
	tests := []struct {
		cmd  string
		want hookio.Decision
		seen string
	}{
		{"pg-nix-log-wrapped -- nix flake check", hookio.Approve, "nix flake check"},
		{"pg-nix-log-wrapped nix build .#some-package", hookio.Approve, "nix build .#some-package"},
		{"pg-nix-log-wrapped --otlp-endpoint http://127.0.0.1:4318 --traceparent 00-a-b-01 -- nix build .#p", hookio.Approve, "nix build .#p"},
		{"pg-nix-log-wrapped -- darwin-rebuild switch --flake .", hookio.Reject, "darwin-rebuild switch --flake ."},
	}
	for _, tc := range tests {
		ev := &stubEval{}
		got, err := NewWithEvaluator(ev).Evaluate(bash(tc.cmd))
		if err != nil {
			t.Fatalf("%q: %v", tc.cmd, err)
		}
		if got.Decision != tc.want {
			t.Errorf("%q: decision = %v, want %v", tc.cmd, got.Decision, tc.want)
		}
		if len(ev.seen) != 1 || ev.seen[0] != tc.seen {
			t.Errorf("%q: delegated %v, want [%q]", tc.cmd, ev.seen, tc.seen)
		}
	}
}

func TestCheckApprovedWithoutDelegating(t *testing.T) {
	ev := &stubEval{}
	got, _ := NewWithEvaluator(ev).Evaluate(bash("pg-nix-log-wrapped --check --otlp-endpoint http://x"))
	if got.Decision != hookio.Approve || len(ev.seen) != 0 {
		t.Fatalf("got %v seen %v, want Approve with no delegation", got.Decision, ev.seen)
	}
}

func TestNotApplicable(t *testing.T) {
	for _, cmd := range []string{
		"ls",
		"pg-nix-log-wrapped",
		"pg-nix-log-wrapped --bogus -- nix build .#p",
		"pg-nix-log-wrapped --",
	} {
		_, err := NewWithEvaluator(&stubEval{}).Evaluate(bash(cmd))
		if !errors.Is(err, hookio.ErrNotApplicable) {
			t.Errorf("%q: err = %v, want ErrNotApplicable", cmd, err)
		}
	}
	_, err := New().Evaluate(bash("pg-nix-log-wrapped -- nix build .#p"))
	if !errors.Is(err, hookio.ErrNotApplicable) {
		t.Errorf("no evaluator: err = %v, want ErrNotApplicable", err)
	}
}

func TestUnownedWrappedCommandFallsThrough(t *testing.T) {
	_, err := NewWithEvaluator(&stubEval{}).Evaluate(bash("pg-nix-log-wrapped -- some-unknown-tool"))
	if !errors.Is(err, hookio.ErrNotApplicable) {
		t.Fatalf("err = %v, want ErrNotApplicable", err)
	}
}
