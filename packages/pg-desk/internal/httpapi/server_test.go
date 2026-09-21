package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func testConfig() *config.Config {
	return &config.Config{HeartbeatPeriod: "60s"}
}

func setClock(t *testing.T, fixed time.Time) {
	t.Helper()
	orig := nowUTC
	nowUTC = func() time.Time { return fixed }
	t.Cleanup(func() { nowUTC = orig })
}

// TestReadyGate503BeforeFirstInterpretation is the acceptance criterion
// "503 before first interpretation; 200 with the five-panel payload after" —
// the "before" half, for BOTH routes this package registers (Binding
// decisions: "503 for every route").
func TestReadyGate503BeforeFirstInterpretation(t *testing.T) {
	s := store.OpenForTest(t)
	handler, err := NewHandler(s, testConfig())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	for _, path := range []string{"/api/v1/dashboard", "/metrics"} {
		t.Run(path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s: status = %d, want %d", path, rr.Code, http.StatusServiceUnavailable)
			}
		})
	}
}

// TestDashboard200AfterFirstInterpretation is the "after" half of the same
// acceptance criterion: once the store holds one interpretation row, the
// dashboard route returns 200 with that row in its panel and every other
// panel present as an empty array (never null).
func TestDashboard200AfterFirstInterpretation(t *testing.T) {
	s := store.OpenForTest(t)
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "1",
		Panel: PanelMineActNow, AsOf: "2026-09-16T12:00:00Z",
	})
	mustSetMeta(t, s, store.MetaKeyLastHeartbeat, "2026-09-16T12:00:00Z")
	setClock(t, time.Date(2026, 9, 16, 12, 0, 30, 0, time.UTC))

	handler, err := NewHandler(s, testConfig())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var payload Payload
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.MineActNow) != 1 {
		t.Fatalf("MineActNow = %+v, want exactly 1 row", payload.MineActNow)
	}
	for name, got := range map[string][]Row{
		"mine_awaiting_others":       payload.MineAwaitingOthers,
		"mine_awaiting_other_things": payload.MineAwaitingOtherThings,
		"team_act_now":               payload.TeamActNow,
		"team_blocked":               payload.TeamBlocked,
		"hidden":                     payload.Hidden,
	} {
		if got == nil || len(got) != 0 {
			t.Fatalf("%s = %#v, want a non-nil empty array", name, got)
		}
	}
}

// TestStaleFlag is the acceptance criterion "stale: true fires correctly
// relative to meta.last_heartbeat," pinned against the Binding decisions
// section's exact bound: older than TWO heartbeat_periods.
func TestStaleFlag(t *testing.T) {
	const heartbeatPeriod = 60 * time.Second
	base := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name          string
		lastHeartbeat string
		now           time.Time
		wantStale     bool
		wantAge       int
	}{
		{"just heartbeat", base.Format(time.RFC3339), base, false, 0},
		{"inside the two-period bound", base.Format(time.RFC3339), base.Add(119 * time.Second), false, 119},
		{"exactly at the bound is not yet stale", base.Format(time.RFC3339), base.Add(2 * heartbeatPeriod), false, 120},
		{"one second past the bound is stale", base.Format(time.RFC3339), base.Add(2*heartbeatPeriod + time.Second), true, 121},
		{"missing heartbeat is stale (fail closed)", "", base, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := store.OpenForTest(t)
			mustUpsertInterpretation(t, s, store.Interpretation{
				Repo: "acme/widgets", EntityType: "pull_request", EntityID: "1",
				Panel: PanelMineActNow, AsOf: base.Format(time.RFC3339),
			})
			if tc.lastHeartbeat != "" {
				mustSetMeta(t, s, store.MetaKeyLastHeartbeat, tc.lastHeartbeat)
			}
			setClock(t, tc.now)

			payload, err := BuildPayload(s, testConfig(), nowUTC())
			if err != nil {
				t.Fatalf("BuildPayload: %v", err)
			}
			if payload.Stale != tc.wantStale {
				t.Fatalf("Stale = %v, want %v", payload.Stale, tc.wantStale)
			}
			if payload.AgeSeconds != tc.wantAge {
				t.Fatalf("AgeSeconds = %d, want %d", payload.AgeSeconds, tc.wantAge)
			}
			if payload.SyncIntervalSeconds != 60 {
				t.Fatalf("SyncIntervalSeconds = %d, want 60", payload.SyncIntervalSeconds)
			}
			if payload.StaleAfterSeconds != 120 {
				t.Fatalf("StaleAfterSeconds = %d, want 120", payload.StaleAfterSeconds)
			}
		})
	}
}

// TestHiddenArrayExcludesFromPanel is the acceptance criterion covering the
// hidden-array binding decision: a row whose PR-level annotation is hidden
// leaves its would-be panel entirely and appears only in Hidden, still
// carrying the three new per-row fields.
func TestHiddenArrayExcludesFromPanel(t *testing.T) {
	s := store.OpenForTest(t)
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "9",
		Panel: PanelMineAwaitingOthers, ReadyToPromote: true, Degraded: true,
		AsOf: "2026-09-16T10:00:00Z",
	})
	hidden := true
	if err := s.UpsertAnnotation(store.Annotation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "9",
		Hidden: &hidden, HiddenReason: "waiting on design review",
		SetBy: "operator", SetAt: "2026-09-16T09:00:00Z",
	}); err != nil {
		t.Fatalf("UpsertAnnotation: %v", err)
	}
	mustSetMeta(t, s, store.MetaKeyLastHeartbeat, "2026-09-16T10:00:00Z")
	setClock(t, time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC))

	payload, err := BuildPayload(s, testConfig(), nowUTC())
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	if len(payload.MineAwaitingOthers) != 0 {
		t.Fatalf("MineAwaitingOthers = %+v, want empty (row is hidden)", payload.MineAwaitingOthers)
	}
	if len(payload.Hidden) != 1 {
		t.Fatalf("Hidden = %+v, want exactly 1 row", payload.Hidden)
	}
	row := payload.Hidden[0]
	if row["entity_id"] != "9" {
		t.Fatalf("Hidden[0][entity_id] = %v, want %q", row["entity_id"], "9")
	}
	if row["ready_to_promote"] != true {
		t.Fatalf("Hidden[0][ready_to_promote] = %v, want true", row["ready_to_promote"])
	}
	if row["degraded"] != true {
		t.Fatalf("Hidden[0][degraded] = %v, want true", row["degraded"])
	}
}

// TestMetricsSmoke is the acceptance criterion "pg-desk serve exposes the
// full §8 catalog on its existing /metrics route": once ready, GET
// /metrics returns 200, a Prometheus-text content type, and every catalog
// member's name appears in the exposition text.
func TestMetricsSmoke(t *testing.T) {
	s := store.OpenForTest(t)
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "1",
		Panel: PanelMineActNow, AsOf: "2026-09-16T12:00:00Z",
	})
	mustSetMeta(t, s, store.MetaKeyLastHeartbeat, "2026-09-16T12:00:00Z")
	setClock(t, time.Date(2026, 9, 16, 12, 0, 30, 0, time.UTC))

	handler, err := NewHandler(s, testConfig())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want a text/plain prefix", ct)
	}
	body := rr.Body.String()
	// The payload is fresh (30s old against a 120s stale-after bound), so
	// pg_desk_dashboard_stale MUST read 0 here — the acceptance criterion's
	// own explicit polarity check (0 = fresh, 1 = stale; the opposite of a
	// presence-style gauge).
	for _, want := range []string{
		"# TYPE pg_desk_liveness gauge",
		"pg_desk_liveness 1",
		"# TYPE pg_desk_dashboard_age_seconds gauge",
		"pg_desk_dashboard_age_seconds 30",
		"# TYPE pg_desk_dashboard_stale gauge",
		"pg_desk_dashboard_stale 0",
		"# TYPE pg_desk_dropped gauge",
		"pg_desk_dropped 0",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q; full body:\n%s", want, body)
		}
	}
	// pg_desk_up (the old stub) MUST be gone — the whole point of this
	// docket is replacing it.
	if strings.Contains(body, "pg_desk_up") {
		t.Fatalf("body still contains the retired pg_desk_up stub:\n%s", body)
	}
}

// TestMetricsSmoke_StalePolarity is the same acceptance criterion's other
// half: once the payload is stale, pg_desk_dashboard_stale MUST flip to 1.
func TestMetricsSmoke_StalePolarity(t *testing.T) {
	s := store.OpenForTest(t)
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "1",
		Panel: PanelMineActNow, AsOf: "2026-09-16T12:00:00Z",
	})
	mustSetMeta(t, s, store.MetaKeyLastHeartbeat, "2026-09-16T12:00:00Z")
	// 121s past a 60s heartbeat period (120s stale-after bound): stale.
	setClock(t, time.Date(2026, 9, 16, 12, 2, 1, 0, time.UTC))

	handler, err := NewHandler(s, testConfig())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "pg_desk_dashboard_stale 1") {
		t.Fatalf("body missing %q (payload is stale); full body:\n%s", "pg_desk_dashboard_stale 1", body)
	}
}

// TestPayloadGoldenMatchesGrafanaSelectors is the acceptance criterion
// "Payload golden matches the Grafana selector list byte-for-byte on the
// fields Grafana reads." Every per-row column selector below was read
// directly from phillipgreenii-nix-support-apps's
// darwin/modules/observability/dashboards/pg-desk.json (read-only reference;
// that repo's own file is never touched here). Commit bde10e9 (pg2-060c7,
// landed in that repo 2026-09-18) retired this dashboard's old column set —
// number, title, url, draft, build_state, agent_approved, has_conflicts,
// owner, self_approval_state, self_commented, files_changed, lines_changed —
// verifying against the real running server and this package's buildRow
// (plus internal/interpret's Enrichment/Approvals structs, which now back
// this row: neither carries any of those field names) that none of them are
// emitted by the current Row shape. Only "number" got a replacement
// (entity_id); the rest have none:
//
//   - Mine panels (mine_act_now, mine_awaiting_others,
//     mine_awaiting_other_things) read: entity_id, human_approved,
//     bot_verdict, ready_to_promote, degraded, sync_error.
//   - Team panels (team_act_now, team_blocked) read: entity_id,
//     human_approved, bot_verdict, match_team_authored,
//     match_review_requested, match_has_watch_label, ready_to_promote,
//     degraded, sync_error.
//   - The hidden panel (hidden) reads: entity_id, category,
//     ready_to_promote, degraded, sync_error.
//   - The root reads: dropped_count, age_seconds.
//
// sync_error is exercised via the hidden-panel fixture (entity 303) rather
// than duplicated on every fixture below: buildRow sets it through the same
// setIfNonEmpty call regardless of which panel the row lands in (see
// buildRow above), so proving it once is sufficient.
//
// The fixture stuffs those exact field names into the interpretation row's
// approvals blob plus match_reasons — this test proves buildRow's flatten
// mechanism projects them onto the served row unchanged, byte-for-byte on
// every key name above, and that the fixed payload matches the checked-in
// golden file exactly.
func TestPayloadGoldenMatchesGrafanaSelectors(t *testing.T) {
	// The five panel keys and "hidden", pinned against the design doc and
	// the Grafana JSON's root_selector values.
	wantPanelKeys := []string{
		PanelMineActNow, PanelMineAwaitingOthers, PanelMineAwaitingOtherThings,
		PanelTeamActNow, PanelTeamBlocked, "hidden",
	}
	for _, want := range []string{
		"mine_act_now", "mine_awaiting_others", "mine_awaiting_other_things",
		"team_act_now", "team_blocked",
	} {
		found := false
		for _, k := range wantPanelKeys {
			if k == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("panel constant list is missing the Grafana root_selector %q", want)
		}
	}

	s := store.OpenForTest(t)

	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "101",
		Ownership: "mine", Category: "bug", GateState: "satisfied",
		Approvals: `{"human_approved":true,"bot_verdict":"no_decision"}`,
		Panel:     PanelMineActNow, ReadyToPromote: true, Degraded: false,
		AsOf: "2026-09-16T12:00:00Z",
	})
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "202",
		Ownership: "team", Category: "feature", GateState: "unsatisfied",
		Approvals:    `{"human_approved":false,"bot_verdict":"disapproved"}`,
		MatchReasons: `["team-authored","label:urgent"]`,
		Panel:        PanelTeamActNow, ReadyToPromote: false, Degraded: true,
		AsOf: "2026-09-16T11:55:00Z",
	})
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "303",
		Ownership: "mine", Category: "chore",
		Panel: PanelMineAwaitingOthers, ReadyToPromote: false, Degraded: false,
		// SyncError is the one row exercising the sync_error Grafana
		// selector (see the doc comment above): buildRow sets it uniformly
		// regardless of destination panel, so a single fixture suffices.
		SyncError: "sync timeout",
		AsOf:      "2026-09-16T10:00:00Z",
	})
	hidden := true
	if err := s.UpsertAnnotation(store.Annotation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "303",
		Hidden: &hidden, HiddenReason: "not ready",
		SetBy: "operator", SetAt: "2026-09-16T09:55:00Z",
	}); err != nil {
		t.Fatalf("UpsertAnnotation: %v", err)
	}

	mustSetMeta(t, s, store.MetaKeyLastHeartbeat, "2026-09-16T12:00:00Z")
	mustSetMeta(t, s, store.MetaKeyLastRun, "2026-09-16T11:59:00Z")
	mustSetMeta(t, s, store.MetaKeyLastSweep, "2026-09-16T11:00:00Z")

	fixedNow := time.Date(2026, 9, 16, 12, 0, 30, 0, time.UTC)
	payload, err := BuildPayload(s, testConfig(), fixedNow)
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	gotBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	// Byte-for-byte on the fields Grafana reads: every mine/team/hidden
	// column selector must be present, spelled exactly as pg-desk.json's
	// Infinity datasource columns spell them.
	mineColumns := []string{"entity_id", "human_approved", "bot_verdict", "ready_to_promote", "degraded"}
	teamColumns := []string{
		"entity_id", "human_approved", "bot_verdict",
		"match_team_authored", "match_review_requested", "match_has_watch_label", "ready_to_promote", "degraded",
	}
	hiddenColumns := []string{"entity_id", "category", "ready_to_promote", "degraded", "sync_error"}

	if len(payload.MineActNow) != 1 {
		t.Fatalf("MineActNow = %+v, want exactly 1 row", payload.MineActNow)
	}
	requireColumns(t, "mine_act_now[0]", payload.MineActNow[0], mineColumns)

	if len(payload.TeamActNow) != 1 {
		t.Fatalf("TeamActNow = %+v, want exactly 1 row", payload.TeamActNow)
	}
	requireColumns(t, "team_act_now[0]", payload.TeamActNow[0], teamColumns)

	if len(payload.Hidden) != 1 {
		t.Fatalf("Hidden = %+v, want exactly 1 row (entity 303, excluded from mine_awaiting_others)", payload.Hidden)
	}
	requireColumns(t, "hidden[0]", payload.Hidden[0], hiddenColumns)
	if len(payload.MineAwaitingOthers) != 0 {
		t.Fatalf("MineAwaitingOthers = %+v, want empty (entity 303 is hidden)", payload.MineAwaitingOthers)
	}

	// dropped_count is the one root-level Grafana selector.
	if payload.DroppedCount != 0 {
		t.Fatalf("DroppedCount = %d, want 0", payload.DroppedCount)
	}

	goldenPath := filepath.Join("testdata", "dashboard_golden.json")
	wantBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v", goldenPath, err)
	}

	var got, want any
	if err := json.Unmarshal(gotBytes, &got); err != nil {
		t.Fatalf("unmarshal actual payload: %v", err)
	}
	if err := json.Unmarshal(wantBytes, &want); err != nil {
		t.Fatalf("unmarshal golden %s: %v", goldenPath, err)
	}
	if !reflect.DeepEqual(got, want) {
		gotPretty, _ := json.MarshalIndent(got, "", "  ")
		wantPretty, _ := json.MarshalIndent(want, "", "  ")
		t.Fatalf("payload does not match golden %s:\n--- got ---\n%s\n--- want ---\n%s", goldenPath, gotPretty, wantPretty)
	}
}

func requireColumns(t *testing.T, label string, row Row, columns []string) {
	t.Helper()
	for _, c := range columns {
		if _, ok := row[c]; !ok {
			t.Fatalf("%s: missing Grafana-selected column %q; row = %+v", label, c, row)
		}
	}
}

func mustUpsertInterpretation(t *testing.T, s *store.Store, i store.Interpretation) {
	t.Helper()
	if err := s.UpsertInterpretation(i); err != nil {
		t.Fatalf("UpsertInterpretation: %v", err)
	}
}

func mustSetMeta(t *testing.T, s *store.Store, key, value string) {
	t.Helper()
	if err := s.SetMeta(key, value); err != nil {
		t.Fatalf("SetMeta(%s): %v", key, err)
	}
}
