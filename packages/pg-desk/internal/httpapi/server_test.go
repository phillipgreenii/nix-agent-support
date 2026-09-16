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
	handler := NewHandler(s, testConfig())

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

	handler := NewHandler(s, testConfig())
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

// TestMetricsSmoke is the acceptance criterion "/metrics exposes what D24
// requires, nothing more": once ready, GET /metrics returns 200, a
// Prometheus-text content type, and valid exposition text.
func TestMetricsSmoke(t *testing.T) {
	s := store.OpenForTest(t)
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "1",
		Panel: PanelMineActNow, AsOf: "2026-09-16T12:00:00Z",
	})

	handler := NewHandler(s, testConfig())
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want a text/plain prefix", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "# TYPE pg_desk_up gauge") || !strings.Contains(body, "pg_desk_up 1") {
		t.Fatalf("body does not look like valid Prometheus exposition text: %q", body)
	}
}

// TestPayloadGoldenMatchesGrafanaSelectors is the acceptance criterion
// "Payload golden matches the Grafana selector list byte-for-byte on the
// fields Grafana reads." The five root_selector strings and every per-row
// column selector below were read directly from
// phillipgreenii-nix-support-apps's
// darwin/modules/observability/dashboards/pg-pr.json (read-only reference;
// that repo's own file, packet 12's to modify, is never touched here):
//
//   - Mine panels (mine_act_now, mine_awaiting_others,
//     mine_awaiting_other_things) read: number, title, url, draft,
//     build_state, human_approved, agent_approved, has_conflicts, bot_verdict.
//   - Team panels (team_act_now, team_blocked) read: number, title, url,
//     owner, build_state, human_approved, agent_approved, has_conflicts,
//     bot_verdict, match_team_authored, match_review_requested,
//     match_has_watch_label, self_approval_state, self_commented,
//     files_changed, lines_changed.
//   - The root reads: dropped_count.
//
// The fixture stuffs those exact field names into the interpretation row's
// enrichment/approvals blobs plus match_reasons — precisely what gather and
// interpret (this docket's packets 4 and 5, not yet landed) are expected to
// populate — and this test proves buildRow's flatten mechanism projects them
// onto the served row unchanged, byte-for-byte on every key name above, and
// that the fixed payload matches the checked-in golden file exactly.
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
		Enrichment: `{"number":101,"title":"Fix flaky test","url":"https://github.com/acme/widgets/pull/101","draft":false}`,
		Approvals:  `{"build_state":"passing","human_approved":true,"agent_approved":false,"has_conflicts":false,"bot_verdict":"no_decision"}`,
		Panel:      PanelMineActNow, ReadyToPromote: true, Degraded: false,
		AsOf: "2026-09-16T12:00:00Z",
	})
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "202",
		Ownership: "team", Category: "feature", GateState: "unsatisfied",
		Enrichment:   `{"number":202,"title":"Add widget export","url":"https://github.com/acme/widgets/pull/202","owner":"teammate1","files_changed":5,"lines_changed":120}`,
		Approvals:    `{"build_state":"broken","human_approved":false,"agent_approved":true,"has_conflicts":false,"bot_verdict":"disapproved","self_approval_state":"not_approved","self_commented":true}`,
		MatchReasons: `["team-authored","label:urgent"]`,
		Panel:        PanelTeamActNow, ReadyToPromote: false, Degraded: true,
		AsOf: "2026-09-16T11:55:00Z",
	})
	mustUpsertInterpretation(t, s, store.Interpretation{
		Repo: "acme/widgets", EntityType: "pull_request", EntityID: "303",
		Ownership: "mine", Category: "chore",
		Enrichment: `{"number":303,"title":"WIP cleanup","url":"https://github.com/acme/widgets/pull/303","draft":true}`,
		Panel:      PanelMineAwaitingOthers, ReadyToPromote: false, Degraded: false,
		AsOf: "2026-09-16T10:00:00Z",
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

	// Byte-for-byte on the fields Grafana reads: every mine/team column
	// selector must be present, spelled exactly as pg-pr.json's Infinity
	// datasource columns spell them.
	mineColumns := []string{"number", "title", "url", "draft", "build_state", "human_approved", "agent_approved", "has_conflicts", "bot_verdict"}
	teamColumns := []string{
		"number", "title", "url", "owner", "build_state", "human_approved", "agent_approved", "has_conflicts", "bot_verdict",
		"match_team_authored", "match_review_requested", "match_has_watch_label", "self_approval_state", "self_commented", "files_changed", "lines_changed",
	}

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
