package hookio

import "testing"

// TestDecisionOrdering pins the restrictiveness order the engine's
// EvaluateExpression fold depends on: Approve < Abstain < Ask < Reject. Reordering
// these iota constants silently breaks the fold (e.g. an Abstaining leaf would stop
// demoting an approving sibling), so this guards the ordering directly at its
// source rather than only through the engine (pg2-t4uyx).
func TestDecisionOrdering(t *testing.T) {
	if Approve >= Abstain || Abstain >= Ask || Ask >= Reject {
		t.Fatalf("Decision ordering broken: Approve=%d Abstain=%d Ask=%d Reject=%d; want Approve<Abstain<Ask<Reject",
			Approve, Abstain, Ask, Reject)
	}
	// Approve MUST be the zero value: every RuleResult that does not set Decision
	// defaults to the LEAST restrictive verdict, so a rule that forgets to set it
	// fails loud in review (audited) rather than silently green-lighting.
	if Approve != 0 {
		t.Fatalf("Approve = %d, want 0 (zero value)", Approve)
	}
}

// TestMostRestrictive_AbstainOutranksApprove is the named regression for bypass #7
// (Abstain-outranks-Approve ordering, types.go MostRestrictive). MostRestrictive is
// the shared most-risky-wins primitive the engine folds every leaf/redirection/
// substitution through; the security-critical property is that Abstain (ceta has
// no opinion → defer to Claude's own prompt) MUST beat Approve, so a compound with
// ANY non-approving leaf is never green-lit as a whole (pg2-t4uyx, pg2-1q5i3).
func TestMostRestrictive_AbstainOutranksApprove(t *testing.T) {
	approve := RuleResult{Decision: Approve, Module: "a"}
	abstain := RuleResult{Decision: Abstain, Module: "b"}
	ask := RuleResult{Decision: Ask, Module: "c"}
	reject := RuleResult{Decision: Reject, Module: "d"}

	tests := []struct {
		name               string
		current, candidate RuleResult
		want               Decision
	}{
		// The core invariant: Abstain must win over Approve regardless of argument
		// position (the fold seeds with Approve and feeds each leaf as candidate).
		{"approve then abstain", approve, abstain, Abstain},
		{"abstain then approve", abstain, approve, Abstain},
		// Ask and Reject outrank both.
		{"approve then ask", approve, ask, Ask},
		{"approve then reject", approve, reject, Reject},
		{"reject then abstain", reject, abstain, Reject},
		// Ties keep current (documented behavior).
		{"tie keeps current", ask, ask, Ask},
		// A fully-approving pair stays Approve (no over-blocking).
		{"approve then approve", approve, approve, Approve},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MostRestrictive(tt.current, tt.candidate); got.Decision != tt.want {
				t.Errorf("MostRestrictive(%v, %v).Decision = %v, want %v", tt.current.Decision, tt.candidate.Decision, got.Decision, tt.want)
			}
		})
	}
}
