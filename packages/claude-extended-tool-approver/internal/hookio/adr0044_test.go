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

// TestADR0044_FromRecursionForwardsAnInnerRefusal is pg2-ij9sr's core assertion: the
// recursion boundary no longer flattens an inner refusal into the outer chain's "not my
// business".
//
// WHY IT IS THE SAME DEFECT ADR 0044 FIXES, one level out. Before this, a rule that
// delegated (nix, docker, kubectl) dropped the inner floor, so the outer chain concluded
// its own terminal NoOpinion with NOTHING recording that anything had been refused — and
// the outer leaf therefore reported an EXHAUSTION, which is the half a consumer may act on
// to clear a body. Measured on this tree: `nix develop -c "git clean -fd"` classified as an
// exhaustion before and as a refusal after.
//
// THE TWO MUST NOT COLLAPSE IN EITHER DIRECTION, so both directions are asserted, and the
// exhaustion row is not a formality: forwarding an exhaustion as a refusal would floor
// every delegated leaf whose inner expression nobody models.
//
// IDENTITY SURVIVES THE HOP. The forwarded floor keeps the INNER rule's Module and Reason
// rather than being re-attributed to the delegating rule — that is what makes a trace read
// "safe-commands refused this" instead of "nix refused this", and it is the acceptance
// criterion's "preserving the refusing rule's identity".
func TestADR0044_FromRecursionForwardsAnInnerRefusal(t *testing.T) {
	inner := RuleResult{
		Decision: NoOpinion,
		Reason:   "safe-commands: rm references non-writable path /etc (deferred to claude-code)",
		Module:   "safe-commands",
		// Provenance left at its zero value ON PURPOSE: that IS ProvenanceRefusal, and the
		// fail-safe orientation is the point — only an EXPLICIT exhaustion claim may take
		// the not-applicable branch.
	}
	floor, err := FromRecursion(inner)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("inner refusal forwarded as %v, want ErrRefused — the outer chain would drop the floor and report an EXHAUSTION", err)
	}
	if floor.Decision != NoOpinion {
		t.Errorf("forwarded floor = %s, want abstain", floor.Decision)
	}
	if floor.Module != inner.Module || floor.Reason != inner.Reason {
		t.Errorf("forwarded floor = %+v, want the INNER rule's identity (%q / %q) — re-attributing it to the delegating rule loses the provenance",
			floor, inner.Module, inner.Reason)
	}
	if floor.Provenance != ProvenanceRefusal {
		t.Errorf("forwarded floor provenance = %s, want refusal", floor.Provenance)
	}
	// A refusal must still read as not-applicable to an un-upgraded consumer; that subtype
	// claim is what keeps every existing errors.Is caller working across this change, and it
	// is why adr0043_test.go's FromRecursion test passes UNMODIFIED.
	if !errors.Is(err, ErrNotApplicable) {
		t.Error("forwarded refusal does not match ErrNotApplicable; the outer engine would file it as a rule FAILURE")
	}

	// THE OTHER DIRECTION. An inner EXHAUSTION still forwards as an exhaustion: the
	// delegating rule formed no opinion, so the outer chain continues carrying nothing.
	exhausted := RuleResult{Decision: NoOpinion, Provenance: ProvenanceExhaustion, Module: "engine"}
	res, err := FromRecursion(exhausted)
	if errors.Is(err, ErrRefused) {
		t.Errorf("inner EXHAUSTION forwarded as a REFUSAL (%+v); every delegated leaf nobody models would be floored", res)
	}
	if !errors.Is(err, ErrNotApplicable) {
		t.Errorf("inner exhaustion forwarded as %v, want ErrNotApplicable", err)
	}
	// RuleResult holds a slice, so it is not comparable; the fields that matter are the
	// ones a consumer could mistake for a verdict.
	if res.Decision != Approve || res.Reason != "" || res.Module != "" || res.Provenance != ProvenanceRefusal {
		t.Errorf("inner exhaustion returned %+v, want the zero RuleResult — the engine ignores it and a non-zero value invites a consumer to read it", res)
	}

	// A genuine inner FAILURE also withdraws the exhaustion claim (engine.Evaluate's
	// sawFailure), so it arrives here as a refusal-provenance NoOpinion and MUST be floored
	// rather than dropped. Absence of evidence from a broken rule is not "nobody refused".
	broken := RuleResult{Decision: NoOpinion, Module: "engine"}
	if _, err := FromRecursion(broken); !errors.Is(err, ErrRefused) {
		t.Error("an inner chain in which a rule FAILED was forwarded as not-applicable; a broken resolver could then clear delegated bodies wholesale")
	}

	// An affirmative inner verdict is still forwarded VERBATIM and TERMINALLY. This is the
	// half pg2-ij9sr must not disturb: an inner Ask/Reject already stopped the outer chain,
	// and turning it into a floor would let a later rule's weaker verdict share the outcome.
	for _, d := range []Decision{Approve, Ask, Reject} {
		got, err := FromRecursion(RuleResult{Decision: d, Module: "inner", Reason: "r"})
		if err != nil {
			t.Errorf("inner %s forwarded with err=%v, want it terminal", d, err)
		}
		if got.Decision != d || got.Module != "inner" {
			t.Errorf("inner %s came back as %+v, want it verbatim", d, got)
		}
	}
}

// TestFromFoldNeverForwardsARefusal guards the ONE way pg2-ij9sr could have broken the
// whole ruleset, and it is a live regression guard rather than a hypothetical.
//
// FromRecursion and FromFold looked interchangeable before a refusal became forwardable,
// and envvars borrowed FromRecursion for its FOLD result. A fold's NoOpinion is the fold
// IDENTITY — "nothing in this leaf was mine" — and it carries no engine-assigned
// provenance: its zero value is ProvenanceRefusal only because the seed literal declares
// nothing. Read as a refusal, envvars' identity would floor EVERY leaf it folds over, and
// envvars reaches its identity for every ordinary `A=1 cmd` AND for every Bash leaf with no
// assignment at all — i.e. every Bash command in the corpus would abstain.
//
// So the two translations MUST stay distinguishable, and the property is asserted on the
// exact input that would have caused it: a NoOpinion with the zero-value provenance.
func TestFromFoldNeverForwardsARefusal(t *testing.T) {
	identity := RuleResult{Decision: NoOpinion, Module: "env-vars"}
	res, err := FromFold(identity)
	if errors.Is(err, ErrRefused) {
		t.Fatalf("a fold IDENTITY was forwarded as a refusal (%+v); every leaf the rule folds over would be floored", res)
	}
	if !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("fold identity forwarded as %v, want ErrNotApplicable", err)
	}
	if res.Decision != Approve || res.Reason != "" || res.Module != "" {
		t.Errorf("fold identity returned %+v, want the zero RuleResult", res)
	}

	// The SAME input through FromRecursion IS a refusal. Asserting the pair together is what
	// makes the split visible: a future edit that merged the two functions back would fail
	// here rather than silently at the chain level.
	if _, err := FromRecursion(identity); !errors.Is(err, ErrRefused) {
		t.Error("FromRecursion no longer forwards a refusal-provenance NoOpinion; the two translations have been merged the wrong way")
	}

	// An affirmative fold result is still forwarded verbatim and terminally — the decisive
	// Ask/Reject envvars produces for an injector variable must not become a floor.
	for _, d := range []Decision{Approve, Ask, Reject} {
		got, err := FromFold(RuleResult{Decision: d, Module: "env-vars", Reason: "r"})
		if err != nil || got.Decision != d {
			t.Errorf("fold %s = %+v (err=%v), want it forwarded verbatim and terminal", d, got, err)
		}
	}
}

// TestFromRecursionIsNeverLessRestrictiveThanFromFold states pg2-ij9sr's acceptance gate as
// a RELATION over the two translations, so it survives retuning of either.
//
// FromFold is exactly the pre-pg2-ij9sr behaviour, which makes it the BASELINE: for every
// possible input, the outcome FromRecursion produces must be at least as restrictive as the
// one the old translation produced. "At least as restrictive" at this boundary means the
// effective floor contributed to the outer chain, which hookio.Verdict computes — an
// ErrNotApplicable contributes nothing, a refusal contributes its floor. A row moving the
// other way would be the approval-widening direction the bead forbids.
func TestFromRecursionIsNeverLessRestrictiveThanFromFold(t *testing.T) {
	for _, d := range []Decision{Approve, NoOpinion, Ask, Reject} {
		for _, p := range []Provenance{ProvenanceRefusal, ProvenanceExhaustion} {
			in := RuleResult{Decision: d, Provenance: p, Module: "inner", Reason: "r"}
			// The chain contribution of each translation, read the way the engine reads it:
			// a bare not-applicable contributes the Approve identity (nothing), anything
			// else contributes its RuleResult.
			contribution := func(res RuleResult, err error) Decision {
				if err != nil && !errors.Is(err, ErrRefused) {
					return Approve
				}
				return res.Decision
			}
			recur := contribution(FromRecursion(in))
			fold := contribution(FromFold(in))
			if recur < fold {
				t.Errorf("input %s/%s: FromRecursion contributes %s but the pre-change translation contributed %s — that is the LESS-restrictive direction",
					d, p, recur, fold)
			}
		}
	}
}
