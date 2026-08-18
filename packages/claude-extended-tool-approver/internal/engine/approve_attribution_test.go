package engine

// pg2-he22o: EvaluateExpression seeded its most-restrictive fold at
// {Approve, Module: "engine"} and hookio.MostRestrictive keeps `current` on a
// tie, so an approving rule's Module never displaced the seed — EVERY Approve
// on a Bash compound was attributed to "engine" instead of the rule that
// actually approved it. mostRestrictiveAttributed (engine.go) fixes the
// attribution without touching any verdict: it defers to hookio.MostRestrictive
// for every case except an exact Approve/Approve tie where one side is the
// generic "engine" identity and the other is a real rule's own Module.
//
// This file is ATTRIBUTION-only, by design: every case below either asserts a
// Decision that was already correct before the fix (unchanged), or asserts a
// Module that was wrong before it (fixed). No case here is a new verdict.

import (
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// attrApproveRule approves any leaf whose executable has the given prefix,
// under its own name, and is NotApplicable to everything else. Kept separate
// from engine_test.go's conditionalMockRule (whose Name() is hardcoded to
// "conditional") so two distinctly-named rules can sit in the same chain and
// be told apart by Module in the fold's output.
type attrApproveRule struct {
	name, prefix string
}

func (r *attrApproveRule) Name() string { return r.name }

func (r *attrApproveRule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	cmd, err := input.BashCommand()
	if err != nil {
		return hookio.NotApplicable()
	}
	for _, pc := range cmdparse.Parse(cmd) {
		if r.prefix != "" && strings.HasPrefix(pc.Executable, r.prefix) {
			return hookio.RuleResult{Decision: hookio.Approve, Reason: "approved by " + r.name, Module: r.Name()}, nil
		}
	}
	return hookio.NotApplicable()
}

// TestMostRestrictiveAttributed pins the helper's exact contract in isolation,
// independent of EvaluateExpression's plumbing: same Decision as
// hookio.MostRestrictive in every case, but an Approve/Approve tie prefers a
// real rule's Module over "engine".
func TestMostRestrictiveAttributed(t *testing.T) {
	approveEngine := hookio.RuleResult{Decision: hookio.Approve, Reason: "seed", Module: "engine"}
	approveRuleA := hookio.RuleResult{Decision: hookio.Approve, Reason: "a approved", Module: "rule-a"}
	approveRuleB := hookio.RuleResult{Decision: hookio.Approve, Reason: "b approved", Module: "rule-b"}
	askRule := hookio.RuleResult{Decision: hookio.Ask, Reason: "needs confirmation", Module: "rule-ask"}
	rejectRule := hookio.RuleResult{Decision: hookio.Reject, Reason: "no", Module: "rule-reject"}
	noOpinion := hookio.RuleResult{Decision: hookio.NoOpinion}

	tests := []struct {
		name       string
		acc        hookio.RuleResult
		candidate  hookio.RuleResult
		wantModule string
	}{
		{"engine seed then real rule ties at Approve: rule wins", approveEngine, approveRuleA, "rule-a"},
		{"real rule then engine candidate ties at Approve: rule stays (matches hookio.MostRestrictive)", approveRuleA, approveEngine, "rule-a"},
		{"two different real rules tie at Approve: acc (current) wins, exactly as hookio.MostRestrictive", approveRuleA, approveRuleB, "rule-a"},
		{"engine seed ties with engine seed: stays engine (nothing to attribute)", approveEngine, approveEngine, "engine"},
		{"candidate strictly more restrictive (Ask) always wins regardless of Module", approveRuleA, askRule, "rule-ask"},
		{"candidate strictly more restrictive (Reject) always wins regardless of Module", approveEngine, rejectRule, "rule-reject"},
		{"acc strictly more restrictive: acc wins, tie-break never reached", askRule, approveRuleA, "rule-ask"},
		{"NoOpinion vs Approve: NoOpinion wins (never a tie, tie-break not reached)", noOpinion, approveRuleA, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mostRestrictiveAttributed(tc.acc, tc.candidate)
			if got.Module != tc.wantModule {
				t.Errorf("Module = %q, want %q", got.Module, tc.wantModule)
			}
			// DECISION PARITY: mostRestrictiveAttributed must never disagree with
			// hookio.MostRestrictive on the Decision, in either direction. This is
			// the mechanical proof that the tie-break is attribution-only.
			plain := hookio.MostRestrictive(tc.acc, tc.candidate)
			if got.Decision != plain.Decision {
				t.Errorf("Decision = %v, but hookio.MostRestrictive(same inputs) = %v — the tie-break moved a VERDICT",
					got.Decision, plain.Decision)
			}
		})
	}
}

// TestEvaluateExpression_ApproveAttributesToDecidingRule is acceptance
// criterion 1: an Approve on a Bash COMPOUND (not a single simple command)
// attributes to the rule that approved, not to "engine".
func TestEvaluateExpression_ApproveAttributesToDecidingRule(t *testing.T) {
	rule := &attrApproveRule{name: "git-mock", prefix: "git"}
	e := New(rule)
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp/project"}

	got := e.EvaluateExpression("git status && git log", nil, origin)
	if got.Decision != hookio.Approve {
		t.Fatalf("Decision = %v, want Approve", got.Decision)
	}
	if got.Module != "git-mock" {
		t.Errorf("Module = %q, want %q (the deciding rule) — an Approve on a Bash compound must not attribute to \"engine\"", got.Module, "git-mock")
	}
}

// TestEvaluateExpression_ApproveAttribution_EarlyApprovesLaterNeutral and its
// mirror below are acceptance criterion 5: a compound where one leaf is
// decisively approved by a rule and the OTHER leaf is neutral (a command-less
// assignment leaf no rule objects to, contributing nothing but the engine
// seed's own identity) must attribute to the approving rule regardless of
// which leaf ran first or last.
func TestEvaluateExpression_ApproveAttribution_EarlyApprovesLaterNeutral(t *testing.T) {
	rule := &attrApproveRule{name: "git-mock", prefix: "git"}
	e := New(rule)
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp/project"}

	// "A=1" is a command-less leaf no rule in this chain has any opinion about
	// (attrApproveRule only matches an Executable prefix, and a command-less
	// leaf has Executable == ""), so it contributes the neutral engine identity
	// — it does not itself approve anything.
	got := e.EvaluateExpression("git status && A=1", nil, origin)
	if got.Decision != hookio.Approve {
		t.Fatalf("Decision = %v, want Approve (a neutral leaf must not demote its approving sibling)", got.Decision)
	}
	if got.Module != "git-mock" {
		t.Errorf(`Module = %q, want "git-mock" — the EARLY leaf's approving rule, not "engine"`, got.Module)
	}
}

func TestEvaluateExpression_ApproveAttribution_EarlyNeutralLaterApproves(t *testing.T) {
	rule := &attrApproveRule{name: "git-mock", prefix: "git"}
	e := New(rule)
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp/project"}

	// Same pair, REVERSED order: the neutral assignment leaf now runs FIRST.
	got := e.EvaluateExpression("A=1 && git status", nil, origin)
	if got.Decision != hookio.Approve {
		t.Fatalf("Decision = %v, want Approve (a neutral leaf must not demote its approving sibling)", got.Decision)
	}
	if got.Module != "git-mock" {
		t.Errorf(`Module = %q, want "git-mock" — the LATER leaf's approving rule, not "engine" and not whichever leaf ran first`, got.Module)
	}
}

// TestEvaluateExpression_EngineAttributionWhenNoRuleHasAnOpinion is acceptance
// criterion 4: "engine" MUST remain the attribution when the Approve verdict
// comes from the engine's OWN bookkeeping (a redirection-safety check, in this
// case) and no registered rule ever expressed a decision — and that case must
// stay distinguishable from "a rule approved" (the sibling case asserted in
// the same test, same shape, one rule registered).
func TestEvaluateExpression_EngineAttributionWhenNoRuleHasAnOpinion(t *testing.T) {
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp/project"}

	t.Run("no rule registered: a safe redirection-only leaf stays engine-attributed", func(t *testing.T) {
		e := New() // no rules at all: nothing but the engine's own bookkeeping can act
		// A PathEvaluator is required for evaluateRedirections to reach a decisive
		// verdict at all; without one it abstains (NoOpinion), which would defeat
		// the point of this case — it needs to reach Approve via the engine's OWN
		// redirection-safety check, not via chain exhaustion.
		e.SetPathEvaluator(patheval.NewWithCWD("/tmp/project", "/tmp/project"))
		got := e.EvaluateExpression("> /tmp/project/output.txt", nil, origin)
		if got.Decision != hookio.Approve {
			t.Fatalf("Decision = %v, want Approve (a redirection to a writable in-project path)", got.Decision)
		}
		if got.Module != "engine" {
			t.Errorf(`Module = %q, want "engine" — nobody but the engine's own redirection check judged this leaf`, got.Module)
		}
	})

	t.Run("control: a rule deciding the SAME shape attributes to the rule, not engine", func(t *testing.T) {
		rule := &attrApproveRule{name: "git-mock", prefix: "git"}
		e := New(rule)
		got := e.EvaluateExpression("git status", nil, origin)
		if got.Decision != hookio.Approve {
			t.Fatalf("Decision = %v, want Approve", got.Decision)
		}
		if got.Module != "git-mock" {
			t.Errorf(`Module = %q, want "git-mock" — distinguishing this from the "engine" case above is the point of this test`, got.Module)
		}
	})
}

// erroringRule always returns a genuine (non-ErrNotApplicable, non-ErrRefused)
// error, the way a resolver whose subprocess failed does. Modeled on
// verdict_provenance_test.go's failingRule but kept local (and unconditional
// rather than scoped to one input) so its role in the compound below is
// explicit at the call site.
type erroringRule struct{}

func (*erroringRule) Name() string { return "erroring" }
func (*erroringRule) Evaluate(*hookio.HookInput) (hookio.RuleResult, error) {
	return hookio.RuleResult{}, errors.New("resolver exploded")
}

// TestEvaluateExpression_EarlyLeafErrorFloorsNoOpinion_NeverManufacturesApprove
// is acceptance criterion 3: a genuine rule ERROR on an early leaf (not
// ErrNotApplicable, not ErrRefused — engine.Evaluate's continue-by-default
// error policy, ADR 0043) must still floor the WHOLE expression at NoOpinion,
// even though a LATER leaf is decisively approved by another rule. The fold
// must not manufacture an Approve out of a leaf no rule could clear — the
// exact hole pg2-wguam/pg2-2u5jf closed, which hookio.MostRestrictive's doc
// comment calls "trap 4" (routing a failure through the Approve identity).
//
// mostRestrictiveAttributed's tie-break activates ONLY on an Approve/Approve
// tie, so it is structurally unreachable here (the erroring leaf contributes
// NoOpinion, which strictly outranks Approve) — this test pins that the new
// helper did not accidentally widen the condition.
func TestEvaluateExpression_EarlyLeafErrorFloorsNoOpinion_NeverManufacturesApprove(t *testing.T) {
	origin := &hookio.HookInput{ToolName: "Bash", CWD: "/tmp/project"}
	// "erroring" always fails; "git-mock" decisively approves "git status".
	// Registration order puts the failing rule first so its error is recorded
	// on the FIRST ("nonsense") leaf before the second leaf even runs.
	e := New(&erroringRule{}, &attrApproveRule{name: "git-mock", prefix: "git"})

	got := e.EvaluateExpression("nonsense && git status", nil, origin)
	if got.Decision == hookio.Approve {
		t.Fatalf("Decision = Approve; an early leaf's rule ERROR must floor the whole expression at NoOpinion, "+
			"not be overridden by a later leaf's Approve (reason: %q, module: %q)", got.Reason, got.Module)
	}
	if got.Decision != hookio.NoOpinion {
		t.Errorf("Decision = %v, want NoOpinion (the error floor)", got.Decision)
	}

	// CONTROL: the same compound with the erroring rule removed approves, and
	// attributes to git-mock — proving the NoOpinion above is the error floor
	// firing, not some unrelated reason the first leaf would abstain anyway.
	eControl := New(&attrApproveRule{name: "git-mock", prefix: "git"})
	// "nonsense" alone is an unowned command leaf, so pair it with a leaf the
	// control rule DOES own on both sides to isolate the error's effect: swap
	// in an assignment-only neutral leaf instead of "nonsense" so the control
	// is genuinely all-approve.
	gotControl := eControl.EvaluateExpression("A=1 && git status", nil, origin)
	if gotControl.Decision != hookio.Approve || gotControl.Module != "git-mock" {
		t.Fatalf("control: EvaluateExpression(%q) = %v/%s, want Approve/git-mock — control setup is broken",
			"A=1 && git status", gotControl.Decision, gotControl.Module)
	}
}
