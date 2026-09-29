package effectpolicy

import (
	"fmt"
	"time"
)

// This file is P11's own implementation (docket tc-o14i5.3, design v16,
// Phase 2 item "l", packet tc-o14i5.3.14): the eval-deadline formula, plus a
// runtime + linter check (warning first, then hard) that fires when the
// computed deadline leaves less than 2x headroom over BenchmarkEvaluate's
// CI-recorded p99 (benchmark_p99.go).
//
// P11 (binding decision, verbatim): "Deadline: eval_deadline = (T -
// Sum(proc_budget + 0.25 s) - 0.5 s) / k; T = 5 s (hooks.json) or ceta's
// router slice; k = 2 with processors (P10), else 1. Worked example: one
// processor at today's 3 s -> (5 - 3.25 - 0.5) / 2 = 0.625 s; two -> (5 -
// 6.5 - 0.5) / 2 = -1.0 s (infeasible). A runtime + linter check (warning
// first, then hard; an HM assertion MAY mirror it) fails when eval_deadline
// < 2 x the CI-recorded BenchmarkEvaluate p99. Expiry -> Abstain."
//
// # proc_budget provenance
//
// The worked example's "one processor at today's 3s" is
// internal/inputproc's own shipped, PER-PROCESSOR defaultTimeout (3 *
// time.Second) plus its waitGrace (250 * time.Millisecond) -- 3.25s exactly
// matches the worked example's first term. This packet's own "Expected
// additional reads" note asked whether P10/K11's input-processor work
// measures processor timing anywhere; it does not (inputproc has no
// per-invocation timing log, only the fixed budget/grace CONSTANTS it
// enforces) -- that absence is the finding this packet records. Since
// internal/inputproc.defaultTimeout/waitGrace are unexported (and this
// packet's own Files section does not touch inputproc.go), DefaultProcBudget
// / DefaultProcGrace below carry the SAME values as a documented duplicate,
// not an import, with this comment as the cross-reference a future reader
// needs to keep the two in sync if inputproc's shipped budget ever changes.
const (
	// DefaultProcBudget mirrors internal/inputproc's unexported
	// defaultTimeout (3s) -- the per-processor rewrite budget.
	DefaultProcBudget = 3 * time.Second
	// DefaultProcGrace mirrors internal/inputproc's unexported waitGrace
	// (250ms) -- the P11 formula's "+ 0.25 s" term.
	DefaultProcGrace = 250 * time.Millisecond
	// DefaultEvalWindow is P11's T: the PreToolUse hook's own 5s timeout
	// (Claude Code's documented default hook timeout; this repo's hooks.json
	// wiring has not been re-measured against a non-default value -- see
	// this packet's "Expected additional reads" note on "ceta's router
	// slice", which does not exist yet either). A future caller with a real
	// hooks.json/router value overrides T explicitly via EvalDeadline's own
	// parameter rather than this constant.
	DefaultEvalWindow = 5 * time.Second
	// fixedOverhead is P11's flat "- 0.5 s" term (parse/graph-build/fold
	// overhead outside the per-processor budget).
	fixedOverhead = 500 * time.Millisecond
)

// EvalDeadline computes P11's eval_deadline for a chain of procCount
// input processors, each budgeted procBudget+procGrace, inside a total
// evaluation window t. k is 2 when procCount > 0 (P10 processors are
// configured), else 1 -- exactly P11's own rule, not a caller choice.
//
// A negative result is a valid (infeasible) output, per P11's own worked
// example ("two -> ... = -1.0 s (infeasible)"); EvalDeadline does not clamp
// it -- a caller wiring this into a live budget decides what to do with an
// infeasible deadline (this packet does not wire live enforcement; see the
// package doc's "Phase 5 cutover" note on internal/inputproc.ProcessReevaluated
// for why: the new engine is not cut over to cmd/claude-extended-tool-approver's
// hook handlers yet, so there is no live caller for this function beyond its
// own tests and CheckDeadline below).
func EvalDeadline(t time.Duration, procCount int, procBudget, procGrace time.Duration) time.Duration {
	k := time.Duration(1)
	if procCount > 0 {
		k = 2
	}
	sum := time.Duration(procCount) * (procBudget + procGrace)
	return (t - sum - fixedOverhead) / k
}

// DeadlineSeverity mirrors internal/speclint's own Severity vocabulary
// (SeverityWarn/SeverityHard -- see that package's doc comment) rather than
// importing it: speclint's Severity is scoped to spec-linting findings, and
// this is a different check family with no other reason to depend on that
// package. Declared as its own type (not a bare bool) for the same reason
// speclint's is: a future promotion to SeverityHard should be a value
// change here, not a signature change at every call site.
type DeadlineSeverity string

const (
	// DeadlineOK: eval_deadline has at least 2x headroom over
	// BenchmarkEvaluateP99.
	DeadlineOK DeadlineSeverity = "ok"
	// DeadlineWarn: eval_deadline is below the 2x headroom threshold.
	// P11's own binding decision is "warning first, then hard" -- this
	// packet only ever produces DeadlineWarn, never a hard-failing
	// severity; promoting a DeadlineWarn finding to a build-breaking one is
	// explicitly OUT OF SCOPE for this packet (see its own "Out of scope"
	// section) and is future work once the CI-recorded p99 line has proven
	// stable.
	DeadlineWarn DeadlineSeverity = "warn"
)

// CheckDeadline is the runtime + linter check P11 requires: it reports
// DeadlineWarn (with a human-readable reason) when deadline is below 2x p99,
// else DeadlineOK. It never panics and never itself decides what a caller
// does with a Warn finding (log it, surface it in a lint report, ...) --
// exactly the same "check computes, caller disposes" shape
// internal/speclint's own Lint functions use (see that package's Finding /
// Severity pattern this type deliberately mirrors).
//
// Deadline EXPIRY (a caller's own evaluation running past whatever deadline
// this function reports) is explicitly NOT this function's concern -- P11's
// own text delegates that to Fold (see evaluate.go's FoldFailure doc
// comment, Kind "deadline": "a caller's own deadline already exceeded ...
// Fold only guarantees what happens once a caller reports one"). This
// packet only computes and checks the deadline BUDGET; it does not
// reimplement Fold's already-existing deadline-expiry-to-Abstain behavior.
func CheckDeadline(deadline, p99 time.Duration) (DeadlineSeverity, string) {
	threshold := 2 * p99
	if deadline < threshold {
		return DeadlineWarn, fmt.Sprintf(
			"eval_deadline %s has less than 2x headroom over BenchmarkEvaluate's CI-recorded p99 %s (need >= %s)",
			deadline, p99, threshold,
		)
	}
	return DeadlineOK, ""
}
