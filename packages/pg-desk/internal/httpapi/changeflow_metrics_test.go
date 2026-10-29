package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
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
		`pg_desk_due_backlog{type="pr"} 0`, // one PR is older than the max age; a PR has no due backlog
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

// TestMetricsSweepBoundViolatedPerTier scrapes the real handler: the poll
// interval comes from WithPollInterval, the bound from the SAME evaluation as
// doctor. 7 active PRs / cap 1 x 30s = 210s: over the local tier's 3m age
// (violated) and under the remote tier's 6h (holds); issue has no known poll
// interval, so it has no series at all.
func TestMetricsSweepBoundViolatedPerTier(t *testing.T) {
	s := store.OpenNewSchemaForTest(t)
	setClock(t, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	for _, id := range []string{"o/r#1", "o/r#2", "o/r#3", "o/r#4", "o/r#5", "o/r#6", "o/r#7"} {
		e := store.Entity{Repo: "o/r", EntityType: "pr", EntityID: id, Facts: `{}`, AsOf: "x"}
		if _, err := s.WriteEntityStateWithLog(e, 0, "2026-10-01T11:59:00Z", true, []string{"created"}, "poll", "2026-10-01T11:59:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Panel: PanelMineAwaitingMe, AsOf: "2026-10-01T11:59:00Z",
	})
	one := 1
	cfg := testConfig()
	cfg.Sweep.MaxPerPoll = &one
	cfg.Sweep.ReconcileAge = "3m"

	scrapeWith := func(opts ...Option) string {
		handler, err := NewHandler(s, cfg, opts...)
		if err != nil {
			t.Fatal(err)
		}
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
		}
		return rr.Body.String()
	}

	body := scrapeWith(WithPollInterval(func(typ string) (time.Duration, bool) {
		if typ == "pr" {
			return 30 * time.Second, true
		}
		return 0, false
	}))
	for _, want := range []string{
		`pg_desk_sweep_bound_violated{tier="local",type="pr"} 1`,
		`pg_desk_sweep_bound_violated{tier="remote",type="pr"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `pg_desk_sweep_bound_violated{tier="local",type="issue"}`) ||
		strings.Contains(body, `pg_desk_sweep_bound_violated{tier="remote",type="issue"}`) {
		t.Errorf("issue has no known poll interval, so no series is expected:\n%s", body)
	}

	// Without a supplier no family member is emitted: never a false violation.
	if body := scrapeWith(); strings.Contains(body, "pg_desk_sweep_bound_violated{") {
		t.Errorf("a series was emitted without a poll interval:\n%s", body)
	}
}

// TestMetricsExposeTheFailedIngestSeriesFromTheTable pins the real scrape: the
// failed-ingest gauges are derived from the ingest_failed table at scrape time,
// an old-schema store has no series, and an empty table of a new-schema store
// still reads 0 (an empty backend label, which Prometheus drops, when none is configured).
func TestMetricsExposeTheFailedIngestSeriesFromTheTable(t *testing.T) {
	setClock(t, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	s := store.OpenNewSchemaForTest(t)
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Panel: PanelMineAwaitingMe, AsOf: "2026-10-01T11:59:00Z",
	})
	body := scrape(t, s)
	for _, want := range []string{`pg_desk_ingest_failed{backend=""} 0`, `pg_desk_ingest_failed_oldest_age_seconds{backend=""} 0`} {
		if !strings.Contains(body, want) {
			t.Errorf("an empty table: body missing %q; full body:\n%s", want, body)
		}
	}
	if err := s.RecordIngestFailure(store.IngestFailure{
		Instance: "jira-a", EntityID: "PROJ-1", Seq: 3, StoreIdentity: "s1",
		FirstFailedAt: "2026-10-01T11:00:00Z", LastError: "boom",
	}); err != nil {
		t.Fatal(err)
	}
	body = scrape(t, s)
	for _, want := range []string{
		`pg_desk_ingest_failed{backend="jira-a"} 1`,
		`pg_desk_ingest_failed_oldest_age_seconds{backend="jira-a"} 3600`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q; full body:\n%s", want, body)
		}
	}

	old := store.OpenForTest(t)
	mustUpsertInterpretation(t, old, store.Interpretation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "1",
		Panel: PanelMineAwaitingMe, AsOf: "2026-10-01T11:59:00Z",
	})
	if body := scrape(t, old); strings.Contains(body, "pg_desk_ingest_failed") {
		t.Errorf("an old-schema store has failed-ingest series:\n%s", body)
	}
}

// TestMetricsExposeTheJiraOutcomeSeriesFromTheStore pins the real scrape: the
// watched-source age and the stored unflagged count of an owning instance are
// derived from the meta keys at scrape time, and a deployment with no owning
// instance still emits one backendless 0 of each.
func TestMetricsExposeTheJiraOutcomeSeriesFromTheStore(t *testing.T) {
	setClock(t, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	s := store.OpenNewSchemaForTest(t)
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Panel: PanelMineAwaitingMe, AsOf: "2026-10-01T11:59:00Z",
	})
	body := scrape(t, s)
	for _, want := range []string{
		`pg_desk_watched_source_observed_age_seconds{backend=""} 0`,
		`pg_desk_issue_unflagged_members{backend=""} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no owning instance: body missing %q; full body:\n%s", want, body)
		}
	}

	cfg := testConfig()
	cfg.Watch.Issue.Queries = []string{"mine"}
	cfg.Watch.Issue.Backends = map[string]config.WatchBackend{"mine": {Instance: "jira-a", OwnsChanges: true}}
	if err := s.SetMeta(changes.MetaFeedObservedPrefix+"jira-a.mine", "2026-10-01T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMeta(changes.MetaUnflaggedPrefix+"jira-a", `{"count":2,"at":"2026-10-01T11:55:00Z"}`); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{
		`pg_desk_watched_source_observed_age_seconds{backend="jira-a"} 7200`,
		`pg_desk_issue_unflagged_members{backend="jira-a"} 2`,
	} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Errorf("body missing %q; full body:\n%s", want, rr.Body.String())
		}
	}

	old := store.OpenForTest(t)
	mustUpsertInterpretation(t, old, store.Interpretation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "1",
		Panel: PanelMineAwaitingMe, AsOf: "2026-10-01T11:59:00Z",
	})
	if body := scrape(t, old); strings.Contains(body, "pg_desk_issue_unflagged_members") || strings.Contains(body, "pg_desk_watched_source_observed_age_seconds") {
		t.Errorf("an old-schema store has outcome series:\n%s", body)
	}
}

// TestMetricsExposeTheWatchedEntityAndOrphanSeriesFromTheStore pins the real
// scrape: the watched count by type and backend and the oldest-orphan age (from
// the last successful read, hydrated_at before any) are derived from the entity
// table at scrape time, and a deployment with no orphan emits one 0 series.
func TestMetricsExposeTheWatchedEntityAndOrphanSeriesFromTheStore(t *testing.T) {
	setClock(t, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	s := store.OpenNewSchemaForTest(t)
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Panel: PanelMineAwaitingMe, AsOf: "2026-10-01T11:59:00Z",
	})
	body := scrape(t, s)
	for _, want := range []string{
		`pg_desk_watched_entities{backend="",type="issue"} 0`,
		`pg_desk_oldest_orphan_read_age_seconds{backend=""} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("nothing watched: body missing %q; full body:\n%s", want, body)
		}
	}

	fp := "F1"
	yes := true
	for id, hydrated := range map[string]string{"bd-1": "2026-10-01T11:30:00Z", "bd-2": "2026-10-01T11:50:00Z"} {
		if _, err := s.WriteEntityStateWithLogFP(store.Entity{Repo: "o/r", EntityType: "issue", EntityID: id, Facts: `{}`, AsOf: "x"},
			0, hydrated, true, &fp, []string{"reconcile"}, "sweep", "2026-09-29T10:00:00Z"); err != nil {
			t.Fatal(err)
		}
		if err := s.SetEntityColumns("o/r", "issue", id, nil, &yes); err != nil {
			t.Fatal(err)
		}
	}
	// bd-1 was last READ successfully 45 minutes ago, though hydrated 30 minutes ago.
	ok := "2026-10-01T11:15:00Z"
	if err := s.SetEntityReadTimes("o/r", "issue", "bd-1", nil, &ok); err != nil {
		t.Fatal(err)
	}
	body = scrape(t, s)
	for _, want := range []string{
		`pg_desk_watched_entities{backend="",type="issue"} 2`,
		`pg_desk_oldest_orphan_read_age_seconds{backend=""} 2700`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("two orphans: body missing %q; full body:\n%s", want, body)
		}
	}

	old := store.OpenForTest(t)
	mustUpsertInterpretation(t, old, store.Interpretation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "1",
		Panel: PanelMineAwaitingMe, AsOf: "2026-10-01T11:59:00Z",
	})
	if body := scrape(t, old); strings.Contains(body, "pg_desk_watched_entities") || strings.Contains(body, "pg_desk_oldest_orphan_read_age_seconds") {
		t.Errorf("an old-schema store has watched or orphan series:\n%s", body)
	}
}

// TestMetricsScrapeOnASchema2StoreWithNoLedgerServesTheStaleGaugeAndNoRetiredName
// is the P9a-1 guard: the metrics callback has no sync reads, so a migrated
// schema-2 store with no ledger still serves the kept stale-snapshot gauge
// (fresh, then stale as the heartbeat ages), and none of the retired
// sync-era metric names appears.
func TestMetricsScrapeOnASchema2StoreWithNoLedgerServesTheStaleGaugeAndNoRetiredName(t *testing.T) {
	setClock(t, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	n := store.OpenNewSchemaForTest(t)
	mustUpsertInterpretation(t, n, store.Interpretation{
		Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Panel: PanelMineAwaitingMe, AsOf: "2026-10-05T11:59:00Z",
	})
	mustSetMeta(t, n, store.MetaKeyLastHeartbeat, "2026-10-05T11:59:30Z")
	body := scrape(t, n)
	if !strings.Contains(body, "pg_desk_dashboard_stale 0") {
		t.Errorf("fresh heartbeat: want pg_desk_dashboard_stale 0:\n%s", body)
	}
	mustSetMeta(t, n, store.MetaKeyLastHeartbeat, "2026-10-05T09:00:00Z")
	if body = scrape(t, n); !strings.Contains(body, "pg_desk_dashboard_stale 1") {
		t.Errorf("old heartbeat: want pg_desk_dashboard_stale 1:\n%s", body)
	}
	for _, retired := range []string{
		"pg_desk_sync_error_rows", "pg_desk_oldest_sync_error_age_seconds",
		"pg_desk_sync_error_retrying_rows", "pg_desk_sync_error_exhausted_rows",
		"pg_desk_sync_errors", "pg_desk_oldest_anchor_check_age_seconds", "pg_desk_reconcile_",
	} {
		if strings.Contains(body, retired) {
			t.Errorf("retired metric %s is still emitted:\n%s", retired, body)
		}
	}
}
