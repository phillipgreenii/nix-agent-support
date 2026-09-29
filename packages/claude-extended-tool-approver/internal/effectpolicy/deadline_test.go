package effectpolicy

import (
	"testing"
	"time"
)

// TestEvalDeadlineWorkedExamples pins P11's own two worked examples
// (docket tc-o14i5.3 design v16, binding decision P11) exactly:
//
//	one processor at today's 3s => (5 - 3.25 - 0.5) / 2 = 0.625s
//	two                         => (5 - 6.5  - 0.5) / 2 = -1.0s (infeasible)
func TestEvalDeadlineWorkedExamples(t *testing.T) {
	tests := []struct {
		name      string
		procCount int
		want      time.Duration
	}{
		{"one processor", 1, 625 * time.Millisecond},
		{"two processors", 2, -1 * time.Second},
		{"zero processors, k=1", 0, DefaultEvalWindow - fixedOverhead},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EvalDeadline(DefaultEvalWindow, tc.procCount, DefaultProcBudget, DefaultProcGrace)
			if got != tc.want {
				t.Errorf("EvalDeadline(%d) = %s, want %s", tc.procCount, got, tc.want)
			}
		})
	}
}

// TestEvalDeadlineKSwitchesOnProcessorPresence: k is 2 the moment even one
// processor is configured, never scaled by processor count itself (only the
// Sum(proc_budget+grace) term scales with count) -- P11's own text: "k = 2
// with processors (P10), else 1".
func TestEvalDeadlineKSwitchesOnProcessorPresence(t *testing.T) {
	zero := EvalDeadline(DefaultEvalWindow, 0, DefaultProcBudget, DefaultProcGrace)
	one := EvalDeadline(DefaultEvalWindow, 1, DefaultProcBudget, DefaultProcGrace)
	// k flips from 1 to 2 between these two calls: verify by reconstructing
	// what a same-k comparison would have given and confirming the actual
	// result is NOT that (i.e. k really changed, not just the numerator).
	sameKWouldBe := DefaultEvalWindow - (DefaultProcBudget + DefaultProcGrace) - fixedOverhead
	if one == sameKWouldBe {
		t.Fatalf("EvalDeadline(1 processor) = %s equals the same-k-as-zero-processors value %s; k did not switch to 2", one, sameKWouldBe)
	}
	if zero <= 0 {
		t.Fatalf("EvalDeadline(0 processors) = %s, want positive (no processor penalty, k=1)", zero)
	}
}

// TestCheckDeadlineOK: today's live configuration (1 processor, the shipped
// defaultTimeout/waitGrace mirrored as DefaultProcBudget/DefaultProcGrace,
// T=5s) against the CI-recorded BenchmarkEvaluateP99 must not warn -- this
// is the "manually verify the deadline check" validation bullet's OTHER
// half (the synthetic warn case is TestCheckDeadlineWarnsOnSyntheticScenario
// below): today's real numbers should currently be healthy.
func TestCheckDeadlineOK(t *testing.T) {
	deadline := EvalDeadline(DefaultEvalWindow, 1, DefaultProcBudget, DefaultProcGrace)
	sev, reason := CheckDeadline(deadline, BenchmarkEvaluateP99)
	if sev != DeadlineOK {
		t.Fatalf("CheckDeadline(live config) = %s (%s), want %s -- either P11's deadline math or BenchmarkEvaluateP99 needs attention", sev, reason, DeadlineOK)
	}
}

// TestCheckDeadlineWarnsOnSyntheticScenario is this packet's own Validation
// bullet 2, executed as a test: "construct a synthetic scenario where
// eval_deadline < 2 x p99 and confirm the check fires as a warning first (not
// yet hard-failing CI on first landing, per warning first, then hard)."
//
// Uses P11's own second worked example (two processors => -1.0s, an
// infeasible deadline that is trivially below any positive 2x p99
// threshold) as the synthetic scenario, and confirms the returned severity
// is DeadlineWarn (never a build-breaking severity -- this package defines
// none) with a non-empty, human-readable reason.
func TestCheckDeadlineWarnsOnSyntheticScenario(t *testing.T) {
	infeasible := EvalDeadline(DefaultEvalWindow, 2, DefaultProcBudget, DefaultProcGrace)
	if infeasible >= 0 {
		t.Fatalf("synthetic scenario setup: EvalDeadline(2 processors) = %s, want negative (infeasible) per P11's own worked example", infeasible)
	}
	sev, reason := CheckDeadline(infeasible, BenchmarkEvaluateP99)
	if sev != DeadlineWarn {
		t.Fatalf("CheckDeadline(infeasible deadline) = %s, want %s", sev, DeadlineWarn)
	}
	if reason == "" {
		t.Fatal("CheckDeadline returned DeadlineWarn with an empty reason")
	}
	// This is the check "firing as a warning first": it is surfaced via
	// t.Log, never t.Error/t.Fatal, so this test (and therefore `go test
	// ./...`) never fails CI on a Warn finding -- exactly "not yet
	// hard-failing CI on first landing".
	t.Logf("deadline check fired (warning-level, non-failing): %s", reason)
}

// TestCheckDeadlineThresholdIsExactlyTwoP99 pins the boundary: a deadline
// exactly equal to 2xp99 is OK (the formula is "< 2 x p99", not "<="), and
// one nanosecond below it warns.
func TestCheckDeadlineThresholdIsExactlyTwoP99(t *testing.T) {
	threshold := 2 * BenchmarkEvaluateP99
	if sev, _ := CheckDeadline(threshold, BenchmarkEvaluateP99); sev != DeadlineOK {
		t.Errorf("CheckDeadline(exactly 2x p99) = %s, want %s", sev, DeadlineOK)
	}
	if sev, _ := CheckDeadline(threshold-1, BenchmarkEvaluateP99); sev != DeadlineWarn {
		t.Errorf("CheckDeadline(2x p99 - 1ns) = %s, want %s", sev, DeadlineWarn)
	}
}

// TestDeadlineLintLiveConfiguration is the "linter check" P11 calls for,
// run as an ordinary go test (this repo's own custom linter framework
// pattern -- see internal/speclint -- is a Finding/Severity check invoked
// from a test or CLI, not a third-party linter binary; this check has no
// CLI surface yet since nothing outside this package's own tests consumes
// it, matching the "not yet hard-failing CI" scope of this packet). It
// evaluates TODAY's shipped configuration and only LOGS a warning, never
// fails the build, per P11's "warning first, then hard".
func TestDeadlineLintLiveConfiguration(t *testing.T) {
	deadline := EvalDeadline(DefaultEvalWindow, 1, DefaultProcBudget, DefaultProcGrace)
	if sev, reason := CheckDeadline(deadline, BenchmarkEvaluateP99); sev == DeadlineWarn {
		t.Logf("[deadline-lint warn] %s", reason)
	}
}
