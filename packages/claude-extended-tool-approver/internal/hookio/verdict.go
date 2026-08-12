package hookio

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
func Verdict(res RuleResult, err error) RuleResult {
	if err != nil {
		return RuleResult{Decision: NoOpinion}
	}
	return res
}
