package hookio

// ADR 0044's VOCABULARY TESTS: the provenance channel and the refusal outcome.
//
// Everything here is pure and cheap, and it guards the two properties the rest of the
// tree relies on without being able to see them:
//
//  1. ProvenanceRefusal is the ZERO VALUE, so an unannotated RuleResult is a refusal.
//     Reversing that constant order would silently make every one of the ~150 existing
//     RuleResult literals claim to be an exhaustion, and the only consumer of an
//     exhaustion is a CLEARING decision — so the reversal is approval-widening and no
//     behavioural test elsewhere would name it.
//  2. ErrRefused matches ErrNotApplicable and nothing else matches either. That is what
//     lets 31 converted rule sites keep every existing errors.Is consumer working, and
//     what makes an un-upgraded consumer lose only the floor instead of mis-reading a
//     refusal as a verdict.

import (
	"errors"
	"fmt"
	"testing"
)

// TestProvenanceZeroValueIsRefusal pins property 1 above. It asserts the ZERO VALUE
// rather than the constant's numeric value, because the zero value is the thing every
// unannotated literal in the tree gets.
func TestProvenanceZeroValueIsRefusal(t *testing.T) {
	var zero Provenance
	if zero != ProvenanceRefusal {
		t.Fatalf("zero Provenance = %v, want ProvenanceRefusal — the fail-safe default is the whole design (ADR 0044)", zero)
	}
	if (RuleResult{}).Provenance != ProvenanceRefusal {
		t.Fatal("zero RuleResult does not carry ProvenanceRefusal; an unannotated site would claim to be an exhaustion")
	}
	if (RuleResult{Decision: NoOpinion}).Provenance != ProvenanceRefusal {
		t.Fatal("a bare NoOpinion literal does not carry ProvenanceRefusal")
	}
	// NotApplicable's RuleResult is IGNORED by the engine, but if a consumer ever reads
	// it the fail-safe reading must be the one it gets.
	res, _ := NotApplicable()
	if res.Provenance != ProvenanceRefusal {
		t.Fatal("NotApplicable()'s RuleResult claims an exhaustion")
	}
	// And the refusal helper, whose whole job is to be a refusal.
	floor, _ := Refused("m", "r")
	if floor.Provenance != ProvenanceRefusal || floor.Decision != NoOpinion {
		t.Fatalf("Refused() = %+v, want a NoOpinion refusal", floor)
	}
}

// TestRefusalIsANotApplicableSubtype pins property 2 in BOTH directions, which is what
// separates a deliberate subtype claim from the wrap ADR 0043's Decision point 5
// forbids. The dangerous direction is the third assertion: if a GENUINE failure ever
// matched ErrNotApplicable, the engine would file it as "absent" and the rule's failure
// would become a silent non-event.
func TestRefusalIsANotApplicableSubtype(t *testing.T) {
	if !errors.Is(ErrRefused, ErrNotApplicable) {
		t.Error("errors.Is(ErrRefused, ErrNotApplicable) = false; every consumer that only knows ErrNotApplicable would treat a refusal as a FAILURE and record it in the error sink")
	}
	if errors.Is(ErrNotApplicable, ErrRefused) {
		t.Error("errors.Is(ErrNotApplicable, ErrRefused) = true; a plain not-applicable would be read as an examined refusal and would floor every leaf it touches")
	}
	genuine := fmt.Errorf("read bash command: %w", errors.New("bad json"))
	if errors.Is(genuine, ErrNotApplicable) || errors.Is(genuine, ErrRefused) {
		t.Error("a genuine rule failure matches one of the control sentinels; ADR 0043 decision 5's second failure mode")
	}
	if !errors.Is(ErrRefused, ErrRefused) {
		t.Error("ErrRefused does not match itself")
	}
}

// TestMostRestrictiveMergesProvenanceConservatively is the merge truth table.
//
// The load-bearing row is the last pair: an exhaustion tied with a refusal must be a
// refusal WHICHEVER WAY ROUND it is folded. `MostRestrictive` keeps `current` on a tie,
// so without the merge the answer would depend on the caller's fold order and
// `seq 1 3 && cat <<EOF` would classify differently from `cat <<EOF && seq 1 3`.
func TestMostRestrictiveMergesProvenanceConservatively(t *testing.T) {
	exh := func(d Decision) RuleResult {
		return RuleResult{Decision: d, Provenance: ProvenanceExhaustion}
	}
	ref := func(d Decision) RuleResult { return RuleResult{Decision: d} }

	tests := []struct {
		name             string
		current, cand    RuleResult
		wantDecision     Decision
		wantProvenance   Provenance
		alsoTryReversed  bool
		reversedSameProv bool
	}{
		{
			name: "both exhaustion, tie", current: exh(NoOpinion), cand: exh(NoOpinion),
			wantDecision: NoOpinion, wantProvenance: ProvenanceExhaustion,
			alsoTryReversed: true, reversedSameProv: true,
		},
		{
			name: "exhaustion tied with refusal", current: exh(NoOpinion), cand: ref(NoOpinion),
			wantDecision: NoOpinion, wantProvenance: ProvenanceRefusal,
			alsoTryReversed: true, reversedSameProv: true,
		},
		{
			name: "candidate strictly wins, carries its own provenance", current: ref(Approve), cand: exh(NoOpinion),
			wantDecision: NoOpinion, wantProvenance: ProvenanceExhaustion,
		},
		{
			name: "candidate strictly loses and does NOT taint", current: exh(NoOpinion), cand: ref(Approve),
			wantDecision: NoOpinion, wantProvenance: ProvenanceExhaustion,
		},
		{
			name: "a refusal Ask beats an exhaustion NoOpinion", current: exh(NoOpinion), cand: ref(Ask),
			wantDecision: Ask, wantProvenance: ProvenanceRefusal,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MostRestrictive(tt.current, tt.cand)
			if got.Decision != tt.wantDecision || got.Provenance != tt.wantProvenance {
				t.Errorf("MostRestrictive = %s/%s, want %s/%s",
					got.Decision, got.Provenance, tt.wantDecision, tt.wantProvenance)
			}
			if tt.alsoTryReversed {
				rev := MostRestrictive(tt.cand, tt.current)
				if tt.reversedSameProv && rev.Provenance != tt.wantProvenance {
					t.Errorf("reversed fold provenance = %s, want %s — the fold is order-dependent",
						rev.Provenance, tt.wantProvenance)
				}
			}
		})
	}
}

// TestNeutralApproveSeedsDoNotTaintAFold is the regression guard for the ONE way the
// merge could have been written wrong and still passed the table above.
//
// The engine's folds are seeded with neutral Approves ("no redirections to evaluate",
// "no substitutions to evaluate", "all sub-commands approved"), and every one of them
// carries the zero-value REFUSAL provenance. If the merge ran on a strict loss as well
// as on a tie, each of those seeds would taint the fold and NO expression could ever
// report an exhaustion — the channel would be dead on arrival while every test that
// only checks Decision kept passing.
func TestNeutralApproveSeedsDoNotTaintAFold(t *testing.T) {
	result := RuleResult{Decision: Approve, Reason: "all sub-commands approved"}
	result = MostRestrictive(result, RuleResult{Decision: NoOpinion, Provenance: ProvenanceExhaustion})
	result = MostRestrictive(result, RuleResult{Decision: Approve, Reason: "no redirections to evaluate"})
	result = MostRestrictive(result, RuleResult{Decision: Approve, Reason: "no substitutions to evaluate"})
	if result.Provenance != ProvenanceExhaustion {
		t.Fatal("neutral Approve seeds tainted an exhaustion fold to a refusal; the exhaustion channel would never fire")
	}
}

// TestVerdictClassifiesEachOutcome pins hookio.Verdict against engine.Evaluate's
// chokepoint on a one-rule chain, outcome by outcome.
//
// The refusal row is the one that matters: the floor must be FOLDED, not discarded. An
// Ask floor discarded here reads as an abstain, so a unit test would report a rule as
// non-gating while the engine gates — the exact disagreement this helper exists to
// prevent.
func TestVerdictClassifiesEachOutcome(t *testing.T) {
	if got := Verdict(RuleResult{Decision: Ask, Reason: "r"}, nil); got.Decision != Ask || got.Reason != "r" {
		t.Errorf("handled: %+v, want the rule's own verdict verbatim", got)
	}
	if got := Verdict(NotApplicable()); got.Decision != NoOpinion || got.Provenance != ProvenanceExhaustion {
		t.Errorf("not-applicable: %s/%s, want abstain/exhaustion", got.Decision, got.Provenance)
	}
	if got := Verdict(Refused("m", "why")); got.Decision != NoOpinion || got.Provenance != ProvenanceRefusal || got.Reason != "why" {
		t.Errorf("NoOpinion refusal: %s/%s/%q, want abstain/refusal/\"why\"", got.Decision, got.Provenance, got.Reason)
	}
	askFloor, err := Refuse(RuleResult{Decision: Ask, Reason: "needs a person", Module: "m"})
	if got := Verdict(askFloor, err); got.Decision != Ask || got.Reason != "needs a person" {
		t.Errorf("Ask refusal: %s/%q, want ask/\"needs a person\" — the floor was discarded", got.Decision, got.Reason)
	}
	if got := Verdict(RuleResult{}, errors.New("resolver exploded")); got.Decision != NoOpinion || got.Provenance != ProvenanceExhaustion {
		if got.Provenance == ProvenanceExhaustion {
			t.Error("a genuine failure was classified as an EXHAUSTION; a systematically-failing resolver could then clear bodies wholesale")
		}
		if got.Decision != NoOpinion {
			t.Errorf("genuine failure: %s, want abstain", got.Decision)
		}
	}
}

// FuzzProvenanceFoldIsConservativeAndOrderIndependent is the fuzz half of ADR 0044's
// classification invariant, at the level where the classification is COMBINED.
//
// It asserts two properties over an arbitrary sequence of folded results:
//
//  1. CONSERVATISM. The folded result may claim an exhaustion only if every input that
//     TIED at the winning decision claimed one. Stated as the contrapositive the fuzzer
//     can check cheaply: if any input at the winning decision was a refusal, the fold
//     must be a refusal. A fold that reported an exhaustion where a refusal took part is
//     approval-widening, because the only consumer of an exhaustion is a clearing
//     decision.
//  2. ORDER INDEPENDENCE. Folding the same multiset in reverse must give the same
//     provenance. `MostRestrictive` keeps `current` on a tie, so this is the property
//     that would break first if the merge were dropped, and it is invisible to any test
//     that folds in one order only.
func FuzzProvenanceFoldIsConservativeAndOrderIndependent(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3})
	f.Add([]byte{2, 3})
	f.Add([]byte{1, 1, 1})
	f.Add([]byte{})

	// decode maps one fuzz byte to a (Decision, Provenance) pair. Two bits are enough
	// and the mapping is total, so no input is rejected.
	decode := func(b byte) RuleResult {
		res := RuleResult{Decision: Decision(int(b>>1) % 4)}
		if b&1 == 1 {
			res.Provenance = ProvenanceExhaustion
		}
		return res
	}
	fold := func(in []RuleResult) RuleResult {
		out := RuleResult{Decision: Approve}
		for _, r := range in {
			out = MostRestrictive(out, r)
		}
		return out
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		results := make([]RuleResult, 0, len(data))
		for _, b := range data {
			results = append(results, decode(b))
		}
		got := fold(results)

		// (1) conservatism, checked against the inputs that tie at the winning decision.
		if got.Provenance == ProvenanceExhaustion {
			for _, r := range results {
				if r.Decision == got.Decision && r.Provenance != ProvenanceExhaustion {
					t.Fatalf("fold of %v reported an EXHAUSTION while a refusal (%s) tied at the winning decision %s",
						data, r.Decision, got.Decision)
				}
			}
		}

		// (2) order independence.
		reversed := make([]RuleResult, len(results))
		for i, r := range results {
			reversed[len(results)-1-i] = r
		}
		if rev := fold(reversed); rev.Provenance != got.Provenance {
			t.Fatalf("fold of %v is order-dependent: forward %s, reverse %s", data, got.Provenance, rev.Provenance)
		}
	})
}
