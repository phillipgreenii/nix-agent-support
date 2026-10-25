package httpapi

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// TestMetricsExposeTheOpenAnchorFamiliesFromTheStoresLinks: on a migrated
// store the open-anchor and stale-anchor gauges are exported with a value
// when healthy (0 on an empty store) and move with the store's links, which
// are read at scrape time (not the tracker, not the daemon, not the ledger).
func TestMetricsExposeTheOpenAnchorFamiliesFromTheStoresLinks(t *testing.T) {
	setClock(t, time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC))
	s := store.OpenNewSchemaForTest(t)
	mustUpsertInterpretation(t, s, store.Interpretation{Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Panel: PanelMineAwaitingMe, AsOf: "2026-10-10T00:00:00Z"})
	body := scrapeWithConfig(t, s, testConfig())
	requireLines(
		t, body,
		`pg_desk_pr_open_anchors 0`,
		`pg_desk_pr_open_anchors_hidden 0`,
		`pg_desk_pr_stale_anchors 0`,
		`pg_desk_pr_stale_anchor_oldest_age_seconds 0`,
		`pg_desk_pr_open_anchor_oldest_hydration_age_seconds 0`,
	)

	// One open PR hydrated 2 days ago and one merged PR hydrated 5 hours ago,
	// each with an open anchor, written through the store's own entity and
	// link writers.
	for n, p := range []struct{ facts, hydratedAt string }{
		{`{"pr_show":{"state":"open"}}`, "2026-10-09T00:00:00Z"},
		{`{"pr_show":{"state":"open","merged":true}}`, "2026-10-10T19:00:00Z"},
	} {
		prID := fmt.Sprintf("o/r#%d", n+1)
		anchorID := fmt.Sprintf("bd-%d", n+1)
		if err := s.UpsertEntity(store.Entity{Repo: "o/r", EntityType: "pr", EntityID: prID, Facts: p.facts, AsOf: p.hydratedAt}); err != nil {
			t.Fatal(err)
		}
		if err := s.UpsertEntity(store.Entity{
			Repo: "o/r", EntityType: "issue", EntityID: anchorID,
			Facts: `{"issue_show":{"issue_type":"merge-request","state":"open"}}`, AsOf: p.hydratedAt,
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceDerivedXrefs("o/r", "issue", anchorID, []store.XrefLink{{
			Repo: "o/r", FromType: "issue", FromID: anchorID, ToType: "pr", ToID: prID,
			Relation: "work", Origin: "derived:work-item", FirstSeen: p.hydratedAt, LastConfirmed: p.hydratedAt,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	// hydrated_at is stamped by the entity-change path; stamp it the way that
	// path does (WriteEntityStateWithLog), since UpsertEntity writes none.
	for n, at := range []string{"2026-10-09T00:00:00Z", "2026-10-10T19:00:00Z"} {
		e, _, err := s.GetEntity("o/r", "pr", fmt.Sprintf("o/r#%d", n+1))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.WriteEntityStateWithLog(e, e.Version, at, true, []string{"created"}, "poll", at); err != nil {
			t.Fatal(err)
		}
	}

	body = scrapeWithConfig(t, s, testConfig())
	requireLines(
		t, body,
		`pg_desk_pr_open_anchors 2`,
		`pg_desk_pr_open_anchors_hidden 0`,
		`pg_desk_pr_stale_anchors 1`,
		`pg_desk_pr_stale_anchor_oldest_age_seconds 18000`,
		`pg_desk_pr_open_anchor_oldest_hydration_age_seconds 172800`,
	)
}

// TestMetricsOmitTheOpenAnchorFamiliesOnAnOldSchemaStore: the families read
// schema-2 links, so an older store has no series for them (never a 0 that
// would read as healthy) and the scrape still succeeds.
func TestMetricsOmitTheOpenAnchorFamiliesOnAnOldSchemaStore(t *testing.T) {
	old := store.OpenForTest(t)
	mustUpsertInterpretation(t, old, store.Interpretation{Repo: "o/r", EntityType: "pull_request", EntityID: "1", Panel: PanelMineAwaitingMe, AsOf: "2026-10-10T00:00:00Z"})
	body := scrapeWithConfig(t, old, testConfig())
	if strings.Contains(body, "pg_desk_pr_open_anchor") || strings.Contains(body, "pg_desk_pr_stale_anchor") {
		t.Errorf("an old-schema store exports an anchor series:\n%s", body)
	}
	if !strings.Contains(body, "pg_desk_liveness 1") {
		t.Errorf("the rest of the scrape is missing:\n%s", body)
	}
}
