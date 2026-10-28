package alertrules

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/metrics"
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
