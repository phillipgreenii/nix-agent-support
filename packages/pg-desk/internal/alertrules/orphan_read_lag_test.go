package alertrules

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/metrics"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const orphanReadLagUID = "pg-desk-issue-orphan-read-lag"

// wantOrphanReadLagExpr MUST equal the expr of rule pg-desk-issue-orphan-read-lag
// (the approach of watched_source_observed_test.go).
const wantOrphanReadLagExpr = `max by (backend) (pg_desk_oldest_orphan_read_age_seconds)` + outcomeGuard

// orphanReadLagBoundSeconds is the rule's fixed threshold: 6 polls of 5 minutes.
const orphanReadLagBoundSeconds = 1800

func TestOrphanReadLagRuleExprMatchesModel(t *testing.T) {
	pinOutcomeRule(t, orphanReadLagUID, wantOrphanReadLagExpr, "pg_desk_oldest_orphan_read_age_seconds", "1800")
}

func TestOrphanReadLagFiresAboveThirtyMinutesAndClears(t *testing.T) {
	nd := outcomeNoDataState(t, orphanReadLagUID)
	old := []outcomeSample{{"pg-desk", "a", 2400}, {"pg-desk-legacy", "a", 2400}, {"pg-desk", "b", 600}}
	got, _ := outcomeFiring(evalOutcomeExpr(old, true), orphanReadLagBoundSeconds, nd)
	if len(got) != 1 || got[0] != "a" {
		t.Errorf("firing = %v, want exactly [a] (the duplicate job series is ONE backend)", got)
	}
	if got, _ := outcomeFiring(evalOutcomeExpr([]outcomeSample{{"pg-desk", "a", 1800}}, true), orphanReadLagBoundSeconds, nd); len(got) != 0 {
		t.Errorf("age == 30 minutes fired %v", got)
	}
	if got, _ := outcomeFiring(evalOutcomeExpr([]outcomeSample{{"pg-desk", "a", 1801}}, true), orphanReadLagBoundSeconds, nd); len(got) != 1 {
		t.Errorf("age == 30 minutes + 1s fired %v, want one backend", got)
	}
	got, noData := outcomeFiring(evalOutcomeExpr([]outcomeSample{{"pg-desk", "a", 30}}, true), orphanReadLagBoundSeconds, nd)
	if len(got) != 0 || noData {
		t.Errorf("a freshly read orphan fired %v (noData alert %v)", got, noData)
	}
}

func TestOrphanReadLagNoDataAlertsOnlyWhilePgDeskIsUp(t *testing.T) {
	nd := outcomeNoDataState(t, orphanReadLagUID)
	if nd != "Alerting" {
		t.Fatalf("noDataState = %q, want Alerting", nd)
	}
	if _, noData := outcomeFiring(evalOutcomeExpr(nil, true), orphanReadLagBoundSeconds, nd); !noData {
		t.Error("liveness up and no series did not alert")
	}
	got, noData := outcomeFiring(evalOutcomeExpr(nil, false), orphanReadLagBoundSeconds, nd)
	if len(got) != 0 || noData {
		t.Errorf("pg-desk down: fired %v (noData alert %v), want quiet", got, noData)
	}
}

// TestOrphanReadLagRuleAgreesWithTheEmittedSeries feeds the model with what the
// gauge really emits: an orphan whose last SUCCESSFUL read is old fires (its
// failed attempts do not reset the age), a read clears it, and a deployment with
// no orphan emits one 0 and stays quiet.
func TestOrphanReadLagRuleAgreesWithTheEmittedSeries(t *testing.T) {
	nd := outcomeNoDataState(t, orphanReadLagUID)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}}
	cfg.Watch.Issue.Queries = []string{"open"}
	st := store.OpenNewSchemaForTest(t)
	yes := true
	seed := func(id string, okAgo time.Duration) {
		t.Helper()
		fp := "F1"
		if _, err := st.WriteEntityStateWithLogFP(store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: `{}`, AsOf: "x"},
			0, now.Add(-time.Hour).Format(time.RFC3339), true, &fp, []string{"reconcile"}, "sweep", "2026-09-29T10:00:00Z"); err != nil {
			t.Fatal(err)
		}
		if err := st.SetEntityColumns("o/r", "issue", id, nil, &yes); err != nil {
			t.Fatal(err)
		}
		ok, attempt := now.Add(-okAgo).Format(time.RFC3339Nano), now.Add(-time.Minute).Format(time.RFC3339Nano)
		if err := st.SetEntityReadTimes("o/r", "issue", id, &attempt, &ok); err != nil {
			t.Fatal(err)
		}
	}
	opt := func() metrics.ChangeFlowOption {
		return metrics.WithOldestOrphanReadAge(metrics.OldestOrphanReadAgeFromStore(st, cfg, func() time.Time { return now }))
	}
	empty := emittedOutcome(t, metrics.MetricOldestOrphanReadAge, opt())
	if len(empty) != 1 || empty[0].v != 0 {
		t.Fatalf("no orphan must read one 0 series, got %+v", empty)
	}
	if got, noData := outcomeFiring(evalOutcomeExpr(empty, true), orphanReadLagBoundSeconds, nd); len(got) != 0 || noData {
		t.Errorf("no orphan fired %v (noData alert %v)", got, noData)
	}

	seed("bd-a", 10*time.Minute)
	seed("bd-b", 45*time.Minute)
	series := emittedOutcome(t, metrics.MetricOldestOrphanReadAge, opt())
	if len(series) != 1 || series[0].v != 45*60 {
		t.Fatalf("series = %+v, want one sample at %d", series, 45*60)
	}
	if got, _ := outcomeFiring(evalOutcomeExpr(series, true), orphanReadLagBoundSeconds, nd); len(got) != 1 {
		t.Errorf("a 45 minute old orphan: firing = %v, want one backend", got)
	}
	seed2 := func() {
		t.Helper()
		ok := now.Add(-5 * time.Minute).Format(time.RFC3339Nano)
		if err := st.SetEntityReadTimes("o/r", "issue", "bd-b", nil, &ok); err != nil {
			t.Fatal(err)
		}
	}
	seed2()
	series = emittedOutcome(t, metrics.MetricOldestOrphanReadAge, opt())
	if got, _ := outcomeFiring(evalOutcomeExpr(series, true), orphanReadLagBoundSeconds, nd); len(got) != 0 {
		t.Errorf("after a successful read fired %v", got)
	}
}

func TestOrphanReadLagUIDIsRegisteredWithTheRouterProbe(t *testing.T) {
	assertProbeRegistered(t, orphanReadLagUID)
}
