package inputproc

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// TestP10Reevaluation pins ADR 0075's P10 ("Input-processor rewrites are
// re-evaluated; a stricter verdict on the rewrite emits the original
// verdict without the rewrite; a rewrite never turns non-Approve into
// Approve") against ProcessReevaluated.
func TestP10Reevaluation(t *testing.T) {
	t.Run("no rewrite: original decision passes through untouched", func(t *testing.T) {
		setProcessors(t) // none configured — Process never rewrites
		reeval := func(string) evalcontract.Decision {
			t.Fatal("reeval must not be called when the chain produced no rewrite")
			return evalcontract.Abstain
		}
		cmd, applied, decision := ProcessReevaluated("git status", mainSessionPayload, evalcontract.Approve, reeval)
		if applied {
			t.Error("applied = true, want false (no rewrite happened)")
		}
		if cmd != "git status" {
			t.Errorf("finalCommand = %q, want the original command unchanged", cmd)
		}
		if decision != evalcontract.Approve {
			t.Errorf("decision = %v, want Approve (original)", decision)
		}
	})

	t.Run("rewrite re-evaluates SAME or LOOSER: applied, original decision kept", func(t *testing.T) {
		mock := writeMockProcessor(t, "rewriter", `printf '%s' "REWRITTEN $1"`)
		setProcessors(t, mock)
		reeval := func(command string) evalcontract.Decision {
			if command != "REWRITTEN git status" {
				t.Errorf("reeval called with %q, want the rewritten command", command)
			}
			// Looser than the original (Approve) is impossible since Approve
			// is already the loosest value — same-strictness case: Approve
			// re-evaluates to Approve too.
			return evalcontract.Approve
		}
		cmd, applied, decision := ProcessReevaluated("git status", mainSessionPayload, evalcontract.Approve, reeval)
		if !applied {
			t.Error("applied = false, want true (rewrite is no stricter than original)")
		}
		if cmd != "REWRITTEN git status" {
			t.Errorf("finalCommand = %q, want the rewritten text", cmd)
		}
		if decision != evalcontract.Approve {
			t.Errorf("decision = %v, want Approve (original — P10 never derives the decision from the rewrite)", decision)
		}
	})

	t.Run("rewrite re-evaluates STRICTER: rewrite discarded, ORIGINAL verdict emitted on the ORIGINAL command", func(t *testing.T) {
		mock := writeMockProcessor(t, "rewriter", `printf '%s' "rm -rf $1"`)
		setProcessors(t, mock)
		reevalCalled := false
		reeval := func(command string) evalcontract.Decision {
			reevalCalled = true
			if command != "rm -rf git status" {
				t.Errorf("reeval called with %q, want the rewritten command", command)
			}
			return evalcontract.Reject // stricter than the original Approve
		}
		cmd, applied, decision := ProcessReevaluated("git status", mainSessionPayload, evalcontract.Approve, reeval)
		if !reevalCalled {
			t.Fatal("reeval was never called")
		}
		if applied {
			t.Error("applied = true, want false — a stricter rewrite must be discarded, not forwarded to execution")
		}
		if cmd != "git status" {
			t.Errorf("finalCommand = %q, want the ORIGINAL command (the rewrite must never reach the caller)", cmd)
		}
		if decision != evalcontract.Approve {
			t.Errorf("decision = %v, want the ORIGINAL verdict (Approve), not the rewrite's stricter Reject", decision)
		}
	})

	t.Run("a rewrite never turns non-Approve into Approve, even when the rewrite re-evaluates to Approve", func(t *testing.T) {
		mock := writeMockProcessor(t, "rewriter", `printf '%s' "SAFE-LOOKING $1"`)
		setProcessors(t, mock)
		reeval := func(command string) evalcontract.Decision {
			// The rewritten text looks entirely safe on its own — but the
			// ORIGINAL command was already non-Approve (Abstain), and P10
			// forbids a rewrite from ever turning that into an Approve.
			return evalcontract.Approve
		}
		for _, original := range []evalcontract.Decision{evalcontract.Abstain, evalcontract.Reject} {
			cmd, applied, decision := ProcessReevaluated("curl http://internal", mainSessionPayload, original, reeval)
			if decision != original {
				t.Errorf("original=%v: decision = %v, want %v unchanged — a rewrite must never turn a non-Approve original into Approve", original, decision, original)
			}
			if decision == evalcontract.Approve {
				t.Fatalf("original=%v: decision flipped to Approve — P10 violated", original)
			}
			// Since the rewrite's own re-evaluation (Approve) is NOT
			// stricter than a non-Approve original, the rewrite is still
			// APPLIED (the text substitution happens) — only the DECISION
			// is pinned to the original, per ProcessReevaluated's own
			// contract ("a looser rewritten verdict only ever affects
			// whether the TEXT is substituted, never the DECISION").
			if !applied {
				t.Errorf("original=%v: applied = false, want true (rewrite is not stricter than original)", original)
			}
			if cmd != "SAFE-LOOKING curl http://internal" {
				t.Errorf("original=%v: finalCommand = %q, want the rewritten text", original, cmd)
			}
		}
	})

	t.Run("nil reeval: rewrite always applied, original decision kept (Process's pre-P10 behavior)", func(t *testing.T) {
		mock := writeMockProcessor(t, "rewriter", `printf '%s' "X $1"`)
		setProcessors(t, mock)
		cmd, applied, decision := ProcessReevaluated("echo hi", mainSessionPayload, evalcontract.Approve, nil)
		if !applied || cmd != "X echo hi" || decision != evalcontract.Approve {
			t.Errorf("got (%q, %v, %v), want (%q, true, Approve)", cmd, applied, decision, "X echo hi")
		}
	})
}

// TestDecisionStricter pins the total order stricter/decisionRank apply:
// Reject > Ask > Abstain > Approve.
func TestDecisionStricter(t *testing.T) {
	order := []evalcontract.Decision{evalcontract.Approve, evalcontract.Abstain, evalcontract.Ask, evalcontract.Reject}
	for i, looser := range order {
		for j, tighter := range order {
			want := i < j
			if got := stricter(tighter, looser); got != want {
				t.Errorf("stricter(%v, %v) = %v, want %v", tighter, looser, got, want)
			}
		}
	}
}
