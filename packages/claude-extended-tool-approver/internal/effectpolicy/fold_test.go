package effectpolicy

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectgraph"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// TestFoldAllPermittedApproves: P1's APPROVE case — every command node
// MarkPermitted folds to Approve.
func TestFoldAllPermittedApproves(t *testing.T) {
	g := &effectgraph.Graph{Nodes: []effectgraph.Node{
		{ID: "n0", Kind: effectgraph.NodeCommand, Label: "cat README.md", Mark: effectgraph.MarkPermitted},
		{ID: "n1", Kind: effectgraph.NodeCommand, Label: "echo hi", Mark: effectgraph.MarkPermitted},
	}}
	decision, reason, findings, err := Fold(g)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if decision != evalcontract.Approve {
		t.Fatalf("decision = %s, want approve (reason: %q)", decision, reason)
	}
	if len(findings) != 0 {
		t.Errorf("findings = %v, want none", findings)
	}
}

// TestFoldAnyRejectDominatesAlongsidePermittedAndUnknown: P1's REJECT case
// wins outright even when other nodes are Permitted or Insufficient
// (Unknown) — Reject is the strictest, safest verdict and nothing softens
// it.
func TestFoldAnyRejectDominatesAlongsidePermittedAndUnknown(t *testing.T) {
	g := &effectgraph.Graph{Nodes: []effectgraph.Node{
		{ID: "n0", Kind: effectgraph.NodeCommand, Label: "cat README.md", Mark: effectgraph.MarkPermitted},
		{ID: "n1", Kind: effectgraph.NodeCommand, Label: "some-unmodeled-cmd", Mark: effectgraph.MarkInsufficient, MarkReason: "no policy judges this effect"},
		{ID: "n2", Kind: effectgraph.NodeCommand, Label: "rm ~/.ssh/id_rsa", Mark: effectgraph.MarkForbidden, MarkReason: "path-access (well-known credential store)"},
	}}
	decision, reason, findings, err := Fold(g)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if decision != evalcontract.Reject {
		t.Fatalf("decision = %s, want reject (reason: %q)", decision, reason)
	}
	if !strings.Contains(reason, "n2") {
		t.Errorf("reason = %q, want it to name the forbidden node n2", reason)
	}
	if len(findings) != 2 {
		t.Errorf("findings = %v, want 2 (the forbidden node and the insufficient node)", findings)
	}
}

// TestFoldAnyUnknownNoneRejectAbstains: no Forbidden node, but at least one
// Insufficient (Unknown) node, folds to Abstain — P1's "otherwise ABSTAIN".
func TestFoldAnyUnknownNoneRejectAbstains(t *testing.T) {
	g := &effectgraph.Graph{Nodes: []effectgraph.Node{
		{ID: "n0", Kind: effectgraph.NodeCommand, Label: "cat README.md", Mark: effectgraph.MarkPermitted},
		{ID: "n1", Kind: effectgraph.NodeCommand, Label: "some-unmodeled-cmd", Mark: effectgraph.MarkInsufficient, MarkReason: "no policy judges this effect"},
	}}
	decision, reason, _, err := Fold(g)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if decision != evalcontract.Abstain {
		t.Fatalf("decision = %s, want abstain (reason: %q)", decision, reason)
	}
	if !strings.Contains(reason, "n1") {
		t.Errorf("reason = %q, want it to name the insufficient node n1", reason)
	}
}

// TestFoldNoCommandNodesAbstains: an empty (or command-less) graph has
// nothing to be understood OR permitted, so it abstains rather than
// vacuously approving.
func TestFoldNoCommandNodesAbstains(t *testing.T) {
	decision, reason, _, err := Fold(&effectgraph.Graph{})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if decision != evalcontract.Abstain {
		t.Fatalf("decision = %s, want abstain", decision)
	}
	if reason != "no command nodes" {
		t.Errorf("reason = %q", reason)
	}
}

// TestFoldNilGraphAbstains: Fold never dereferences a nil graph.
func TestFoldNilGraphAbstains(t *testing.T) {
	decision, _, _, err := Fold(nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if decision != evalcontract.Abstain {
		t.Fatalf("decision = %s, want abstain", decision)
	}
}

// TestFoldHardFailuresForceAbstainOverApprove: each of the shared
// preamble's named hard-failure classes (parse, deadline/timeout,
// unsupported-tool, config-load) independently downgrades what would
// otherwise be Approve (every node Permitted) to Abstain, per P1's "iff a
// bounded/timeout/parse/panic/unsupported-tool/config-dependent load
// failure ⇒ ABSTAIN" rule.
func TestFoldHardFailuresForceAbstainOverApprove(t *testing.T) {
	allPermitted := func() *effectgraph.Graph {
		return &effectgraph.Graph{Nodes: []effectgraph.Node{
			{ID: "n0", Kind: effectgraph.NodeCommand, Label: "cat README.md", Mark: effectgraph.MarkPermitted},
		}}
	}

	cases := []struct {
		name    string
		failure FoldFailure
	}{
		{"parse", FoldFailure{Kind: "parse", Reason: "unparseable: unterminated quote"}},
		{"deadline", FoldFailure{Kind: "deadline", Reason: "exceeded bounded-evaluation deadline"}},
		{"unsupported-tool", FoldFailure{Kind: "unsupported-tool", Reason: "Glob"}},
		{"config-load", FoldFailure{Kind: "config-load", Reason: "rules.json: unexpected EOF"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision, reason, findings, err := Fold(allPermitted(), tc.failure)
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if decision != evalcontract.Abstain {
				t.Fatalf("decision = %s, want abstain (a hard failure must never let Approve through)", decision)
			}
			if !strings.Contains(reason, tc.failure.Reason) {
				t.Errorf("reason = %q, want it to name the failure %q", reason, tc.failure.Reason)
			}
			found := false
			for _, f := range findings {
				if strings.Contains(f, tc.failure.Kind) {
					found = true
				}
			}
			if !found {
				t.Errorf("findings = %v, want an entry naming failure kind %q", findings, tc.failure.Kind)
			}
		})
	}
}

// TestFoldHardFailureDoesNotOverrideReject: a hard failure can only ever
// tighten the outcome, never loosen an already-confirmed Reject — Reject
// stays Reject even when a failure was also reported.
func TestFoldHardFailureDoesNotOverrideReject(t *testing.T) {
	g := &effectgraph.Graph{Nodes: []effectgraph.Node{
		{ID: "n0", Kind: effectgraph.NodeCommand, Label: "rm ~/.ssh/id_rsa", Mark: effectgraph.MarkForbidden, MarkReason: "path-access (well-known credential store)"},
	}}
	decision, _, _, err := Fold(g, FoldFailure{Kind: "deadline", Reason: "exceeded bounded-evaluation deadline"})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if decision != evalcontract.Reject {
		t.Fatalf("decision = %s, want reject (a failure must not loosen an already-confirmed reject)", decision)
	}
}

// panicPolicy is a Policy whose Judge always panics — a stand-in for a
// broken classification function (a future packet's bug, or a genuinely
// unexpected input the effect-graph builder produced) — used to prove
// EvaluateGraph's own recover() catches it and folds to Abstain rather
// than letting the panic escape.
type panicPolicy struct{}

func (panicPolicy) Name() string { return "panic-policy" }
func (panicPolicy) Judge(cmddesc.Effect, PolicyContext) (Finding, bool) {
	panic("simulated classification-function panic")
}

// TestFoldGraphPanicRecoversToAbstainNeverPropagates: a forced panic
// raised while EvaluateGraph walks a node's effects through a Policy.Judge
// implementation is recovered and folds to Abstain — the panic never
// propagates to this test (a t.Fatal from a propagated panic would abort
// the whole test binary, not just fail this test, so simply reaching the
// assertions below already proves the recover fired).
func TestFoldGraphPanicRecoversToAbstainNeverPropagates(t *testing.T) {
	g := effectgraph.Graph{Nodes: []effectgraph.Node{
		{
			ID:      "n0",
			Kind:    effectgraph.NodeCommand,
			Label:   "cat README.md",
			Effects: []cmddesc.Effect{{Kind: cmddesc.EffectPath, Path: "README.md", Access: cmddesc.AccessRead}},
		},
	}}
	resp := EvaluateGraph(g, g, []Policy{panicPolicy{}}, nil, PolicyContext{})
	if resp.Decision != evalcontract.Abstain {
		t.Fatalf("decision = %s, want abstain", resp.Decision)
	}
	if !strings.Contains(resp.Reason, "simulated classification-function panic") {
		t.Errorf("reason = %q, want it to mention the recovered panic", resp.Reason)
	}
}

// TestFoldEvaluatePanicRecoversToAbstainNeverPropagates: the same recovery
// guarantee at Evaluate's own composition-root level (the entry point a
// hook actually calls), independent of EvaluateGraph's own recover.
func TestFoldEvaluatePanicRecoversToAbstainNeverPropagates(t *testing.T) {
	resp := Evaluate(
		evalcontract.Request{Command: "cat README.md", CWD: "."},
		cmddesc.DefaultRegistry(),
		[]Policy{panicPolicy{}},
		nil,
	)
	if resp.Decision != evalcontract.Abstain {
		t.Fatalf("decision = %s, want abstain", resp.Decision)
	}
	if !strings.Contains(resp.Reason, "simulated classification-function panic") {
		t.Errorf("reason = %q, want it to mention the recovered panic", resp.Reason)
	}
}
