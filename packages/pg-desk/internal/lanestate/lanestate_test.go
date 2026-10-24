package lanestate

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

var (
	t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Hour)
	t2 = t0.Add(2 * time.Hour)
)

func intp(n int) *int { return &n }

func mustRecord(t *testing.T, st *store.Store, configured string, at time.Time, in RunInput) {
	t.Helper()
	if err := Record(st, configured, at, in); err != nil {
		t.Fatalf("Record: %v", err)
	}
}

func mustRead(t *testing.T, st *store.Store, at time.Time) Snapshot {
	t.Helper()
	snap, err := Read(st, at)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return snap
}

func TestWritingLaneStateLeavesTheSchemaVersionUnchanged(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	before, err := st.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	mustRecord(t, st, "apply", t0, RunInput{
		PR: "o/r#1", Mode: "apply", Residual: intp(1),
		Events:    []RunEvent{{Stage: "hydrate", Class: "transient"}},
		Decisions: []DecisionCount{{Rule: "all.closed", Action: "close", Outcome: "applied"}},
	})
	if _, err := MarkInactive(st, "o/r", "o/r#9"); err != nil {
		t.Fatal(err)
	}
	after, err := st.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if before != after || after != store.NewSchemaVersion {
		t.Errorf("schema version %d before, %d after, want unchanged at %d", before, after, store.NewSchemaVersion)
	}
}

func TestEveryLaneKeyLivesUnderTheLanePrefixAndNoneIsTheConfigKey(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	lookup := int64(5)
	mustRecord(t, st, "apply", t0, RunInput{
		PR: "o/r#1", Mode: "apply", Residual: intp(2), LookupItems: &lookup, LookupCeiling: &lookup,
		Events:    []RunEvent{{Stage: "decide", Class: "decide_failed"}},
		Decisions: []DecisionCount{{Rule: "land.ready", Action: "skip", Outcome: "skipped", Reason: "not matched"}},
	})
	rows, err := st.ListMetaPrefix("pr_lane")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 8 {
		t.Fatalf("expected the run to write several lane keys, got %v", rows)
	}
	for k := range rows {
		if k == "pr_lane.mode" {
			t.Errorf("a persisted lane key reuses the configuration key name pr_lane.mode")
		}
		if !strings.HasPrefix(k, KeyPrefix) {
			t.Errorf("key %q is outside the lane prefix %q", k, KeyPrefix)
		}
	}
	for _, k := range []string{KeyStateMode, KeyStateFlipAt, KeyStateLastRunAt, KeyStateLookupItem, KeyStateLookupCeil, KeyMarkedInactive} {
		if k == "pr_lane.mode" {
			t.Errorf("constant %q is the configuration key name", k)
		}
	}
}

func TestFirstApplyRunSetsLaneModeAndFlipTimeOnce(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	// Plan period: the lane mode follows the configured plan.
	mustRecord(t, st, "plan", t0, RunInput{PR: "o/r#1", Mode: "plan", Residual: intp(3)})
	if s := mustRead(t, st, t0); s.LaneMode != "plan" || s.FlipAt != "" {
		t.Fatalf("plan period: lane %q flip %q, want plan and no flip", s.LaneMode, s.FlipAt)
	}
	// The flip: configured apply, first recorded apply run finds the lane not yet apply.
	mustRecord(t, st, "apply", t1, RunInput{PR: "o/r#1", Mode: "apply", Residual: intp(0)})
	s := mustRead(t, st, t1)
	flip := t1.Format(time.RFC3339)
	if s.LaneMode != "apply" || s.FlipAt != flip {
		t.Fatalf("after the first apply run: lane %q flip %q, want apply and %s", s.LaneMode, s.FlipAt, flip)
	}
	// Another PR's first apply run does not reset the lane-wide flip time.
	mustRecord(t, st, "apply", t2, RunInput{PR: "o/r#2", Mode: "apply", Residual: intp(0)})
	if s := mustRead(t, st, t2); s.FlipAt != flip {
		t.Errorf("a later PR's first apply run moved the flip time to %q, want it kept at %q", s.FlipAt, flip)
	}
}

func TestByHandPlanRunAfterTheFlipLeavesLaneLevelAndPRStateAlone(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	mustRecord(t, st, "apply", t0, RunInput{PR: "o/r#1", Mode: "apply", Residual: intp(2), LastOutcome: "applied"})
	flip := mustRead(t, st, t0).FlipAt
	prKey := KeyPRPrefix + "o/r#1"
	beforePR, _, err := st.GetMeta(prKey)
	if err != nil {
		t.Fatal(err)
	}

	// A by-hand plan run after the flip: configured mode stays apply.
	mustRecord(t, st, "apply", t1, RunInput{
		PR: "o/r#1", Mode: "plan", Residual: intp(0), LastOutcome: "plan-clean",
		Events: []RunEvent{{Stage: "hydrate", Class: "degraded"}},
	})
	s := mustRead(t, st, t1)
	if s.LaneMode != "apply" || s.FlipAt != flip {
		t.Errorf("a by-hand plan run changed the lane level: lane %q flip %q, want apply and %q", s.LaneMode, s.FlipAt, flip)
	}
	afterPR, _, _ := st.GetMeta(prKey)
	if afterPR != beforePR {
		t.Errorf("a by-hand plan run overwrote the PR's apply-mode state:\n before %s\n after  %s", beforePR, afterPR)
	}
	if s.ResidualPRs != 1 {
		t.Errorf("residual gauge = %d PRs, want the apply-mode residual (1) kept visible", s.ResidualPRs)
	}
	// The run is still a recorded run for the counters and the last-run time.
	if got := s.Runs[RunKey{"hydrate", "degraded"}]; got != 1 {
		t.Errorf("run counter = %d, want the plan run's event counted", got)
	}
	if s.LastRunAt != t1.Format(time.RFC3339) {
		t.Errorf("last run %q, want %q: every recorded run writes it", s.LastRunAt, t1.Format(time.RFC3339))
	}

	// The next apply run does not reset the flip time.
	mustRecord(t, st, "apply", t2, RunInput{PR: "o/r#1", Mode: "apply", Residual: intp(2)})
	if s := mustRead(t, st, t2); s.FlipAt != flip {
		t.Errorf("the next apply run reset the flip time to %q, want %q", s.FlipAt, flip)
	}
}

func TestResidualPlanInsideAnApplyRunNeverSetsTheLaneMode(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	// The read-only residual plan inside an apply run is not a recorded run: the
	// composed command reports it as the apply run's Residual. A configuration
	// that is still plan must therefore never see apply written by it, and an
	// apply-mode input under a plan configuration is not a recorded run either.
	mustRecord(t, st, "plan", t0, RunInput{PR: "o/r#1", Mode: "apply", Residual: intp(4)})
	s := mustRead(t, st, t0)
	if s.LaneMode != "off" || s.FlipAt != "" {
		t.Errorf("lane %q flip %q: a run that is not the configured mode set the lane level", s.LaneMode, s.FlipAt)
	}
	if v, found, _ := st.GetMeta(KeyPRPrefix + "o/r#1"); found {
		t.Errorf("per-PR state written for a run that is not the configured mode: %s", v)
	}
}

func TestResidualClockSurvivesWhileNonEmptyAndRestartsOnAModeChange(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	mustRecord(t, st, "plan", t0, RunInput{PR: "o/r#1", Mode: "plan", Residual: intp(2)})
	mustRecord(t, st, "plan", t1, RunInput{PR: "o/r#1", Mode: "plan", Residual: intp(1)})
	s := mustRead(t, st, t2)
	if s.ResidualPRs != 1 || s.ResidualOldestAgeSeconds != int(t2.Sub(t0).Seconds()) {
		t.Errorf("residual %d PRs oldest %ds, want 1 and %ds (clock from the first non-empty run)", s.ResidualPRs, s.ResidualOldestAgeSeconds, int(t2.Sub(t0).Seconds()))
	}
	// The flip: the apply-mode residual starts its own clock.
	mustRecord(t, st, "apply", t1, RunInput{PR: "o/r#1", Mode: "apply", Residual: intp(1)})
	s = mustRead(t, st, t2)
	if s.ResidualOldestAgeSeconds != int(t2.Sub(t1).Seconds()) {
		t.Errorf("oldest %ds, want %ds: apply residual starts a new clock", s.ResidualOldestAgeSeconds, int(t2.Sub(t1).Seconds()))
	}
	// An empty residual clears it.
	mustRecord(t, st, "apply", t2, RunInput{PR: "o/r#1", Mode: "apply", Residual: intp(0)})
	if s := mustRead(t, st, t2); s.ResidualPRs != 0 || s.ResidualOldestAgeSeconds != 0 {
		t.Errorf("residual %d/%d after an empty plan, want 0/0", s.ResidualPRs, s.ResidualOldestAgeSeconds)
	}
	// A run that does not learn the residual keeps the recorded one.
	mustRecord(t, st, "apply", t2, RunInput{PR: "o/r#2", Mode: "apply", Residual: intp(3)})
	mustRecord(t, st, "apply", t2, RunInput{PR: "o/r#2", Mode: "apply", LastOutcome: "hydrate-failed"})
	if s := mustRead(t, st, t2); s.ResidualPRs != 1 {
		t.Errorf("residual PRs = %d, want 1: a run with an unknown residual must keep the old one", s.ResidualPRs)
	}
}

func TestLeavingApplyClearsTheFlipTimeSoALaterFlipIsNew(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	mustRecord(t, st, "apply", t0, RunInput{PR: "o/r#1", Mode: "apply"})
	mustRecord(t, st, "plan", t1, RunInput{PR: "o/r#1", Mode: "plan"}) // rollback
	if s := mustRead(t, st, t1); s.LaneMode != "plan" || s.FlipAt != "" {
		t.Fatalf("after the rollback: lane %q flip %q, want plan and no flip", s.LaneMode, s.FlipAt)
	}
	mustRecord(t, st, "apply", t2, RunInput{PR: "o/r#1", Mode: "apply"})
	if s := mustRead(t, st, t2); s.FlipAt != t2.Format(time.RFC3339) {
		t.Errorf("flip %q after a second flip, want %q", s.FlipAt, t2.Format(time.RFC3339))
	}
}

func TestRecordRejectsAnythingOutsideTheClosedVocabulariesAndWritesNothing(t *testing.T) {
	cases := map[string]RunInput{
		"no pr":          {Mode: "plan"},
		"bad mode":       {PR: "o/r#1", Mode: "sync"},
		"bad stage":      {PR: "o/r#1", Mode: "plan", Events: []RunEvent{{Stage: "reconcile", Class: "transient"}}},
		"retry class":    {PR: "o/r#1", Mode: "plan", Events: []RunEvent{{Stage: "hydrate", Class: "retry_exhausted"}}},
		"bad rule":       {PR: "o/r#1", Mode: "plan", Decisions: []DecisionCount{{Rule: "made.up", Action: "close", Outcome: "applied"}}},
		"bad action":     {PR: "o/r#1", Mode: "plan", Decisions: []DecisionCount{{Rule: "all.closed", Action: "explode", Outcome: "applied"}}},
		"bad outcome":    {PR: "o/r#1", Mode: "plan", Decisions: []DecisionCount{{Rule: "all.closed", Action: "close", Outcome: "meh"}}},
		"bad reason":     {PR: "o/r#1", Mode: "plan", Decisions: []DecisionCount{{Rule: "all.closed", Action: "skip", Outcome: "skipped", Reason: "because"}}},
		"reason on op":   {PR: "o/r#1", Mode: "plan", Decisions: []DecisionCount{{Rule: "all.closed", Action: "close", Outcome: "applied", Reason: "hidden"}}},
		"negative count": {PR: "o/r#1", Mode: "plan", Decisions: []DecisionCount{{Rule: "all.closed", Action: "close", Outcome: "applied", Count: -1}}},
		"negative resid": {PR: "o/r#1", Mode: "plan", Residual: intp(-1)},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			st := store.OpenNewSchemaForTest(t)
			// A valid event ahead of the bad one: validation runs before any write.
			in.Events = append([]RunEvent{{Stage: "hydrate", Class: "transient"}}, in.Events...)
			if err := Record(st, "plan", t0, in); err == nil {
				t.Fatal("Record accepted an invalid input")
			}
			rows, err := st.ListMetaPrefix(KeyPrefix)
			if err != nil || len(rows) != 0 {
				t.Errorf("rejected input wrote %v (err %v), want nothing", rows, err)
			}
		})
	}
}

func TestDecisionsAndRunsAccumulateAcrossRunsAndLookupFiguresAreKept(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	items, ceil := int64(40), int64(500)
	in := RunInput{
		PR: "o/r#1", Mode: "plan", LookupItems: &items, LookupCeiling: &ceil,
		Events:    []RunEvent{{Stage: "decide", Class: "decide_partial"}},
		Decisions: []DecisionCount{{Rule: "review.head-advanced", Action: "create", Outcome: "planned"}, {Rule: "review.head-advanced", Action: "skip", Outcome: "skipped", Reason: "review-pending", Count: 3}},
	}
	mustRecord(t, st, "plan", t0, in)
	mustRecord(t, st, "plan", t1, in)
	s := mustRead(t, st, t1)
	if got := s.Runs[RunKey{"decide", "decide_partial"}]; got != 2 {
		t.Errorf("run counter = %d, want 2", got)
	}
	if got := s.Decisions[Decision{"review.head-advanced", "create", "planned", ""}]; got != 2 {
		t.Errorf("decision counter = %d, want 2", got)
	}
	if got := s.Decisions[Decision{"review.head-advanced", "skip", "skipped", "review-pending"}]; got != 6 {
		t.Errorf("skip counter = %d, want 3 per run times 2", got)
	}
	if s.LookupItems != 40 || s.LookupCeiling != 500 {
		t.Errorf("lookup %d of %d, want 40 of 500", s.LookupItems, s.LookupCeiling)
	}
}

func TestMarkInactiveWorksFromAPlanRunAndDropsThePRFromTheNeverHydratedSet(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	if err := st.UpsertEntity(store.Entity{Repo: "o/r", EntityType: "pr", EntityID: "o/r#7", Facts: `{"pr_show":{"state":"open"}}`, AsOf: "2026-10-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	// Migrate the row to schema 2 style: a never-hydrated entity (version 0).
	got, err := st.CountPRUnhydrated(t0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 1 {
		t.Fatalf("setup: never-hydrated set = %d, want 1", got.Count)
	}
	// A plan-mode run: marking is a store write, no mode check.
	mustRecord(t, st, "plan", t0, RunInput{PR: "o/r#7", Mode: "plan"})
	changed, err := MarkInactive(st, "o/r", "o/r#7")
	if err != nil || !changed {
		t.Fatalf("MarkInactive: changed=%v err=%v", changed, err)
	}
	if got, _ := st.CountPRUnhydrated(t0); got.Count != 0 {
		t.Errorf("never-hydrated set = %d after marking inactive, want the PR dropped", got.Count)
	}
	if s := mustRead(t, st, t0); s.MarkedInactive != 1 {
		t.Errorf("marked-inactive counter = %d, want 1", s.MarkedInactive)
	}
}

func TestRulesAndReasonsCopyAgreesWithTheDocumentedSkipReasonCount(t *testing.T) {
	if len(Reasons) != 5 {
		t.Errorf("%d skip reasons, the decider's closed list has 5", len(Reasons))
	}
	if n := len(ZeroFilledDecisions()); n != len(Rules)*(len(Reasons)+(len(Actions)-1)*(len(Outcomes)-1)) {
		t.Errorf("zero-filled decisions = %d, unexpected size", n)
	}
	seen := map[Decision]bool{}
	for _, d := range ZeroFilledDecisions() {
		if seen[d] {
			t.Errorf("duplicate zero-filled series %+v", d)
		}
		seen[d] = true
	}
}
