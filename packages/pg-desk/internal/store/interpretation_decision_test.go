package store

import "testing"

func TestInterpretationDecisionAndPairChange(t *testing.T) {
	a := Interpretation{Approvals: `{"waiting_on":"me","reason":"r1"}`, Panel: "p1"}
	if got := a.Decision(); got != (Decision{WaitingOn: "me", Panel: "p1", Reason: "r1"}) {
		t.Fatalf("Decision = %+v", got)
	}
	if got := (Interpretation{Approvals: `garbage`, Panel: "p"}).Decision(); got != (Decision{Panel: "p"}) {
		t.Errorf("malformed blob = %+v, want only the panel", got)
	}
	reasonOnly := Interpretation{Approvals: `{"waiting_on":"me","reason":"r2"}`, Panel: "p1"}
	if DecisionPairChanged(a, reasonOnly) {
		t.Error("a reason-only difference counts as a pair change")
	}
	if !DecisionPairChanged(a, Interpretation{Approvals: `{"waiting_on":"author"}`, Panel: "p1"}) {
		t.Error("waiting_on difference not detected")
	}
	if !DecisionPairChanged(a, Interpretation{Approvals: a.Approvals, Panel: "p2"}) {
		t.Error("panel difference not detected")
	}
}
