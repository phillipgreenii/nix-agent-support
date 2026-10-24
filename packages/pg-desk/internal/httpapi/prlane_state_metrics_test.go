package httpapi

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/lanestate"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func requireLines(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		found := false
		for _, line := range strings.Split(body, "\n") {
			if line == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("scrape lacks the line %q", w)
		}
	}
}

// TestMetricsExportThePersistedLaneFamiliesZeroFilledThenRiseAfterARecordedRun
// is the scrape test on a migrated schema-2 store: every lane-run series, the
// residual gauges, the decisions counter, the inactive counter and the two
// lookup gauges exist at 0 before any run, and move after one recorded run.
func TestMetricsExportThePersistedLaneFamiliesZeroFilledThenRiseAfterARecordedRun(t *testing.T) {
	setClock(t, time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC))
	s := store.OpenNewSchemaForTest(t)
	cfg := &config.Config{SelfLogin: "me", PRLane: config.PRLaneConfig{Mode: "apply"}}
	mustUpsertInterpretation(t, s, store.Interpretation{Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Panel: PanelMineAwaitingMe, AsOf: "2026-10-10T00:00:00Z"})

	body := scrapeWithConfig(t, s, cfg)
	requireLines(
		t, body,
		`pg_desk_pr_lane_runs_total{class="not_found",stage="hydrate"} 0`,
		`pg_desk_pr_lane_runs_total{class="decide_failed",stage="decide"} 0`,
		`pg_desk_pr_lane_runs_total{class="transient",stage="hydrate"} 0`,
		`pg_desk_pr_lane_residual_prs{mode="off"} 0`,
		`pg_desk_pr_lane_residual_prs{mode="plan"} 0`,
		`pg_desk_pr_lane_residual_prs{mode="apply"} 0`,
		`pg_desk_pr_lane_residual_oldest_age_seconds{mode="apply"} 0`,
		`pg_decider_decisions_total{action="skip",outcome="skipped",reason="not matched",rule="all.closed"} 0`,
		`pg_decider_decisions_total{action="close",outcome="applied",reason="",rule="all.closed"} 0`,
		`pg_desk_pr_marked_inactive_total 0`,
		`pg_desk_pr_lane_lookup_items 0`,
		`pg_desk_pr_lane_lookup_ceiling 0`,
	)

	resid, items, ceil := 2, int64(40), int64(500)
	if err := lanestate.Record(s, "apply", time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), lanestate.RunInput{
		PR: "o/r#1", Mode: "apply", Residual: &resid, LookupItems: &items, LookupCeiling: &ceil,
		Events:    []lanestate.RunEvent{{Stage: "hydrate", Class: "transient"}},
		Decisions: []lanestate.DecisionCount{{Rule: "all.closed", Action: "close", Outcome: "applied"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertEntity(store.Entity{Repo: "o/r", EntityType: "pr", EntityID: "o/r#9", Facts: `{}`, AsOf: "2026-10-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := lanestate.MarkInactive(s, "o/r", "o/r#9"); err != nil {
		t.Fatal(err)
	}

	body = scrapeWithConfig(t, s, cfg)
	requireLines(
		t, body,
		`pg_desk_pr_lane_runs_total{class="transient",stage="hydrate"} 1`,
		`pg_desk_pr_lane_residual_prs{mode="apply"} 1`,
		`pg_desk_pr_lane_residual_prs{mode="plan"} 0`,
		`pg_desk_pr_lane_residual_oldest_age_seconds{mode="apply"} 86400`,
		`pg_decider_decisions_total{action="close",outcome="applied",reason="",rule="all.closed"} 1`,
		`pg_desk_pr_marked_inactive_total 1`,
		`pg_desk_pr_lane_lookup_items 40`,
		`pg_desk_pr_lane_lookup_ceiling 500`,
	)
}

// TestMetricsOmitThePersistedLaneFamiliesOnAnOldSchemaStore: the lane state
// needs a schema-2 store; an older one has no series for it (never a 0 that
// would read as healthy) and the scrape still succeeds.
func TestMetricsOmitThePersistedLaneFamiliesOnAnOldSchemaStore(t *testing.T) {
	s := store.OpenForTest(t)
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "1",
		Panel: PanelMineAwaitingMe, AsOf: "2026-09-16T12:00:00Z",
	})
	body := scrapeWithConfig(t, s, &config.Config{SelfLogin: "me"})
	for _, absent := range []string{"pg_desk_pr_lane_runs", "pg_desk_pr_lane_residual", "pg_decider_decisions", "pg_desk_pr_marked_inactive", "pg_desk_pr_lane_lookup"} {
		if strings.Contains(body, absent) {
			t.Errorf("%s has a series on an old-schema store", absent)
		}
	}
	if !strings.Contains(body, "pg_desk_liveness 1") {
		t.Errorf("the rest of the scrape is missing:\n%s", body)
	}
}
