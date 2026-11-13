package prchange

import (
	"context"
	"strings"
	"testing"
)

// planCalls counts the read-only `pg-decider plan pr <id> --json` reads a run made.
func planCalls(s *script) int {
	n := 0
	for _, c := range s.calls {
		if strings.HasPrefix(c, "pg-decider plan pr acme/api#7 --json") {
			n++
		}
	}
	return n
}

func deciderInvocations(s *script) int {
	n := 0
	for _, c := range s.calls {
		if strings.HasPrefix(c, "pg-decider ") {
			n++
		}
	}
	return n
}

func runApplyForResidual(t *testing.T, s *script) (int, RunRecord) {
	t.Helper()
	o, _, errb := opts(t, ModeApply, s)
	o.RunRecordPath = t.TempDir() + "/" + RunRecordFileName
	code := Run(context.Background(), o)
	recs := readRecords(t, o.RunRecordPath)
	if len(recs) != 1 {
		t.Fatalf("%d run records, want 1: %s", len(recs), errb)
	}
	return code, recs[0]
}

func TestNoActionApplyMakesOneDeciderInvocationAndRecordsResidualZero(t *testing.T) {
	s := hydrated().
		on("pg-decider apply", exit(0, planEmpty, ""))
	code, rec := runApplyForResidual(t, s)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := deciderInvocations(s); got != 1 {
		t.Fatalf("%d decider invocations %v, want 1 (the apply)", got, s.calls)
	}
	if rec.Residual == nil || *rec.Residual != 0 || rec.ResidualError != "" {
		t.Fatalf("residual = %v, error %q; want 0 and none", rec.Residual, rec.ResidualError)
	}
	if lanes := laneCalls(t, s); len(lanes) != 1 || lanes[0].Residual == nil || *lanes[0].Residual != 0 {
		t.Fatalf("lane input residual not 0: %+v", lanes)
	}
}

func TestActionApplyMakesTwoDeciderInvocationsAndNoPrePlanCall(t *testing.T) {
	s := hydrated().
		on("pg-decider plan pr acme/api#7 --json", ok(planEmpty)).
		on("pg-decider apply", exit(0, planWithWork, applyCounters+"\n"))
	code, rec := runApplyForResidual(t, s)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := deciderInvocations(s); got != 2 {
		t.Fatalf("%d decider invocations %v, want 2 (apply, residual plan)", got, s.calls)
	}
	var order []string
	for _, c := range s.calls {
		if strings.HasPrefix(c, "pg-decider ") {
			order = append(order, strings.Fields(c)[1])
		}
	}
	if strings.Join(order, ",") != "apply,plan" {
		t.Fatalf("decider call order %v, want apply then the residual plan", order)
	}
	if rec.Residual == nil || *rec.Residual != 0 {
		t.Fatalf("residual = %v, want 0 from the post-apply plan", rec.Residual)
	}
	// The plan the record carries is the apply's own.
	if rec.Decide == nil || len(rec.Decide.Actions) != 3 || rec.Decide.Actions[0].Op != "create" || rec.Decide.PlanError != "" || rec.Decide.PrePlanMs != nil {
		t.Fatalf("decide = %+v", rec.Decide)
	}
}

// An apply whose result is not readable JSON sets the PlanError the pre-apply
// call used to set, and the residual is still read.
func TestUnreadableApplyResultSetsPlanErrorAndStillReadsTheResidual(t *testing.T) {
	s := hydrated().
		on("pg-decider plan pr acme/api#7 --json", ok(planWithWork)).
		on("pg-decider apply", exit(0, "not json", ""))
	code, rec := runApplyForResidual(t, s)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := planCalls(s); got != 1 {
		t.Fatalf("%d plan reads, want 1 (the residual)", got)
	}
	if rec.Decide == nil || !strings.HasPrefix(rec.Decide.PlanError, "the plan is not readable JSON") {
		t.Fatalf("PlanError = %+v", rec.Decide)
	}
	if rec.Decide.ExitCode != 0 || len(rec.Decide.Actions) != 0 {
		t.Fatalf("decide = %+v", rec.Decide)
	}
	if rec.Residual == nil || *rec.Residual != 3 {
		t.Fatalf("residual = %v, want 3 from the post-apply plan", rec.Residual)
	}
}

// Partial failure: the plan the failed apply printed is still the record's plan,
// and the residual is read.
func TestApplyExit2UsesItsOwnPlanAndStillReadsTheResidual(t *testing.T) {
	s := hydrated().
		on("pg-decider plan pr acme/api#7 --json", ok(planWithWork)).
		on("pg-decider apply", exit(5, planEmpty, "rule x failed\n"))
	_, rec := runApplyForResidual(t, s)
	if got := planCalls(s); got != 1 {
		t.Fatalf("%d plan reads, want 1", got)
	}
	if rec.Decide == nil || rec.Decide.PlanError != "" {
		t.Fatalf("decide = %+v", rec.Decide)
	}
	if rec.Residual == nil || *rec.Residual != 3 {
		t.Fatalf("residual = %v, want 3", rec.Residual)
	}
}

// An apply that fails outright (no plan printed) records a PlanError and reads
// no residual.
func TestApplyThatFailsOutrightRecordsAPlanError(t *testing.T) {
	s := hydrated().
		on("pg-decider apply", exit(20, "", "lookup untrusted\n"))
	_, rec := runApplyForResidual(t, s)
	if rec.Decide == nil || !strings.Contains(rec.Decide.PlanError, "exited 20") {
		t.Fatalf("decide = %+v", rec.Decide)
	}
	if planCalls(s) != 0 {
		t.Fatalf("a plan was read: %v", s.calls)
	}
}
