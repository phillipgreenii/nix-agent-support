package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func scrape(t *testing.T, s *store.Store) string {
	t.Helper()
	handler, err := NewHandler(s, testConfig())
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

// TestMetricsExposeEveryChangeFlowFamilyForANewSchemaStore seeds a
// new-schema store with a known change flow and asserts each family appears
// with the right value: records by kind and origin, hydrations, hydration
// failures, due backlog, consumer lag and optimistic-concurrency retries.
func TestMetricsExposeEveryChangeFlowFamilyForANewSchemaStore(t *testing.T) {
	s := store.OpenNewSchemaForTest(t)
	setClock(t, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	write := func(id string, expected int64, hydratedAt string, kinds []string, origin string) {
		t.Helper()
		e := store.Entity{Repo: "o/r", EntityType: "pr", EntityID: id, Facts: `{}`, AsOf: "x"}
		if _, err := s.WriteEntityStateWithLog(e, expected, hydratedAt, true, kinds, origin, "2026-10-01T10:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	write("o/r#1", 0, "2026-10-01T11:00:00Z", []string{"created"}, "poll")
	write("o/r#2", 0, "2026-09-30T00:00:00Z", []string{"created", "labels"}, "poll") // due: older than 6h
	write("o/r#1", 1, "2026-10-01T11:00:00Z", []string{"reconcile"}, "sweep")
	if err := s.RegisterConsumer("alpha", "pr", nowUTC()); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceCursor("alpha", "pr", 1); err != nil {
		t.Fatal(err)
	}
	changes.RecordHydration(s, "pr", "o/r#1", pipeline.EntityChangeResult{Retries: 2}, nil)
	changes.RecordHydration(s, "pr", "o/r#2", pipeline.EntityChangeResult{}, errors.New("boom"))
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Panel: PanelMineAwaitingMe, AsOf: "2026-10-01T11:00:00Z",
	})

	body := scrape(t, s)
	for _, want := range []string{
		`pg_desk_change_log_records{kind="created",origin="poll",type="pr"} 2`,
		`pg_desk_change_log_records{kind="labels",origin="poll",type="pr"} 1`,
		`pg_desk_change_log_records{kind="reconcile",origin="sweep",type="pr"} 1`,
		`pg_desk_hydrations_total{type="pr"} 2`,
		`pg_desk_hydration_failures_total{type="pr"} 1`,
		`pg_desk_occ_retries_total{type="pr"} 2`,
		`pg_desk_due_backlog{type="pr"} 1`,
		`pg_desk_consumer_lag{consumer="alpha",type="pr"} 2`,
		`pg_desk_repeated_degraded_entities{type="pr"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q; full body:\n%s", want, body)
		}
	}
}

// TestMetricsDegradeOnAnOldSchemaStore pins that serve keeps answering on a
// store that has not been cut over: the dashboard families stay, the
// change-flow families are simply absent, and the scrape does not fail.
func TestMetricsDegradeOnAnOldSchemaStore(t *testing.T) {
	s := store.OpenForTest(t)
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "1",
		Panel: PanelMineAwaitingMe, AsOf: "2026-09-16T12:00:00Z",
	})
	setClock(t, time.Date(2026, 9, 16, 12, 0, 30, 0, time.UTC))

	body := scrape(t, s)
	if !strings.Contains(body, "pg_desk_liveness 1") {
		t.Errorf("dashboard families missing on an old-schema store:\n%s", body)
	}
	for _, absent := range []string{
		"pg_desk_change_log_records", "pg_desk_hydrations", "pg_desk_hydration_failures", "pg_desk_occ_retries",
		"pg_desk_due_backlog", "pg_desk_consumer_lag",
	} {
		if strings.Contains(body, absent+"{") || strings.Contains(body, absent+"_total{") {
			t.Errorf("%s has a series on an old-schema store:\n%s", absent, body)
		}
	}
}

// TestMetricsExposeOldestAnchorCheckAge covers bead pg2-u4c1s end to end:
// on an old-schema store the gauge is the stalest open applied anchor's
// ledger age (closed and planned rows excluded), and on a cut-over store
// (no ledger table) the scrape still succeeds and reports 0.
func TestMetricsExposeOldestAnchorCheckAge(t *testing.T) {
	setClock(t, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))

	s := store.OpenForTest(t)
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "1",
		Panel: PanelMineAwaitingMe, AsOf: "2026-10-05T11:59:00Z",
	})
	for _, l := range []store.LedgerEntry{
		{Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Kind: "anchor", BeadID: "bd-1", LastSyncedContentHash: "h", LastSyncedAt: "2026-10-05T10:00:00Z"},
		{Repo: "o/r", EntityType: "pr", EntityID: "o/r#2", Kind: "anchor", BeadID: "bd-2", LastSyncedContentHash: "closed", LastSyncedAt: "2026-10-01T00:00:00Z"},
		{Repo: "o/r", EntityType: "pr", EntityID: "o/r#3", Kind: "anchor", BeadID: "", LastSyncedContentHash: "h", LastSyncedAt: "2026-10-01T00:00:00Z"},
	} {
		if err := s.UpsertLedger(l); err != nil {
			t.Fatal(err)
		}
	}
	if body := scrape(t, s); !strings.Contains(body, "pg_desk_oldest_anchor_check_age_seconds 7200") {
		t.Errorf("old-schema scrape missing the 7200s anchor-check gauge:\n%s", body)
	}

	n := store.OpenNewSchemaForTest(t)
	mustUpsertInterpretation(t, n, store.Interpretation{
		Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Panel: PanelMineAwaitingMe, AsOf: "2026-10-05T11:59:00Z",
	})
	if body := scrape(t, n); !strings.Contains(body, "pg_desk_oldest_anchor_check_age_seconds 0") {
		t.Errorf("new-schema scrape missing the zero anchor-check gauge:\n%s", body)
	}
}
