package asklog

// Outcome values stored in tool_decisions.outcome.
//
// The column is plain TEXT with no CHECK constraint, so introducing a value
// needs no ALTER TABLE — but every value MUST be listed here and classified by
// OutcomeIsDecision, because that classification is what keeps a row that
// nobody ever decided out of the miss set.
//
// Each value names exactly ONE provenance, so the column alone answers "did
// anybody decide this, and who":
//
//	pending    — logged at PreToolUse, not yet resolved. Live sessions only.
//	approved   — PostToolUse fired: the tool actually ran.
//	denied     — a decline JUDGEMENT was rendered against the call, by the user
//	             or by the auto-mode classifier (the PermissionDenied event).
//	             Written only by RecordPermissionDenied, which always also sets
//	             outcome_notes.
//	rejected   — CETA itself returned Reject at PreToolUse. Nobody was asked;
//	             the hook refused. Written by RecordPreToolDecision at INSERT
//	             time, so resolved_at == created_at and hook_decision == 'deny'.
//	unresolved — the call was never resolved at all (interrupted, abandoned,
//	             session died, agent moved on) and got swept at SessionEnd by
//	             ResolveUnresolvedAll. This is NOT a denial: nobody declined
//	             anything.
//
// Before this split all three of denied/rejected/unresolved were written as
// 'denied'. A bulk SessionEnd sweep therefore looked identical to a user
// saying no, which manufactured phantom "the user denied it but the hook
// allows it" false-allows — the artifact migration 7 backfills away.
const (
	OutcomePending    = "pending"
	OutcomeApproved   = "approved"
	OutcomeDenied     = "denied"
	OutcomeRejected   = "rejected"
	OutcomeUnresolved = "unresolved"
)

// OutcomeIsDecision reports whether an outcome records that SOMETHING actually
// decided the call — i.e. whether the row carries ground truth that hook
// correctness can legitimately be graded against.
//
// It is false for exactly the two non-decisions: OutcomePending (not resolved
// YET) and OutcomeUnresolved (never resolved). Grading a replayed hook decision
// against either INVENTS ground truth, so callers MUST NOT count such a row as
// a miss. An unknown/future value is also treated as "no decision", which fails
// closed: it can never be scored as a miss on a value this binary cannot
// interpret.
func OutcomeIsDecision(outcome string) bool {
	switch outcome {
	case OutcomeApproved, OutcomeDenied, OutcomeRejected:
		return true
	default:
		return false
	}
}
