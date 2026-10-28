package alertrules

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/metrics"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// wantUnflaggedExpr MUST equal the expr of rule pg-desk-issue-unflagged-members.
const wantUnflaggedExpr = `max by (backend) (pg_desk_issue_unflagged_members)` + outcomeGuard

const unflaggedUID = "pg-desk-issue-unflagged-members"

func TestIssueUnflaggedMembersRuleExprMatchesModel(t *testing.T) {
	pinOutcomeRule(t, unflaggedUID, wantUnflaggedExpr, "pg_desk_issue_unflagged_members", "0")
}

func TestIssueUnflaggedMembersFiresOnAnyNonZeroCountAndClears(t *testing.T) {
	nd := outcomeNoDataState(t, unflaggedUID)
	got, _ := outcomeFiring(evalOutcomeExpr([]outcomeSample{{"pg-desk", "jira-a", 3}, {"pg-desk-legacy", "jira-a", 3}, {"pg-desk", "jira-b", 0}}, true), 0, nd)
	if len(got) != 1 || got[0] != "jira-a" {
		t.Errorf("firing = %v, want exactly [jira-a]", got)
	}
	if got, _ := outcomeFiring(evalOutcomeExpr([]outcomeSample{{"pg-desk", "jira-a", 1}}, true), 0, nd); len(got) != 1 {
		t.Errorf("a count of one fired %v, want one instance", got)
	}
	got, noData := outcomeFiring(evalOutcomeExpr([]outcomeSample{{"pg-desk", "jira-a", 0}}, true), 0, nd)
	if len(got) != 0 || noData {
		t.Errorf("a zero count fired %v (noData alert %v)", got, noData)
	}
}

func TestIssueUnflaggedMembersNoDataAlertsOnlyWhilePgDeskIsUp(t *testing.T) {
	nd := outcomeNoDataState(t, unflaggedUID)
	if nd != "Alerting" {
		t.Fatalf("noDataState = %q, want Alerting", nd)
	}
	if _, noData := outcomeFiring(evalOutcomeExpr(nil, true), 0, nd); !noData {
		t.Error("liveness up and no series did not alert")
	}
	got, noData := outcomeFiring(evalOutcomeExpr(nil, false), 0, nd)
	if len(got) != 0 || noData {
		t.Errorf("pg-desk down: fired %v (noData alert %v), want quiet", got, noData)
	}
}

// TestIssueUnflaggedMembersRuleAgreesWithTheEmittedSeries feeds the model with
// what the gauge really emits for the count a poll stored: a stored count
// fires, a later poll storing 0 clears it, and no stored count reads 0.
func TestIssueUnflaggedMembersRuleAgreesWithTheEmittedSeries(t *testing.T) {
	nd := outcomeNoDataState(t, unflaggedUID)
	st := store.OpenNewSchemaForTest(t)
	queries := map[string][]string{"jira-a": {"mine"}}
	emit := func() []outcomeSample {
		return emittedOutcome(t, metrics.MetricIssueUnflaggedMembers, metrics.WithUnflaggedMembers(metrics.UnflaggedMembersFromStore(st, queries)))
	}
	if series := emit(); len(series) != 1 || series[0].v != 0 {
		t.Fatalf("no stored count must read 0, got %+v", series)
	}
	store1 := func(n int) {
		t.Helper()
		b, _ := json.Marshal(changes.UnflaggedRecord{Count: n, At: time.Now().UTC().Format(time.RFC3339)})
		if err := st.SetMeta(changes.MetaUnflaggedPrefix+"jira-a", string(b)); err != nil {
			t.Fatal(err)
		}
	}
	store1(2)
	if got, _ := outcomeFiring(evalOutcomeExpr(emit(), true), 0, nd); len(got) != 1 || got[0] != "jira-a" {
		t.Errorf("a stored count of 2: firing = %v, want [jira-a]", got)
	}
	store1(0)
	got, noData := outcomeFiring(evalOutcomeExpr(emit(), true), 0, nd)
	if len(got) != 0 || noData {
		t.Errorf("a stored count of 0 fired %v (noData alert %v)", got, noData)
	}
}

func TestIssueUnflaggedMembersUIDIsRegisteredWithTheRouterProbe(t *testing.T) {
	assertProbeRegistered(t, unflaggedUID)
}

// quietHydrator answers every hydration with nothing written: the poll notes it
// and carries on, which is all these tests need from the pipeline.
type quietHydrator struct{}

func (quietHydrator) RunEntityChange(context.Context, string, string, gather.ChangeKind, pipeline.EntityChangeOptions) (pipeline.EntityChangeResult, error) {
	return pipeline.EntityChangeResult{}, nil
}

type listedLister struct{ res gather.ListFingerprintsResult }

func (l listedLister) ListFingerprints(context.Context, string, string) (gather.ListFingerprintsResult, error) {
	return l.res, nil
}

// TestBeadsUnflaggedSeriesFromARealPollFiresClearsAndHoldsABaseline drives the
// real poll over a beads instance's watched query: three held members have no
// watched flag. While the query's baseline has been open under one day they are
// excluded (the series reads 0 and the rule is quiet); once it has been open
// over a day the series reads 3 and the rule fires; flagging them clears it.
func TestBeadsUnflaggedSeriesFromARealPollFiresClearsAndHoldsABaseline(t *testing.T) {
	nd := outcomeNoDataState(t, unflaggedUID)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	st := store.OpenNewSchemaForTest(t)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{Repos: []config.RepoConfig{{Remote: "o/r"}}}
	cfg.Watch.Issue.Queries = []string{"open"}
	cfg.Watch.Issue.Backends = map[string]config.WatchBackend{"open": {Instance: "beads-a"}}
	res := gather.ListFingerprintsResult{
		Sources:      []gather.ListChangesSource{{Backend: "b", Status: "succeeded"}},
		Fingerprints: map[string]string{},
	}
	for _, id := range []string{"bd-1", "bd-2", "bd-3"} {
		fp := "F1"
		if _, err := st.WriteEntityStateWithLogFP(store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: `{}`, AsOf: "x"},
			0, now.Add(-time.Hour).Format(time.RFC3339), true, &fp, []string{"reconcile"}, "sweep", now.Add(-time.Hour).Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
		res.Entities = append(res.Entities, gather.ListedEntity{ID: id, Title: id})
		res.Fingerprints[id] = "F2" // differs from the stored list_fp, so the baseline must hydrate, and the quiet hydrator never marks it
	}
	eng := &changes.Engine{Cfg: cfg, Store: st, Hydrator: quietHydrator{}, Lister: listedLister{res: res}, Now: func() time.Time { return now }}
	poll := func() {
		t.Helper()
		_, err := eng.Run(context.Background(), changes.Options{EntityType: "issue", Consumer: "router"}, func(changes.Envelope) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	emit := func() []outcomeSample {
		return emittedOutcome(t, metrics.MetricIssueUnflaggedMembers, metrics.WithUnflaggedMembers(metrics.UnflaggedMembersForConfig(st, cfg)))
	}
	setOpen := func(ago time.Duration) {
		t.Helper()
		if err := st.SetMeta(changes.MetaBaselineOpenPrefix+"open", now.Add(-ago).Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	// The baseline has been open one hour and cannot finish (nothing is written
	// by the quiet hydrator): its members are excluded.
	setOpen(time.Hour)
	poll()
	series := emit()
	if len(series) != 1 || series[0].backend != "beads-a" || series[0].v != 0 {
		t.Fatalf("series = %+v, want one beads-a sample at 0 while the baseline is under a day old", series)
	}
	if got, noData := outcomeFiring(evalOutcomeExpr(series, true), 0, nd); len(got) != 0 || noData {
		t.Errorf("a baseline in progress fired %v (noData alert %v)", got, noData)
	}
	// Open for over a day: the three unflagged members count and the rule fires.
	setOpen(25 * time.Hour)
	poll()
	series = emit()
	if len(series) != 1 || series[0].v != 3 {
		t.Fatalf("series = %+v, want one beads-a sample at 3", series)
	}
	if got, _ := outcomeFiring(evalOutcomeExpr(series, true), 0, nd); len(got) != 1 || got[0] != "beads-a" {
		t.Errorf("firing = %v, want [beads-a]", got)
	}
	// The members get their flag: the next poll reads 0 and the alert clears.
	yes := true
	for _, id := range []string{"bd-1", "bd-2", "bd-3"} {
		if err := st.SetEntityColumns("o/r", "issue", id, nil, &yes); err != nil {
			t.Fatal(err)
		}
	}
	poll()
	series = emit()
	if got, noData := outcomeFiring(evalOutcomeExpr(series, true), 0, nd); len(got) != 0 || noData {
		t.Errorf("after flagging fired %v (noData alert %v), series %+v", got, noData, series)
	}
	// pg-desk down: the rule stays quiet whatever the series.
	if got, noData := outcomeFiring(evalOutcomeExpr(nil, false), 0, nd); len(got) != 0 || noData {
		t.Errorf("pg-desk down: fired %v (noData alert %v)", got, noData)
	}
}
