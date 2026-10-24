package prchange

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/backfill"
)

// The backfill driver (internal/backfill) reads the run record this package
// writes and reads this package's exit codes, and cannot import it (this
// package's test rig imports the driver). These tests pin what the two share, so
// neither side can change alone.

func TestTheBackfillDriverReadsTheRunRecordThisPackageWrites(t *testing.T) {
	if backfill.RunRecordContract != RunRecordContract || backfill.RunRecordFileName != RunRecordFileName {
		t.Fatalf("the driver reads %q in %q, the command writes %q in %q", backfill.RunRecordContract, backfill.RunRecordFileName, RunRecordContract, RunRecordFileName)
	}
	residual := 2
	want := RunRecord{
		Contract: RunRecordContract, RunID: "0123456789abcdef", At: "2026-10-24T10:00:00Z", FinishedAt: "2026-10-24T10:00:03Z",
		Entity: "acme/api#7", EventID: "acme/api#7", Mode: ModeApply, ExitCode: ExitDecidePartial, ErrorClass: ClassDecidePartial, FailingStage: StageDecide,
		Hydrate: HydrateRecord{Result: "ok"},
		Decide: &DecideRecord{
			ExitCode: 2,
			Rules:    []RuleOutcome{{Rule: "anchor.lazy", Outcome: "applied", Counts: map[string]int{"planned": 2, "applied": 1}}, {Rule: "adoption", Outcome: "skipped", Reason: "not matched"}},
			Actions:  []PlannedAction{{Rule: "anchor.lazy", Kind: "anchor", Op: "create"}, {Rule: "adoption", Kind: "anchor", Op: "update"}},
			ByKind:   map[string]int{"anchor": 2},
		},
		Adoption:      &AdoptionRecord{Outcome: "unadoptable", Candidates: []AnchorCandidate{{ID: "bd-1", State: "open", Keyed: false, MatchedBy: "title"}}},
		Residual:      &residual,
		ResidualError: "",
	}
	path := filepath.Join(t.TempDir(), RunRecordFileName)
	if err := AppendRunRecord(path, want); err != nil {
		t.Fatal(err)
	}
	lg, err := backfill.ReadLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(lg.Records) != 1 || lg.Ignored != 0 {
		t.Fatalf("the driver read %d records, ignored %d", len(lg.Records), lg.Ignored)
	}
	got := lg.Records[0]
	if got.Entity != want.Entity || got.Mode != want.Mode || got.ExitCode != want.ExitCode || got.ErrorClass != want.ErrorClass ||
		got.FailingStage != want.FailingStage || got.RunID != want.RunID || got.EventID != want.EventID || got.Hydrate.Result != "ok" ||
		got.At != want.At || got.Time.IsZero() {
		t.Errorf("header: %+v", got)
	}
	if got.Residual == nil || *got.Residual != residual {
		t.Errorf("residual %v", got.Residual)
	}
	if got.Decide == nil || got.Decide.ExitCode != 2 || got.Decide.ByKind["anchor"] != 2 || len(got.Decide.Actions) != 2 ||
		got.Decide.Actions[0] != (backfill.Planned{Rule: "anchor.lazy", Kind: "anchor", Op: "create"}) ||
		got.Decide.Rules[0].Counts["applied"] != 1 || got.Decide.Rules[1].Reason != "not matched" {
		t.Errorf("decide: %+v", got.Decide)
	}
	if got.Adoption == nil || got.Adoption.Outcome != "unadoptable" || !reflect.DeepEqual(got.Adoption.Candidates, []backfill.AnchorCandidate{{ID: "bd-1", State: "open", MatchedBy: "title"}}) {
		t.Errorf("adoption: %+v", got.Adoption)
	}
}

func TestTheBackfillDriverReadsTheComposedCommandsExitCodesTheWayTheyAreMeant(t *testing.T) {
	// Every failure row of the published table is in the driver's table with the
	// same class, and the driver's table has no other row but the usage code.
	seen := map[int]bool{}
	for _, s := range Table {
		c, ok := backfill.ExitClasses[s.Code]
		if !ok {
			t.Errorf("exit %d (%s/%s) is not in the driver's table", s.Code, s.Stage, s.Class)
			continue
		}
		seen[s.Code] = true
		if c.Class != s.Class {
			t.Errorf("exit %d: the driver reads class %q, the command says %q", s.Code, c.Class, s.Class)
		}
		// Only these end a run at once; only a not-found is a final result.
		wantStop := s.Class == ClassUnauthenticated || s.Class == ClassDecideFailed
		if c.Stop != wantStop {
			t.Errorf("exit %d (%s): stop = %v, want %v", s.Code, s.Class, c.Stop, wantStop)
		}
		if c.Final != (s.Class == ClassNotFound) {
			t.Errorf("exit %d (%s): final = %v", s.Code, s.Class, c.Final)
		}
	}
	for code, c := range backfill.ExitClasses {
		if !seen[code] && code != ExitUsage {
			t.Errorf("the driver's table has exit %d (%s) the command does not have", code, c.Class)
		}
	}
	if c := backfill.ExitClasses[ExitUsage]; !c.Stop {
		t.Error("a usage error must stop the run")
	}
	// The driver's own exit codes never collide with 1 or 9.
	for _, c := range []int{backfill.ExitDone, backfill.ExitFloor, backfill.ExitFailed, backfill.ExitStopped, backfill.ExitUsage, backfill.ExitSummaryFailed} {
		if c == 1 || c == 9 {
			t.Errorf("exit code %d", c)
		}
	}
}

func TestTheBackfillDriversDefaultRunRecordIsWhereTheCommandWritesIt(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	t.Setenv("PG_PR_CHANGE_RUN_RECORD", "")
	c := &backfill.Config{}
	c.WithDefaults()
	if want := filepath.Join("/state", "pg-pr-change", RunRecordFileName); c.RunRecord != want {
		t.Fatalf("the driver reads %s, the command writes %s", c.RunRecord, want)
	}
	t.Setenv("PG_PR_CHANGE_RUN_RECORD", "/elsewhere/runs.jsonl")
	c = &backfill.Config{}
	c.WithDefaults()
	if c.RunRecord != "/elsewhere/runs.jsonl" {
		t.Fatalf("the environment override is not honoured: %s", c.RunRecord)
	}
}
