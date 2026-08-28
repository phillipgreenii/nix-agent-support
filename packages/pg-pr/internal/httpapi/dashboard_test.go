package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
)

func TestDashboard503WhenEmpty(t *testing.T) {
	s := snapshot.NewStore()
	srv := httptest.NewServer(DashboardHandler(s))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/dashboard")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503", resp.StatusCode)
	}
}

func TestDashboard200WhenPopulated(t *testing.T) {
	s := snapshot.NewStore()
	s.Set(&snapshot.Snapshot{
		GeneratedAt:         time.Unix(1700000000, 0).UTC(),
		SyncIntervalSeconds: 60,
		Mine:                []snapshot.MineRow{},
		Team:                []snapshot.TeamRow{},
	})
	srv := httptest.NewServer(DashboardHandler(s))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/dashboard")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type: got %q want application/json", ct)
	}
	var got snapshot.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SyncIntervalSeconds != 60 {
		t.Errorf("got interval %d want 60", got.SyncIntervalSeconds)
	}
}

// TestDashboardDroppedCountSerializesAsZero proves the JSON-encode
// present-vs-absent-scalar risk for Snapshot.DroppedCount (pg2-4dz88.7.6): a
// snapshot with nothing dropped must still round-trip the field as the
// numeral 0, not omit it — the concrete regression this guards against is
// DroppedCount someday becoming a `*int` or picking up an `omitempty` tag,
// either of which would make the key vanish from the payload precisely when
// its value is the zero value. Decoding into snapshot.Snapshot alone would
// not catch that (a missing key and an explicit 0 both decode to the Go zero
// value), so this asserts against the raw JSON object instead.
func TestDashboardDroppedCountSerializesAsZero(t *testing.T) {
	s := snapshot.NewStore()
	s.Set(&snapshot.Snapshot{
		GeneratedAt:         time.Unix(1700000000, 0).UTC(),
		SyncIntervalSeconds: 60,
		Mine:                []snapshot.MineRow{},
		Team:                []snapshot.TeamRow{},
		DroppedCount:        0,
	})
	srv := httptest.NewServer(DashboardHandler(s))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/dashboard")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var raw map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, present := raw["dropped_count"]
	if !present {
		t.Fatal("dropped_count key must be present even when nothing was dropped (no omitempty)")
	}
	if got == nil {
		t.Fatalf("dropped_count must not be null; got %v", got)
	}
	n, ok := got.(float64)
	if !ok {
		t.Fatalf("dropped_count must decode as a JSON number, got %T (%v)", got, got)
	}
	if n != 0 {
		t.Errorf("dropped_count = %v, want 0", n)
	}
}

// dashboardPanelSelectors is the LITERAL key list this bead's compensating
// check (pg2-4dz88.7.8, parent pg2-4dz88.7's design section 7) requires:
// "the expected key list as a literal derived from the dashboard's
// targets[0].columns[].selector values, so a renamed Go field fails a pg-pr
// test rather than silently emptying a column in production." It is
// deliberately a HAND-MAINTAINED mirror of
// phillipgreenii-nix-support-apps/darwin/modules/observability/dashboards/pg-pr.json's
// panel columns (a cross-repo file this hermetic Go test cannot read) — keep
// the two in sync whenever either changes.
//
// rootSelector is the Snapshot JSON key the panel's root_selector points at;
// columnSelectors are the per-row keys its columns.selector values name.
var dashboardPanelSelectors = []struct {
	rootSelector    string
	columnSelectors []string
}{
	{
		rootSelector: "team_act_now",
		columnSelectors: []string{
			"number", "title", "url", "owner",
			"build_state", "human_approved", "agent_approved", "has_conflicts",
			"bot_verdict", "match_team_authored", "match_review_requested", "match_has_watch_label",
			"self_approval_state", "self_commented",
			"files_changed", "lines_changed",
		},
	},
	{
		rootSelector: "team_blocked",
		columnSelectors: []string{
			"number", "title", "url", "owner",
			"build_state", "human_approved", "agent_approved", "has_conflicts",
			"bot_verdict", "match_team_authored", "match_review_requested", "match_has_watch_label",
			"self_approval_state", "self_commented",
			"files_changed", "lines_changed",
		},
	},
	{
		rootSelector: "mine_act_now",
		columnSelectors: []string{
			"number", "title", "url", "draft",
			"build_state", "human_approved", "agent_approved", "has_conflicts", "bot_verdict",
		},
	},
	{
		rootSelector: "mine_awaiting_others",
		columnSelectors: []string{
			"number", "title", "url", "draft",
			"build_state", "human_approved", "agent_approved", "has_conflicts", "bot_verdict",
		},
	},
	{
		rootSelector: "mine_awaiting_other_things",
		columnSelectors: []string{
			"number", "title", "url", "draft",
			"build_state", "human_approved", "agent_approved", "has_conflicts", "bot_verdict",
		},
	},
}

// requiredIndicatorSelectors is pg2-4dz88.7.5's full 7-indicator set, named
// by the JSON field each indicator category surfaces as (pg2-4dz88.7.8's own
// description: "all seven .7.5 indicator field NAMES appear as a selector in
// at least one panel, without constraining which panel or visual form
// renders each"). The match-reason category is a THREE-way breakdown
// (match_team_authored/match_review_requested/match_has_watch_label); any
// one of the three satisfies that category for this count, since the
// operator's ask was one indicator ("assigned reviewer/my team is a
// reviewer/has label"), not three independent ones.
var requiredIndicatorSelectors = []struct {
	category  string // one of pg2-4dz88.7.5's 7 named indicators
	anyOfKeys []string
}{
	{"build state (broken/pending/passing)", []string{"build_state"}},
	{"approval state (approved/needs approval)", []string{"human_approved"}},
	{"merge conflict", []string{"has_conflicts"}},
	{"bot verdict (approve/disapprove/no-decision)", []string{"bot_verdict"}},
	{"match reason (assigned reviewer/my team/has label)", []string{"match_team_authored", "match_review_requested", "match_has_watch_label"}},
	{"my review state (approved + staleness)", []string{"self_approval_state"}},
	{"have I commented", []string{"self_commented"}},
}

// TestDashboardPayloadFieldsMatchPanelColumns is pg2-4dz88.7.8's required
// compensating check (it cannot be a live Grafana test — the dashboard is
// only reachable through a cross-repo flake input plus a darwin apply, see
// pg2-4dz88.7's design section 7): every JSON key a panel selects
// (dashboardPanelSelectors above) must exist as a real key on the payload,
// at the array level the panel's root_selector names and the row level its
// columns name. A renamed/removed Go field fails HERE, in this repo's own
// test suite, rather than silently emptying a dashboard column in
// production.
func TestDashboardPayloadFieldsMatchPanelColumns(t *testing.T) {
	fullMine := snapshot.MineRow{
		Repo: "o/r", Number: 1, Title: "t", URL: "u", Draft: true,
		CIStatus: "success", HumanApproved: true, AgentApproved: true,
		HasConflicts: true, BuildState: "passing", BotVerdict: "approved",
	}
	fullTeam := snapshot.TeamRow{
		Repo: "o/r", Number: 1, Title: "t", Owner: "alice", URL: "u",
		CIStatus: "success", HumanApproved: true, AgentApproved: true,
		HasConflicts: true, BuildState: "passing", BotVerdict: "approved",
		MatchTeamAuthored: true, MatchReviewRequested: true, MatchHasWatchLabel: true,
		SelfApprovalState: "standing", SelfCommented: true,
		FilesChanged: 3, LinesChanged: 30,
	}
	s := snapshot.NewStore()
	s.Set(&snapshot.Snapshot{
		GeneratedAt:             time.Unix(1700000000, 0).UTC(),
		SyncIntervalSeconds:     60,
		Mine:                    []snapshot.MineRow{fullMine},
		Team:                    []snapshot.TeamRow{fullTeam},
		MineActNow:              []snapshot.MineRow{fullMine},
		MineAwaitingOthers:      []snapshot.MineRow{fullMine},
		MineAwaitingOtherThings: []snapshot.MineRow{fullMine},
		TeamActNow:              []snapshot.TeamRow{fullTeam},
		TeamBlocked:             []snapshot.TeamRow{fullTeam},
	})

	var raw map[string]interface{}
	{
		srv := httptest.NewServer(DashboardHandler(s))
		defer srv.Close()
		resp, err := http.Get(srv.URL + "/api/v1/dashboard")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}

	seenIndicatorKeys := map[string]bool{}
	for _, panel := range dashboardPanelSelectors {
		arrVal, present := raw[panel.rootSelector]
		if !present {
			t.Errorf("root_selector %q: key absent from payload", panel.rootSelector)
			continue
		}
		arr, ok := arrVal.([]interface{})
		if !ok || len(arr) == 0 {
			t.Errorf("root_selector %q: want a non-empty array in the fixture, got %T (%v)", panel.rootSelector, arrVal, arrVal)
			continue
		}
		row, ok := arr[0].(map[string]interface{})
		if !ok {
			t.Errorf("root_selector %q: row 0 is not a JSON object: %T", panel.rootSelector, arr[0])
			continue
		}
		for _, sel := range panel.columnSelectors {
			if _, present := row[sel]; !present {
				t.Errorf("root_selector %q: column selector %q does not exist on the payload row", panel.rootSelector, sel)
				continue
			}
			seenIndicatorKeys[sel] = true
		}
	}

	// pg2-4dz88.7's jq compensating check (support-apps side) additionally
	// requires all seven .7.5 indicator field names to appear as a selector
	// SOMEWHERE — cross-check that here too so a Go-side rename is caught by
	// this hermetic test rather than only by the cross-repo jq check.
	for _, ind := range requiredIndicatorSelectors {
		found := false
		for _, k := range ind.anyOfKeys {
			if seenIndicatorKeys[k] {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("indicator %q: none of %v appear as a selector in any panel", ind.category, ind.anyOfKeys)
		}
	}
}

// fixedNow pins the handler's serve-time clock for the test.
func fixedNow(t *testing.T, now time.Time) {
	t.Helper()
	prev := nowUTC
	t.Cleanup(func() { nowUTC = prev })
	nowUTC = func() time.Time { return now }
}

// getDashboard GETs the endpoint and decodes the payload.
func getDashboard(t *testing.T, s *snapshot.Store) snapshot.Snapshot {
	t.Helper()
	srv := httptest.NewServer(DashboardHandler(s))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/dashboard")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var got snapshot.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

// TestDashboardStampsFreshnessAtServeTime: the served payload carries the
// freshness bound plus an age/stale verdict computed at the moment of the
// request — NOT at the moment the daemon built the snapshot. The same held
// snapshot must read fresh early and stale once the bound has passed, which is
// the whole point: a wedged sync tick leaves the snapshot in place and only the
// serve-time verdict can catch it (pr-pool INV-FRESH-1).
func TestDashboardStampsFreshnessAtServeTime(t *testing.T) {
	generated := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	s := snapshot.NewStore()
	s.Set(&snapshot.Snapshot{
		GeneratedAt:         generated,
		SyncIntervalSeconds: 60,
		StaleAfterSeconds:   120,
		Mine:                []snapshot.MineRow{},
		Team:                []snapshot.TeamRow{},
	})

	fixedNow(t, generated.Add(30*time.Second))
	fresh := getDashboard(t, s)
	if fresh.StaleAfterSeconds != 120 {
		t.Errorf("stale_after_seconds: got %d want 120", fresh.StaleAfterSeconds)
	}
	if fresh.AgeSeconds != 30 {
		t.Errorf("age_seconds: got %d want 30", fresh.AgeSeconds)
	}
	if fresh.Stale {
		t.Errorf("a 30s-old snapshot must not be flagged stale: %+v", fresh)
	}

	// SAME held snapshot, later request: the verdict flips.
	fixedNow(t, generated.Add(10*time.Minute))
	stale := getDashboard(t, s)
	if stale.AgeSeconds != 600 {
		t.Errorf("age_seconds: got %d want 600", stale.AgeSeconds)
	}
	if !stale.Stale {
		t.Errorf("a 10-minute-old snapshot past a 120s bound must be flagged stale: %+v", stale)
	}
	// The as-of time itself is untouched by the stamp.
	if !stale.GeneratedAt.Equal(generated) {
		t.Errorf("generated_at must survive the freshness stamp: got %v want %v", stale.GeneratedAt, generated)
	}

	// And the held snapshot itself was never mutated by serving it.
	held, _ := s.Get()
	if held.Stale || held.AgeSeconds != 0 {
		t.Errorf("serving must not mutate the held snapshot: %+v", held)
	}
}
