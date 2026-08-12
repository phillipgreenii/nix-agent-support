package engine

import (
	"errors"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// engineEvaluateSignature is the CHAIN-RUNNER signature. Pinning it at compile time
// is the guard ADR 0043's Consequences demand: before that ADR, *Engine.Evaluate had
// the SAME shape as hookio.RuleModule.Evaluate, so widening the interface changes
// which of the two this method matches with NO compile error anywhere. If someone
// later "harmonizes" Engine.Evaluate to (RuleResult, error), this line fails and the
// three-case chokepoint's contract gets re-read instead of silently moved.
//
// It stays a BARE RuleResult deliberately: participation (ErrNotApplicable) and
// failure are both CONSUMED inside the loop, and an exhausted chain manufactures the
// terminal NoOpinion, so there is nothing left for an error to mean here. It is also
// what keeps every existing caller — including the ordering tests this bead must not
// edit the expectations of — reading a single verdict.
var engineEvaluateSignature func(*hookio.HookInput) hookio.RuleResult = (*Engine)(nil).Evaluate

// TestEngineIsNotItselfARuleModule records the OTHER half of that trap, and corrects
// the ADR's premise while doing so.
//
// ADR 0043's Consequences state that "*engine.Engine ... structurally satisfies
// RuleModule today". Measured on this tree it does NOT and never did: RuleModule
// requires Name() string and *Engine has no Name method at all (it has RuleNames()
// []string, a different signature). So the ADR's worry — a silent un-satisfy — could
// not have happened through that door. The real hazard it was reaching for is the
// signature collision pinned by engineEvaluateSignature above.
//
// The assertion is kept rather than dropped, because "the engine is not one of its
// own rules" is worth holding: registering it into its own chain would recurse until
// the stack ran out, and the chain is built from a []hookio.RuleModule.
func TestEngineIsNotItselfARuleModule(t *testing.T) {
	var any1 any = (*Engine)(nil)
	if _, ok := any1.(hookio.RuleModule); ok {
		t.Fatal("*Engine now satisfies hookio.RuleModule — it must not: it is the chain RUNNER, " +
			"and registering it into its own chain recurses without bound. If a Name() method was " +
			"added deliberately, re-read ADR 0043's Consequences before relaxing this.")
	}

	// The engine's own conformance the ADR asked to be verified explicitly: it
	// implements the RECURSION interface, not the rule interface.
	var _ hookio.Evaluator = (*Engine)(nil)
}

// TestExhaustedChainManufacturesTerminalNoOpinion pins the loop-exhaustion half of
// the three-case switch: with every rule reporting not-applicable, the engine still
// produces the terminal NoOpinion (which FormatOutput renders as `{}`), not the
// Approve zero value.
func TestExhaustedChainManufacturesTerminalNoOpinion(t *testing.T) {
	e := New(notApplicableRule{name: "a"}, notApplicableRule{name: "b"})
	got := e.Evaluate(&hookio.HookInput{ToolName: "Bash"})
	if got.Decision != hookio.NoOpinion {
		t.Fatalf("exhausted chain gave %s, want NoOpinion", got.Decision)
	}
	if string(hookio.FormatOutput(got, nil)) != "{}" {
		t.Fatalf("terminal NoOpinion rendered %q, want {}", hookio.FormatOutput(got, nil))
	}
}

// TestGenuineRuleErrorContinuesAndIsRecordedPerRule pins the third case: a rule that
// returns an error which is NOT ErrNotApplicable is recorded against that rule and
// the chain carries on to the next one.
func TestGenuineRuleErrorContinuesAndIsRecordedPerRule(t *testing.T) {
	sink := &capturingSink{}
	e := New(failingRule{name: "broken"}, approvingRule{name: "later"})
	e.SetRuleErrorSink(sink)

	got := e.Evaluate(&hookio.HookInput{ToolName: "Bash"})
	if got.Decision != hookio.Approve || got.Module != "later" {
		t.Fatalf("got %s/%s, want approve/later — a genuine error must CONTINUE the chain", got.Decision, got.Module)
	}
	if len(sink.rules) != 1 || sink.rules[0] != "broken" {
		t.Fatalf("sink recorded %v, want exactly [broken]", sink.rules)
	}
}

// TestNotApplicableIsNotRecordedAsAnError pins the discrimination itself: the control
// signal must not pollute the failure counter, or a systematically-failing resolver
// becomes undetectable in the noise of every rule that simply did not apply.
func TestNotApplicableIsNotRecordedAsAnError(t *testing.T) {
	sink := &capturingSink{}
	e := New(notApplicableRule{name: "a"}, notApplicableRule{name: "b"})
	e.SetRuleErrorSink(sink)
	_ = e.Evaluate(&hookio.HookInput{ToolName: "Bash"})
	if len(sink.rules) != 0 {
		t.Fatalf("sink recorded %v for not-applicable returns, want none", sink.rules)
	}
}

// TestNoOpinionVerdictIsTerminal pins the NEW capability ADR 0043 adds: a rule can
// now say "I handled this and my answer is no gate", and that STOPS the chain, so a
// later rule cannot approve behind it. This is what makes pathsafety's agent-config
// write branch honour ADR 0041.
func TestNoOpinionVerdictIsTerminal(t *testing.T) {
	e := New(noOpinionRule{name: "decider"}, approvingRule{name: "later"})
	got := e.Evaluate(&hookio.HookInput{ToolName: "Bash"})
	if got.Decision != hookio.NoOpinion || got.Module != "decider" {
		t.Fatalf("got %s/%s, want abstain/decider — a NoOpinion VERDICT must be terminal", got.Decision, got.Module)
	}
}

type notApplicableRule struct{ name string }

func (r notApplicableRule) Name() string { return r.name }
func (r notApplicableRule) Evaluate(*hookio.HookInput) (hookio.RuleResult, error) {
	return hookio.NotApplicable()
}

type noOpinionRule struct{ name string }

func (r noOpinionRule) Name() string { return r.name }
func (r noOpinionRule) Evaluate(*hookio.HookInput) (hookio.RuleResult, error) {
	return hookio.RuleResult{Decision: hookio.NoOpinion, Module: r.name}, nil
}

type approvingRule struct{ name string }

func (r approvingRule) Name() string { return r.name }
func (r approvingRule) Evaluate(*hookio.HookInput) (hookio.RuleResult, error) {
	return hookio.RuleResult{Decision: hookio.Approve, Module: r.name}, nil
}

type failingRule struct{ name string }

func (r failingRule) Name() string { return r.name }
func (r failingRule) Evaluate(*hookio.HookInput) (hookio.RuleResult, error) {
	return hookio.RuleResult{}, errBroken
}

type capturingSink struct{ rules []string }

func (s *capturingSink) Record(rule string, _ error) { s.rules = append(s.rules, rule) }

// errBroken stands for any evidence-gathering failure (a subprocess timeout, a
// malformed tool_input). It MUST NOT wrap hookio.ErrNotApplicable — that is ADR
// 0043's decision 5, and hookio's own guard test enforces it repo-wide.
var errBroken = errors.New("resolver exploded")
