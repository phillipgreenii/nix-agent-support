package hookio

import "errors"

// Verdict collapses a RuleModule.Evaluate pair into the single verdict a chain of
// EXACTLY THAT ONE RULE would produce, so a caller holding one rule (a unit test,
// or an adapter that owns no chain) can reason about it the way the engine does.
//
// It mirrors engine.Engine.Evaluate's chokepoint semantics on a one-element chain:
//
//   - nil error -> the rule handled the input; its RuleResult IS the verdict.
//   - ErrNotApplicable or any other error -> the rule did not decide, the chain has
//     nothing left to try, and an exhausted chain manufactures the TERMINAL
//     NoOpinion. Empty Reason/Module, matching what the engine manufactures.
//
// Because it is variadic-free it composes directly with the two-value call:
//
//	got := hookio.Verdict(rule.Evaluate(input))
//
// SCOPE: this is for unit tests and for callers that hold a single rule. Production
// decision paths MUST go through engine.Engine.EvaluateHook, because Verdict
// DISCARDS the error — it cannot record a genuine failure per rule, and it cannot
// honour a rule's fail-closed carve-out beyond whatever RuleResult that rule
// already returned. TestVerdictHasNoProductionCallers pins that scope
// mechanically.
// PROVENANCE (ADR 0044) is set the same way engine.Evaluate sets it, so a one-rule
// chain classifies identically to the real one. The Decision, Reason and Module of
// every outcome are BYTE-IDENTICAL to before this change — only the new field moves —
// so no existing assertion is affected:
//
//   - ErrRefused: the rule did not decide, but its FLOOR is a real contribution, so the
//     manufactured verdict is folded WITH it exactly as the engine folds it. Discarding
//     it here would make a one-rule chain disagree with the engine on every refusal —
//     an Ask floor would read as an abstain — which is the whole reason this function
//     exists.
//   - ErrNotApplicable: nothing claimed the input and nothing failed — the one
//     legitimate EXHAUSTION.
//   - any other error: a genuine failure is NOT an exhaustion. Reporting one as such
//     would let a systematically-failing resolver clear bodies wholesale, which is the
//     fail-safe default's whole purpose.
func Verdict(res RuleResult, err error) RuleResult {
	if err != nil {
		// ErrRefused matches ErrNotApplicable under errors.Is (see refusalError), so
		// this order is load-bearing: the specific case MUST be tested first.
		if errors.Is(err, ErrRefused) {
			// The floor is `current`, so its Reason survives a tie with the reason-less
			// manufactured verdict — matching engine.Evaluate's exhaustion fold.
			//
			// Provenance: ProvenanceExhaustion on the manufactured candidate (pg2-4x2mu)
			// is REQUIRED, not cosmetic, now that RefusalCategory exists.
			// engine.Evaluate's REAL loop-exhaustion seed always attempts
			// ProvenanceExhaustion and relies on the SUBSEQUENT tie-merge to downgrade
			// it to Refusal when `res` is a genuine refusal — see mergeRefusalCategory's
			// doc for why a bare zero-value candidate (Provenance defaulting to
			// ProvenanceRefusal) would be misread as a SECOND, competing refusal with
			// RefusalCategoryUnspecified and silently AND the real category away. This
			// mirrors the ErrNotApplicable branch below, which already sets it
			// explicitly for the identical reason.
			return MostRestrictive(res, RuleResult{Decision: NoOpinion, Provenance: ProvenanceExhaustion})
		}
		if errors.Is(err, ErrNotApplicable) {
			return RuleResult{Decision: NoOpinion, Provenance: ProvenanceExhaustion}
		}
		return RuleResult{Decision: NoOpinion}
	}
	return res
}
