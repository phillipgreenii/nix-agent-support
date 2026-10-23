package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func scrapeWithConfig(t *testing.T, s *store.Store, cfg *config.Config) string {
	t.Helper()
	handler, err := NewHandler(s, cfg)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	return rr.Body.String()
}

// TestMetricsExposeThePRLaneFamiliesOnASchema2Store: on a migrated store the
// configured lane mode and the never-hydrated set are exported with values,
// the set computed at scrape time from the stored rows.
func TestMetricsExposeThePRLaneFamiliesOnASchema2Store(t *testing.T) {
	setClock(t, time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC))
	s := store.OpenForTest(t)
	for id, asOf := range map[string]string{"o/r#1": "2026-10-10T00:00:00Z", "o/r#2": "2026-10-09T00:00:00Z", "o/r#3": "2026-10-01T00:00:00Z"} {
		if err := s.UpsertEntity(store.Entity{Repo: "o/r", EntityType: "pr", EntityID: id, Facts: `{}`, AsOf: asOf}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Cutover(); err != nil {
		t.Fatal(err)
	}
	// #3 was hydrated after the cutover: it leaves the set.
	e, _, err := s.GetEntity("o/r", "pr", "o/r#3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteEntityStateWithLog(e, e.Version, "2026-10-10T12:00:00Z", true, []string{"created"}, "poll", "2026-10-10T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	mustUpsertInterpretation(t, s, store.Interpretation{Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Panel: PanelMineAwaitingMe, AsOf: "2026-10-10T00:00:00Z"})

	cfg := testConfig()
	cfg.PRLane.Mode = "plan"
	body := scrapeWithConfig(t, s, cfg)
	// #1 (first seen 1 day before the clock) and #2 (2 days) are the set.
	for _, want := range []string{
		`pg_desk_pr_unhydrated 2`,
		`pg_desk_pr_unhydrated_oldest_age_seconds 172800`,
		`pg_desk_pr_lane_mode{mode="plan"} 1`,
		`pg_desk_pr_lane_mode{mode="off"} 0`,
		`pg_desk_pr_lane_mode{mode="apply"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q:\n%s", want, body)
		}
	}
}

// TestMetricsPRLaneEmitsZeroWhenHealthyAndNothingWhenUnmigrated: an empty
// never-hydrated set on a migrated store is exported as 0 (healthy), a store
// that is not migrated has no series for it (never 0), and a missing or
// invalid mode reads as off on both.
func TestMetricsPRLaneEmitsZeroWhenHealthyAndNothingWhenUnmigrated(t *testing.T) {
	setClock(t, time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC))

	n := store.OpenNewSchemaForTest(t)
	mustUpsertInterpretation(t, n, store.Interpretation{Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Panel: PanelMineAwaitingMe, AsOf: "2026-10-10T00:00:00Z"})
	body := scrapeWithConfig(t, n, testConfig())
	for _, want := range []string{`pg_desk_pr_unhydrated 0`, `pg_desk_pr_unhydrated_oldest_age_seconds 0`, `pg_desk_pr_lane_mode{mode="off"} 1`} {
		if !strings.Contains(body, want) {
			t.Errorf("healthy empty scrape missing %q:\n%s", want, body)
		}
	}

	old := store.OpenForTest(t)
	mustUpsertInterpretation(t, old, store.Interpretation{Repo: "o/r", EntityType: "pull_request", EntityID: "1", Panel: PanelMineAwaitingMe, AsOf: "2026-10-10T00:00:00Z"})
	cfg := testConfig()
	cfg.PRLane.Mode = "bogus"
	body = scrapeWithConfig(t, old, cfg)
	if strings.Contains(body, "pg_desk_pr_unhydrated") {
		t.Errorf("an unmigrated store exports a never-hydrated series:\n%s", body)
	}
	if !strings.Contains(body, `pg_desk_pr_lane_mode{mode="off"} 1`) {
		t.Errorf("an invalid mode must read as off:\n%s", body)
	}
}
