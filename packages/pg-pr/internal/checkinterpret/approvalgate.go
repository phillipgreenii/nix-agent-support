package checkinterpret

import (
	"regexp"
	"strconv"
)

// State is the approval-gate interpreter's classification axis: how much
// of an approval gate's rule set is currently satisfied.
type State string

const (
	// Satisfied means every rule the gate tracks is currently approved.
	Satisfied State = "satisfied"
	// PartiallySatisfied means some but not all rules are approved
	// (0 < N < M in the parsed fraction). N and M are retained on Result.
	PartiallySatisfied State = "partially-satisfied"
	// Unsatisfied means none of the gate's rules are currently approved
	// (N == 0, M > 0 in the parsed fraction). M is retained on Result.
	Unsatisfied State = "unsatisfied"
	// Unknown means description could not be parsed into any of the
	// above — an empty/absent description, a malformed or missing
	// fraction, or a zero-denominator/inverted fraction that would
	// otherwise misrepresent the gate. NEVER coerced to Satisfied: a
	// parse failure reading as "gate satisfied" would be worse than no
	// axis at all (pg2-4dz88.2's own stated risk for this generalization).
	Unknown State = "unknown"
)

// Result is the outcome of classifying one approval-gate description. N
// and M are populated only when State was derived from a parsed N/M
// fraction that left the gate short of fully satisfied (PartiallySatisfied
// and Unsatisfied always carry them). Satisfied and Unknown leave both
// zero — including when Satisfied was reached via the fraction path
// (N == M): once the state is definite, the fraction that produced it is
// not retained.
type Result struct {
	State State
	N, M  int
}

// allApprovedRe recognizes policy-bot's "all rules are approved" sentence
// (the example measured in pg2-4dz88.2), tolerant of surrounding
// whitespace, internal whitespace runs, and case. This is a fixed part of
// the approval-gate interpreter's own parsing grammar, not a
// registry-configured pattern: the "every pattern is config-injected in a
// test" constraint governs Interpreter.Patterns (which claim a
// check/status NAME), not this interpreter's internal description
// grammar, which is exactly as fixed as the "/" in the fraction grammar
// below — both come from the external approval-gate service's own output
// format, not from local configuration.
var allApprovedRe = regexp.MustCompile(`(?i)^\s*all\s+rules\s+are\s+approved\s*$`)

// fractionRe finds an N/M fraction anywhere in a description — it is NOT
// anchored to the start/end of the string, so it still resolves inside a
// long or multi-sentence description carrying the fraction mid-string (an
// explicitly named test case).
var fractionRe = regexp.MustCompile(`(\d+)/(\d+)`)

// ClassifyApprovalGate parses description — a GitHub commit-status
// (StatusContext) description string following policy-bot's approval-gate
// convention, e.g. "All rules are approved" or "1/2 rules approved" — into
// a Result.
//
// Algorithm (design decision 5 of pg2-4dz88.2.4's brief, derived from the
// bead's own acceptance-criteria test table):
//
//  1. Look for an N/M fraction anywhere in description. If found:
//     - M == 0 => Unknown (a zero denominator must never read as "all
//     approved").
//     - N > M => Unknown (never PartiallySatisfied — an inverted fraction
//     is not a valid gate state).
//     - N == M (M > 0) => Satisfied. This is how "2/2" resolves — via the
//     fraction path, not the sentence path below.
//     - N == 0 (M > 0) => Unsatisfied, with M retained.
//     - 0 < N < M => PartiallySatisfied, with N and M retained.
//  2. Otherwise, if description case- and whitespace-insensitively matches
//     the all-rules-approved sentence => Satisfied.
//  3. Otherwise => Unknown. This covers non-numeric fraction-looking text
//     (e.g. "x/y"), a missing slash, and an empty or absent description
//     (Go has no distinct "absent string" — a zero-value string IS an
//     empty description, so both collapse to the same case here).
//
// conclusion/description disagreement (design decision 6): conclusion is
// accepted for signature symmetry with the caller's other CIRun-derived
// fields, but is DELIBERATELY NOT CONSULTED anywhere in this algorithm —
// description is always authoritative. Rationale: the fraction
// policy-bot renders into description is the more precise signal (it
// carries "how many rules", which a bare success/failure conclusion
// cannot express at all — this is the epic's own stated motivation for
// adding this interpreter). When the two disagree there is no reason to
// prefer the coarser signal over the richer one. Concretely: conclusion
// "success" paired with a "0/1"-shaped description still resolves
// Unsatisfied, and conclusion "failure" paired with an all-rules-approved
// description still resolves Satisfied — both pinned by
// TestClassifyApprovalGate's disagreement cases.
func ClassifyApprovalGate(conclusion, description string) Result {
	if m := fractionRe.FindStringSubmatch(description); m != nil {
		n, errN := strconv.Atoi(m[1])
		mm, errM := strconv.Atoi(m[2])
		if errN != nil || errM != nil {
			return Result{State: Unknown}
		}
		switch {
		case mm == 0:
			return Result{State: Unknown}
		case n > mm:
			return Result{State: Unknown}
		case n == mm:
			return Result{State: Satisfied}
		case n == 0:
			return Result{State: Unsatisfied, M: mm}
		default:
			return Result{State: PartiallySatisfied, N: n, M: mm}
		}
	}
	if allApprovedRe.MatchString(description) {
		return Result{State: Satisfied}
	}
	return Result{State: Unknown}
}
