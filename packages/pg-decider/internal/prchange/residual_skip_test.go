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

func TestNoActionApplyMakesTwoDeciderInvocationsAndRecordsResidualZero(t *testing.T) {
	s := hydrated().
		on("pg-decider plan pr acme/api#7 --json", ok(planEmpty)).
		on("pg-decider apply", exit(0, "", ""))
	code, rec := runApplyForResidual(t, s)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := deciderInvocations(s); got != 2 {
		t.Fatalf("%d decider invocations %v, want 2 (pre-plan, apply)", got, s.calls)
	}
	if rec.Residual == nil || *rec.Residual != 0 || rec.ResidualError != "" {
		t.Fatalf("residual = %v, error %q; want 0 and none", rec.Residual, rec.ResidualError)
	}
	if lanes := laneCalls(t, s); len(lanes) != 1 || lanes[0].Residual == nil || *lanes[0].Residual != 0 {
		t.Fatalf("lane input residual not 0: %+v", lanes)
	}
}

func TestActionApplyMakesThreeDeciderInvocations(t *testing.T) {
	s := hydrated().
		onSeq("pg-decider plan pr acme/api#7 --json", ok(planWithWork), ok(planEmpty)).
		on("pg-decider apply", exit(0, "", applyCounters+"\n"))
	code, rec := runApplyForResidual(t, s)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := deciderInvocations(s); got != 3 {
		t.Fatalf("%d decider invocations %v, want 3", got, s.calls)
	}
	if rec.Residual == nil || *rec.Residual != 0 {
		t.Fatalf("residual = %v, want 0 from the post-apply plan", rec.Residual)
	}
}

func TestFailedPrePlanStillReadsTheResidual(t *testing.T) {
	s := hydrated().
		onSeq("pg-decider plan pr acme/api#7 --json", exit(1, "", "boom"), ok(planWithWork)).
		on("pg-decider apply", exit(0, "", ""))
	code, rec := runApplyForResidual(t, s)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := planCalls(s); got != 2 {
		t.Fatalf("%d plan reads, want 2", got)
	}
	if rec.Decide == nil || rec.Decide.PlanError == "" {
		t.Fatalf("no PlanError recorded: %+v", rec.Decide)
	}
	if rec.Residual == nil || *rec.Residual != 3 {
		t.Fatalf("residual = %v, want 3 from the post-apply plan", rec.Residual)
	}
}

func TestApplyExit2StillReadsTheResidual(t *testing.T) {
	s := hydrated().
		onSeq("pg-decider plan pr acme/api#7 --json", ok(planEmpty), ok(planWithWork)).
		on("pg-decider apply", exit(2, "", "rule x failed\n"))
	_, rec := runApplyForResidual(t, s)
	if got := planCalls(s); got != 2 {
		t.Fatalf("%d plan reads, want 2", got)
	}
	if rec.Residual == nil || *rec.Residual != 3 {
		t.Fatalf("residual = %v, want 3", rec.Residual)
	}
}
