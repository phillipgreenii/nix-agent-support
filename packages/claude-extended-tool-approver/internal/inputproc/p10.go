package inputproc

import "github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"

// P10 (ADR 0075): "Input-processor rewrites are re-evaluated; a stricter
// verdict on the rewrite emits the original verdict without the rewrite; a
// rewrite never turns non-Approve into Approve."
//
// This file implements the RE-EVALUATION guarantee on top of the ordinary
// Process/processChain rewrite pipeline above. It is deliberately decoupled
// from the new engine's own package graph (claudecodeadapter/effectpolicy):
// this package is a leaf utility (see the package's own doc comments) with
// no other dependency on the hook wire format, and the new engine is not
// wired into cmd/claude-extended-tool-approver's hook handlers yet (Phase 5
// cutover, still pending as of this packet) — the real Reevaluator gets
// wired in at that point. It imports internal/evalcontract only, for the
// Decision vocabulary itself (a leaf package: evalcontract imports only
// internal/effectgraph), not the code that PRODUCES a Decision.

// Reevaluator judges a candidate Bash command TEXT on its own — independent
// of any hook input — into the new engine's verdict vocabulary
// (evalcontract.Decision). ProcessReevaluated's caller supplies the real
// one (eventually claudecodeadapter.Evaluate wired against a synthetic
// HookInput carrying the rewritten command); this package's own tests
// supply a deterministic fake.
type Reevaluator func(command string) evalcontract.Decision

// ProcessReevaluated runs the ordinary rewrite chain (Process) and then
// applies P10 on top of it.
//
// original is the decision the engine ALREADY computed for the pre-rewrite
// command — what Process's caller was about to act on before any rewrite
// happened. When Process produces no rewrite, that decision governs
// unchanged (applied is false, finalCommand is command itself).
//
// When Process DOES rewrite the command, reeval judges the REWRITTEN text
// on its own. If that judgment is STRICTER (more restrictive) than
// original, the rewrite is DISCARDED: applied is false and finalCommand is
// the ORIGINAL, unrewritten text — the caller must forward the command it
// already knows is safe under original's verdict, never the rewrite whose
// own re-evaluation came back worse. Otherwise the rewrite is applied
// (applied is true, finalCommand is the rewritten text).
//
// decision is ALWAYS original — never derived from reeval's own result.
// This is what makes "a rewrite never turns non-Approve into Approve" a
// STRUCTURAL property of this function rather than an extra check: if
// original is Abstain or Reject, the decision returned here is that same
// Abstain or Reject no matter what reeval(rewritten) reports, and no matter
// whether the rewrite ends up applied or discarded — a looser rewritten
// verdict only ever affects whether the TEXT is substituted, never the
// DECISION emitted.
//
// A nil reeval (no re-evaluator wired yet) is treated as "the rewrite is
// never re-judged, so it is always applied" — Process's pre-P10 behavior —
// rather than a panic, so callers that have not yet wired a Reevaluator (as
// of this packet, every caller: see this file's own doc comment) keep
// building.
func ProcessReevaluated(command string, payload Payload, original evalcontract.Decision, reeval Reevaluator) (finalCommand string, applied bool, decision evalcontract.Decision) {
	rewritten, changed := Process(command, payload)
	if !changed {
		return command, false, original
	}
	if reeval != nil && stricter(reeval(rewritten), original) {
		return command, false, original
	}
	return rewritten, true, original
}

// stricter reports whether a is a STRICTER (more restrictive) decision than
// b, per evalcontract.Decision's own vocabulary order: Reject is strictest,
// then Ask (reserved — the new engine's own policies never emit it, but
// ranked here so this comparison stays correct if that ever changes), then
// Abstain, then Approve (loosest). Mirrors internal/hookio.MostRestrictive's
// identical total order (Approve < NoOpinion < Ask < Reject) for the OLD
// engine's own Decision type — a separate implementation because the two
// Decision types are unrelated (R1: the new engine does not depend on the
// old engine's hookio types), not a divergent ordering.
func stricter(a, b evalcontract.Decision) bool {
	return decisionRank(a) > decisionRank(b)
}

// decisionRank maps a Decision onto its position in the total order stricter
// compares by. An unrecognized value (should never occur: evalcontract.Decision
// has no other constructors) ranks as Reject — the fail-closed choice,
// matching P1's "unsupported ... deadline expiry ... ⇒ ABSTAIN" spirit by
// never letting an unrecognized decision be treated as the loosest option.
func decisionRank(d evalcontract.Decision) int {
	switch d {
	case evalcontract.Approve:
		return 0
	case evalcontract.Abstain:
		return 1
	case evalcontract.Ask:
		return 2
	case evalcontract.Reject:
		return 3
	default:
		return 3
	}
}
