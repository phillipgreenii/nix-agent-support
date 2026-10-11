package httpapi

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// The focus families on the real serve scrape (daily-focus store-first
// design, observability): computed from the stored rows at scrape time.

const focusScrapeRepo = "o/r"

func focusConfig() *config.Config {
	cfg := testConfig()
	cfg.Repos = []config.RepoConfig{{Remote: focusScrapeRepo}}
	cfg.BeadIDPattern = `^bd-`
	cfg.Focus.TimeZone = "UTC"
	cfg.Watch.PR.Queries = []string{"mine"}
	return cfg
}

func focusScrape(t *testing.T, s *store.Store) string {
	t.Helper()
	handler, err := NewHandler(s, focusConfig())
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

const focusAt = "2026-10-10T08:00:00Z"

// seedFocusStore writes: an open PR with an open (unclaimed, never claimed)
// minted bead, a merged PR whose bead is held by the terminal-source hold, an
// open PR with no bead (unminted), a draft, and select runs of every outcome.
func seedFocusStore(t *testing.T) *store.Store {
	t.Helper()
	s := store.OpenNewSchemaForTest(t)
	setClock(t, time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC))
	write := func(typ, id, facts string) {
		t.Helper()
		e := store.Entity{Repo: focusScrapeRepo, EntityType: typ, EntityID: id, Facts: facts, AsOf: focusAt, ContentHash: "h" + id}
		if _, err := s.WriteEntityStateWithLog(e, 0, focusAt, true, []string{"created"}, "sync", focusAt); err != nil {
			t.Fatal(err)
		}
	}
	write("pr", "1", `{"pr_show":{"state":"open"}}`)
	write("pr", "2", `{"pr_show":{"state":"closed","merged":true}}`)
	write("pr", "3", `{"pr_show":{"state":"open"}}`)
	write("issue", "bd-1", `{"issue_show":{"id":"bd-1","state":"open","created_at":"2026-10-10T10:00:00Z","metadata":{"source_id":"1","source_type":"pr"}}}`)
	write("issue", "bd-2", `{"issue_show":{"id":"bd-2","state":"deferred","metadata":{"source_id":"2","source_type":"pr","focus_hold":"struck"}}}`)
	for bead, src := range map[string]string{"bd-1": "1", "bd-2": "2"} {
		err := s.ReplaceDerivedXrefs(focusScrapeRepo, "issue", bead, []store.XrefLink{{
			Repo: focusScrapeRepo, FromType: "issue", FromID: bead, ToType: "pr", ToID: src,
			Relation: "source", Origin: "derived:test", FirstSeen: focusAt, LastConfirmed: focusAt,
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: focusScrapeRepo, EntityType: "pr", EntityID: "1", Panel: PanelMineAwaitingMe, AsOf: focusAt,
	})
	if err := s.SetMeta("change_flow.listing.pr.mine", `{"status":"degraded","reason":"truncated","at":"`+focusAt+`"}`); err != nil {
		t.Fatal(err)
	}
	err := s.FocusLockTx(func(tx *store.FocusTx) error {
		p, err := tx.GetOrCreatePeriod("day", "2026-10-10")
		if err != nil {
			return err
		}
		for i, id := range []string{"1", "2", "3"} {
			if _, err := tx.InsertSelection(store.FocusSelection{
				FocusPeriodID: p.ID, Repo: focusScrapeRepo, EntityType: "pr", EntityID: id,
				SelectedAt: "2026-10-10T10:30:00Z", RankPosition: i + 1,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2", "3"} {
		if err := s.SetAnnotation(store.KVAnnotation{
			Repo: focusScrapeRepo, EntityType: "pr", EntityID: id, Key: store.AnnotationFocusSelected,
			Value: "2026-10-10", Origin: "pg-desk", SetBy: "operator", SetAt: focusAt,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// A replan draft over the locked plan.
	if changed, err := s.FocusDraftReplan(store.FocusDraft{PeriodType: "day", PeriodKey: "2026-10-10", Cap: 6, MadeAt: "2026-10-10T11:00:00Z", BodyJSON: "{}"}, true); err != nil || changed {
		t.Fatalf("FocusDraftReplan = %v, %v", changed, err)
	}
	exit := func(n int) *int { return &n }
	for i, r := range []store.FocusRun{
		{Verb: "select", ExitCode: exit(6), CountsJSON: `{}`},
		{Verb: "select", ExitCode: exit(3), CountsJSON: `{}`},
		{Verb: "select", ExitCode: exit(2), CountsJSON: `{}`},
		{Verb: "select", ExitCode: exit(0), CountsJSON: `{"selected":0,"removed":{"operator":0}}`},
		// The newest select: the last-lock series come from this row.
		{Verb: "select", ExitCode: exit(0), CountsJSON: `{"selected":3,"removed":{"operator":0},"draft_age_seconds":3600,"drift_rows":1}`},
		{Verb: "pull", ExitCode: exit(1), CountsJSON: `{}`},
		{Verb: "close", ExitCode: nil, CountsJSON: `{}`}, // an unfinalised run: counted as total
	} {
		r.RunID = "RUN" + string(rune('A'+i))
		r.StartedAt = "2026-10-10T09:0" + string(rune('0'+i)) + ":00Z"
		r.Actor = "tester"
		if _, err := s.FocusRunInsert(r); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

var focusLine = regexp.MustCompile(`^(pg_desk_focus_[a-z_]+|pg_desk_watch_listing_status)(\{([^}]*)\})? (\S+)$`)

// TestFocusMetricsFamilies scrapes the real serve handler over a seeded store
// and checks every family: its name, its exact closed label set and a value.
func TestFocusMetricsFamilies(t *testing.T) {
	body := focusScrape(t, seedFocusStore(t))
	for _, want := range []string{
		`pg_desk_focus_selected 3`,
		`pg_desk_focus_beads{state="open"} 1`,
		`pg_desk_focus_beads{state="held"} 1`,
		`pg_desk_focus_beads{state="claimed"} 0`,
		`pg_desk_focus_beads{state="deferred_other"} 0`,
		`pg_desk_focus_beads{state="blocked"} 0`,
		`pg_desk_focus_beads{state="closed"} 0`,
		`pg_desk_focus_oldest_bead_age_seconds{state="open"} 7200`,
		`pg_desk_focus_pending_beads{reason="unminted"} 1`,
		`pg_desk_focus_pending_beads{reason="source_terminal"} 1`,
		`pg_desk_focus_pending_beads{reason="hidden"} 0`,
		`pg_desk_focus_pending_beads{reason="suppressed"} 0`,
		`pg_desk_focus_pending_beads{reason="no_bead_by_design"} 0`,
		`pg_desk_focus_pending_beads{reason="unwatched"} 0`,
		`pg_desk_focus_oldest_pending_bead_age_seconds{reason="unminted"} 5400`,
		`pg_desk_focus_divergence{kind="selected_bead_held"} 0`,
		`pg_desk_focus_divergence{kind="terminal_source_bead_open"} 0`,
		`pg_desk_focus_divergence{kind="row_without_annotation"} 0`,
		`pg_desk_focus_divergence{kind="annotation_without_row"} 0`,
		`pg_desk_focus_divergence{kind="stale_period"} 0`,
		`pg_desk_focus_divergence{kind="removed_bead_open_unclaimed"} 0`,
		`pg_desk_focus_divergence{kind="removed_bead_claimed"} 0`,
		`pg_desk_focus_unwatched_selected 0`,
		`pg_desk_focus_runs{outcome="ok",verb="select"} 1`,
		`pg_desk_focus_runs{outcome="empty",verb="select"} 1`,
		`pg_desk_focus_runs{outcome="partial",verb="select"} 1`,
		`pg_desk_focus_runs{outcome="total",verb="select"} 1`,
		`pg_desk_focus_runs{outcome="period_closed",verb="select"} 1`,
		`pg_desk_focus_runs{outcome="usage",verb="pull"} 1`,
		`pg_desk_focus_runs{outcome="total",verb="close"} 1`,
		`pg_desk_focus_run_age_seconds 10560`,
		`pg_desk_focus_rank_degraded_inputs{input="unparseable_due"} 0`,
		`pg_desk_focus_rank_degraded_inputs{input="unmapped_priority"} 0`,
		`pg_desk_focus_rank_degraded_inputs{input="age_fallback"} `,
		`pg_desk_focus_rank_degraded_inputs{input="unblocks_unavailable"} `,
		`pg_desk_focus_rank_degraded_inputs{input="status_category_absent"} 0`,
		`pg_desk_watch_listing_status{status="degraded",type="pr"} 1`,
		`pg_desk_watch_listing_status{status="ok",type="pr"} 0`,
		`pg_desk_watch_listing_status{status="failed",type="pr"} 0`,
		`pg_desk_focus_candidates{source="pr"} `,
		`pg_desk_focus_candidates{source="jira"} 0`,
		`pg_desk_focus_candidates{source="bead"} 0`,
		`pg_desk_focus_linked_candidates `,
		`pg_desk_focus_draft_age_seconds 3600`,
		`pg_desk_focus_plan_rows{state="in_plan"} 2`,
		`pg_desk_focus_plan_rows{state="finished"} 1`,
		`pg_desk_focus_plan_rows{state="open_slots"} 4`,
		`pg_desk_focus_last_lock_draft_age_seconds 3600`,
		`pg_desk_focus_last_lock_drift_rows 1`,
		`pg_desk_focus_jira_status_category_absent 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q\n%s", want, focusOnly(body))
		}
	}

	// The closed label vocabulary: no series carries an entity key or a bead
	// id, and every focus line parses.
	allowed := map[string]bool{"state": true, "reason": true, "kind": true, "verb": true, "outcome": true, "input": true, "type": true, "status": true, "source": true}
	n := 0
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "pg_desk_focus_") && !strings.HasPrefix(line, "pg_desk_watch_listing_status") {
			continue
		}
		m := focusLine.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("unparseable focus line %q", line)
			continue
		}
		n++
		for _, kv := range regexp.MustCompile(`(\w+)="[^"]*"`).FindAllStringSubmatch(m[3], -1) {
			if !allowed[kv[1]] {
				t.Errorf("%q carries label %q", line, kv[1])
			}
		}
		for _, banned := range []string{"o/r#", "bd-1", "bd-2"} {
			if strings.Contains(m[3], banned) {
				t.Errorf("%q carries an entity key or bead id", line)
			}
		}
	}
	if n == 0 {
		t.Fatal("no focus series on a migrated store")
	}
}

func focusOnly(body string) string {
	var out []string
	for _, l := range strings.Split(body, "\n") {
		if strings.Contains(l, "pg_desk_focus_") || strings.Contains(l, "pg_desk_watch_listing") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// TestFocusMetricsAbsentOnAnUnmigratedStoreAndScrapeStillSucceeds pins that an
// old-schema store emits no focus series and never fails the scrape.
func TestFocusMetricsAbsentOnAnUnmigratedStoreAndScrapeStillSucceeds(t *testing.T) {
	s := store.OpenForTest(t)
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "1",
		Panel: PanelMineAwaitingMe, AsOf: "2026-09-16T12:00:00Z",
	})
	setClock(t, time.Date(2026, 9, 16, 12, 0, 30, 0, time.UTC))
	body := focusScrape(t, s)
	if !strings.Contains(body, "pg_desk_liveness 1") {
		t.Errorf("dashboard families missing:\n%s", body)
	}
	if got := focusOnly(body); got != "" {
		t.Errorf("focus series on an unmigrated store:\n%s", got)
	}
}

// TestFocusDraftAgeAbsentWithoutAStoredDraft pins the draft-age series is
// absent (not zero) when no draft is stored, while the other families stay.
func TestFocusDraftAgeAbsentWithoutAStoredDraft(t *testing.T) {
	s := store.OpenNewSchemaForTest(t)
	setClock(t, time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC))
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: focusScrapeRepo, EntityType: "pr", EntityID: "1", Panel: PanelMineAwaitingMe, AsOf: focusAt,
	})
	body := focusScrape(t, s)
	if strings.Contains(body, "pg_desk_focus_draft_age_seconds") {
		t.Errorf("draft age present with no stored draft:\n%s", focusOnly(body))
	}
	if !strings.Contains(body, "pg_desk_focus_selected 0") {
		t.Errorf("a migrated empty store should still expose the families:\n%s", focusOnly(body))
	}
	for _, absent := range []string{"pg_desk_focus_run_age_seconds", "pg_desk_focus_last_lock_drift_rows", "pg_desk_focus_oldest_bead_age_seconds"} {
		if strings.Contains(body, absent) {
			t.Errorf("%s present with no source value", absent)
		}
	}
}
