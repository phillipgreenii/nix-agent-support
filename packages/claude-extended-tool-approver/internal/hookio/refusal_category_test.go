package hookio

// RefusalCategory's vocabulary tests (pg2-4x2mu): the structured "what kind of
// refusal is this" channel a consumer (envvars) may act on WITHOUT gating on a
// reason string or an env var NAME. Same discipline as ADR 0044's Provenance
// tests in adr0044_test.go, one level narrower.

import (
	"errors"
	"testing"
)

// TestRefusalCategoryZeroValueIsUnspecified pins the fail-safe default: an
// unannotated RuleResult — and every one of the ~150 pre-existing literals in
// this tree — reads as RefusalCategoryUnspecified, never as the one narrow
// category a consumer is authorized to relieve.
func TestRefusalCategoryZeroValueIsUnspecified(t *testing.T) {
	var zero RefusalCategory
	if zero != RefusalCategoryUnspecified {
		t.Fatalf("zero RefusalCategory = %v, want RefusalCategoryUnspecified", zero)
	}
	if (RuleResult{}).RefusalCategory != RefusalCategoryUnspecified {
		t.Fatal("zero RuleResult does not carry RefusalCategoryUnspecified")
	}
	floor, _ := Refused("m", "r")
	if floor.RefusalCategory != RefusalCategoryUnspecified {
		t.Fatalf("Refused() = %+v, want RefusalCategoryUnspecified — Refused must stay the uncategorized common case", floor)
	}
}

// TestRefusedWithCategory pins RefusedWithCategory's shape: a NoOpinion floor,
// zero-value Provenance (ProvenanceRefusal — the same fail-safe orientation
// Refused already documents), the caller's Module/Reason, and the declared
// category riding along.
func TestRefusedWithCategory(t *testing.T) {
	res, err := RefusedWithCategory("safe-commands", "has a dynamically-expanded path arg $D", RefusalCategoryDynamicPathRead)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("RefusedWithCategory returned err=%v, want ErrRefused", err)
	}
	if res.Decision != NoOpinion {
		t.Errorf("Decision = %s, want abstain", res.Decision)
	}
	if res.Provenance != ProvenanceRefusal {
		t.Errorf("Provenance = %s, want refusal", res.Provenance)
	}
	if res.RefusalCategory != RefusalCategoryDynamicPathRead {
		t.Errorf("RefusalCategory = %v, want RefusalCategoryDynamicPathRead", res.RefusalCategory)
	}
	if res.Module != "safe-commands" || res.Reason != "has a dynamically-expanded path arg $D" {
		t.Errorf("Module/Reason = %q/%q, want the caller's own values", res.Module, res.Reason)
	}
}

// TestMostRestrictiveMergesRefusalCategoryConservatively is
// TestMostRestrictiveMergesProvenanceConservatively's sibling truth table for the
// new channel. THE LOAD-BEARING ROW is "dynamic-path-read tied with a
// manufactured exhaustion": engine.Evaluate's loop-exhaustion manufactures a bare
// `RuleResult{Decision: NoOpinion, Provenance: ProvenanceExhaustion}` with NO
// opinion on RefusalCategory at all (it stays the zero value). When a
// dynamic-path-read refusal is the ONLY thing that examined a leaf and every
// other rule answers "not applicable" — precisely envvars' pg2-4x2mu relief
// target — that manufactured verdict ties with the refusal's floor, and the
// category MUST survive the tie: an AND over the bare categories would read the
// manufactured seed's zero-value "no opinion" as an affirmative "not
// dynamic-path-read" and silently defeat the relief. mergeRefusalCategory's doc
// explains why this requires consulting Provenance, not just the categories.
func TestMostRestrictiveMergesRefusalCategoryConservatively(t *testing.T) {
	dpr := func(d Decision) RuleResult {
		return RuleResult{Decision: d, Provenance: ProvenanceRefusal, RefusalCategory: RefusalCategoryDynamicPathRead}
	}
	unspec := func(d Decision) RuleResult {
		return RuleResult{Decision: d, Provenance: ProvenanceRefusal}
	}
	exhausted := func(d Decision) RuleResult {
		return RuleResult{Decision: d, Provenance: ProvenanceExhaustion}
	}

	tests := []struct {
		name          string
		current, cand RuleResult
		wantDecision  Decision
		wantCategory  RefusalCategory
	}{
		{
			name: "both dynamic-path-read, tie", current: dpr(NoOpinion), cand: dpr(NoOpinion),
			wantDecision: NoOpinion, wantCategory: RefusalCategoryDynamicPathRead,
		},
		{
			name: "dynamic-path-read tied with unspecified refusal", current: dpr(NoOpinion), cand: unspec(NoOpinion),
			wantDecision: NoOpinion, wantCategory: RefusalCategoryUnspecified,
		},
		{
			// THE LOAD-BEARING ROW — see the function doc.
			name: "dynamic-path-read tied with a manufactured exhaustion survives", current: dpr(NoOpinion), cand: exhausted(NoOpinion),
			wantDecision: NoOpinion, wantCategory: RefusalCategoryDynamicPathRead,
		},
		{
			name: "candidate strictly wins, carries its own category", current: unspec(Approve), cand: dpr(NoOpinion),
			wantDecision: NoOpinion, wantCategory: RefusalCategoryDynamicPathRead,
		},
		{
			name: "candidate strictly loses and does NOT taint", current: dpr(NoOpinion), cand: unspec(Approve),
			wantDecision: NoOpinion, wantCategory: RefusalCategoryDynamicPathRead,
		},
		{
			name: "a Reject beats a dynamic-path-read NoOpinion, category irrelevant at Reject", current: dpr(NoOpinion), cand: RuleResult{Decision: Reject},
			wantDecision: Reject, wantCategory: RefusalCategoryUnspecified,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MostRestrictive(tt.current, tt.cand)
			if got.Decision != tt.wantDecision || got.RefusalCategory != tt.wantCategory {
				t.Errorf("MostRestrictive = %s/%v, want %s/%v",
					got.Decision, got.RefusalCategory, tt.wantDecision, tt.wantCategory)
			}
			rev := MostRestrictive(tt.cand, tt.current)
			if rev.Decision == got.Decision && rev.RefusalCategory != got.RefusalCategory {
				t.Errorf("reversed fold category = %v, want %v — the fold is order-dependent", rev.RefusalCategory, got.RefusalCategory)
			}
		})
	}
}

// TestVerdictPreservesRefusalCategory pins the fix Verdict needed once
// RefusalCategory existed: its ErrRefused branch manufactures a candidate to
// simulate the engine's own loop-exhaustion seed, and that candidate MUST claim
// ProvenanceExhaustion — a bare `RuleResult{Decision: NoOpinion}` defaults to
// ProvenanceRefusal (the zero value) and would be misread by
// mergeRefusalCategory as a SECOND, real, uncategorized refusal, ANDing the true
// category away on every single-rule call site that uses hookio.Verdict (which
// is most of this tree's unit tests, including safecmds').
func TestVerdictPreservesRefusalCategory(t *testing.T) {
	res, err := RefusedWithCategory("safe-commands", "has a dynamically-expanded path arg $D", RefusalCategoryDynamicPathRead)
	got := Verdict(res, err)
	if got.Decision != NoOpinion {
		t.Fatalf("precondition: got %s, want abstain", got.Decision)
	}
	if got.RefusalCategory != RefusalCategoryDynamicPathRead {
		t.Errorf("Verdict lost the RefusalCategory: got %v, want RefusalCategoryDynamicPathRead", got.RefusalCategory)
	}
}
