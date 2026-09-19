package router

import (
	"encoding/json"
	"testing"
)

// permissionValues enumerates the five permissionDecision-relevant outcomes
// a single delegate call can contribute to the merge: the four recognized
// decisions plus "abstain" (an empty PermissionDecision, i.e. {} / no
// opinion) — ADR 0071 §4 Phase B, B1 "Merge policy table".
var permissionValues = []string{"allow", "ask", "defer", "deny", "abstain"}

// permissionRankForTest mirrors permissionRank but also assigns "abstain" a
// rank of 0 (lower than every real decision), purely so the expected-winner
// computation below can be expressed as a single "highest rank wins, first
// contributor breaks a tie" rule without special-casing the empty string.
func permissionRankForTest(v string) int {
	if v == "abstain" {
		return 0
	}
	return permissionRank[v]
}

func hookOutputFor(v string) HookOutput {
	if v == "abstain" {
		return HookOutput{}
	}
	return HookOutput{PermissionDecision: v}
}

// expectedWinner returns what mergeState must end up with after folding in,
// in order, the permissionDecision-relevant outcomes in vs — the strict
// "most restrictive wins" order deny > defer > ask > allow, with abstain
// contributing nothing and, among equal ranks, whichever contributor was
// folded in first (mergeState.apply only overwrites on a STRICTLY higher
// rank) — this matters only in principle, since equal rank already implies
// the same decision string.
func expectedWinner(vs []string) string {
	best := ""
	bestRank := -1
	for _, v := range vs {
		r := permissionRankForTest(v)
		if v == "abstain" {
			continue
		}
		if r > bestRank {
			bestRank = r
			best = v
		}
	}
	return best
}

// TestMergePermissionDecisionTableAllOrderedPairs is the exhaustive
// {allow,ask,deny,defer,abstain}^2 = 25-case table for a 2-delegate chain,
// exercised directly against mergeState (the same merge logic
// Dispatch.apply() uses) per ADR 0071 §4 Phase B, B1 "Merge policy table" —
// corrected count: defer is its own rank, not tied with ask. The
// through-real-Dispatch version of this same table lives in
// dispatch_test.go's TestDispatchPermissionMergeTableThroughRealDispatch;
// this one is the fast, exhaustive version.
func TestMergePermissionDecisionTableAllOrderedPairs(t *testing.T) {
	count := 0
	for _, first := range permissionValues {
		for _, second := range permissionValues {
			count++
			t.Run(first+"_then_"+second, func(t *testing.T) {
				var m mergeState
				m.apply("decide", hookOutputFor(first))
				m.apply("decide", hookOutputFor(second))

				want := expectedWinner([]string{first, second})
				got := m.result().PermissionDecision
				if got != want {
					t.Errorf("apply(%q) then apply(%q): permissionDecision = %q, want %q", first, second, got, want)
				}
			})
		}
	}
	if count != 25 {
		t.Fatalf("exercised %d ordered pairs, want exactly 25 ({allow,ask,deny,defer,abstain}^2)", count)
	}
}

// TestMergeMostRestrictiveWinsExhaustiveThreeDelegateChains is the
// "equivalently exhaustive" stand-in for a property-based test over 3+
// delegate chains: every one of the 5^3 = 125 ordered triples of
// {allow,ask,deny,defer,abstain} must fold to the single most restrictive
// decision present (or no opinion at all if every delegate abstained) —
// ADR 0071 §4 Phase B, B1 "Merge policy table" property-based-strategy
// clause.
func TestMergeMostRestrictiveWinsExhaustiveThreeDelegateChains(t *testing.T) {
	count := 0
	for _, a := range permissionValues {
		for _, b := range permissionValues {
			for _, c := range permissionValues {
				count++
				chain := []string{a, b, c}
				var m mergeState
				for _, v := range chain {
					m.apply("decide", hookOutputFor(v))
				}
				want := expectedWinner(chain)
				got := m.result().PermissionDecision
				if got != want {
					t.Errorf("chain %v: permissionDecision = %q, want %q (most restrictive wins)", chain, got, want)
				}
			}
		}
	}
	if count != 125 {
		t.Fatalf("exercised %d ordered triples, want exactly 125 (5^3)", count)
	}
}

// TestMergeMostRestrictiveWinsFiveDelegateChainSample extends the "most
// restrictive wins" invariant to a longer chain (5 delegates) on a
// representative sample of orderings, rather than the full 5^5 = 3125
// exhaustive grid, since the 3-delegate exhaustive test above already
// proves the pairwise-fold logic is order-independent-of-rank.
func TestMergeMostRestrictiveWinsFiveDelegateChainSample(t *testing.T) {
	samples := [][]string{
		{"allow", "allow", "allow", "allow", "deny"},
		{"deny", "allow", "allow", "allow", "allow"},
		{"abstain", "abstain", "abstain", "abstain", "abstain"},
		{"ask", "abstain", "defer", "abstain", "allow"},
		{"defer", "deny", "ask", "allow", "abstain"},
	}
	for _, chain := range samples {
		var m mergeState
		for _, v := range chain {
			m.apply("decide", hookOutputFor(v))
		}
		want := expectedWinner(chain)
		got := m.result().PermissionDecision
		if got != want {
			t.Errorf("chain %v: permissionDecision = %q, want %q", chain, got, want)
		}
	}
}

// TestMergeRewriteAndDecisionFieldsKeepLatestContribution covers the
// non-permission merge fields' own semantics: updatedInput,
// updatedToolOutput and decision.updatedInput each keep the LATEST
// contributing delegate's value (not the first, and not a merge of both),
// mirroring how advanceCumulativePayload already hands each next delegate
// the current cumulative rewrite.
func TestMergeRewriteAndDecisionFieldsKeepLatestContribution(t *testing.T) {
	var m mergeState
	m.apply("rewrite", HookOutput{UpdatedInput: json.RawMessage(`{"v":1}`)})
	m.apply("rewrite", HookOutput{UpdatedInput: json.RawMessage(`{"v":2}`)})
	m.apply("rewrite", HookOutput{UpdatedToolOutput: json.RawMessage(`{"v":"first"}`)})
	m.apply("rewrite", HookOutput{UpdatedToolOutput: json.RawMessage(`{"v":"second"}`)})
	m.apply("decide", HookOutput{Decision: &DecisionField{UpdatedInput: json.RawMessage(`{"v":"a"}`)}})
	m.apply("decide", HookOutput{Decision: &DecisionField{UpdatedInput: json.RawMessage(`{"v":"b"}`)}})

	out := m.result()
	if string(out.UpdatedInput) != `{"v":2}` {
		t.Errorf("UpdatedInput = %s, want the latest contributor's value", out.UpdatedInput)
	}
	if string(out.UpdatedToolOutput) != `{"v":"second"}` {
		t.Errorf("UpdatedToolOutput = %s, want the latest contributor's value", out.UpdatedToolOutput)
	}
	if out.Decision == nil || string(out.Decision.UpdatedInput) != `{"v":"b"}` {
		t.Errorf("Decision.UpdatedInput = %v, want the latest contributor's value", out.Decision)
	}
}

// TestMergeAdditionalContextConcatenatesInDispatchOrder covers the
// annotate-contract field: every applied delegate's AdditionalContext is
// kept, joined by "\n" in the order apply() was called (dispatch order).
func TestMergeAdditionalContextConcatenatesInDispatchOrder(t *testing.T) {
	var m mergeState
	m.apply("annotate", HookOutput{AdditionalContext: "first"})
	m.apply("annotate", HookOutput{AdditionalContext: "second"})
	m.apply("annotate", HookOutput{}) // an abstain in between contributes nothing
	m.apply("annotate", HookOutput{AdditionalContext: "third"})

	want := "first\nsecond\nthird"
	if got := m.result().AdditionalContext; got != want {
		t.Errorf("AdditionalContext = %q, want %q", got, want)
	}
}

// TestMergeRetryIsStickyTrueOnceSet covers PermissionDenied's Retry field:
// once any delegate sets retry=true, it stays true even if a later delegate
// in the same chain contributes nothing about retry at all (mergeState has
// no way to un-set it — there is no "retry=false" contribution modeled).
func TestMergeRetryIsStickyTrueOnceSet(t *testing.T) {
	trueVal := true
	var m mergeState
	m.apply("decide", HookOutput{Retry: &trueVal})
	m.apply("decide", HookOutput{})

	out := m.result()
	if out.Retry == nil || !*out.Retry {
		t.Errorf("Retry = %v, want a sticky true", out.Retry)
	}
}

// TestMergeResultOfNothingAppliedIsTheZeroHookOutput covers the router's own
// fail-safe/no-opinion abstain shape: a mergeState with nothing folded in at
// all renders as {} (IsZero() true).
func TestMergeResultOfNothingAppliedIsTheZeroHookOutput(t *testing.T) {
	var m mergeState
	if got := m.result(); !got.IsZero() {
		t.Errorf("result() of an empty mergeState = %#v, want the zero HookOutput", got)
	}
}
