// Package dashboard guards the provisioned Grafana dashboard shipped under
// packages/pa-monitor/grafana. The dashboard is JSON consumed by Grafana, so
// these tests parse it and pin load-bearing panel queries.
package dashboard

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const overviewPath = "../../grafana/pa-monitor-overview.json"

// livenessGate is the dashboard-side zero for pushed counters: a zero only
// while the daemon is exporting (pa_monitor_sessions_count is always emitted),
// so a dead producer never reads as a healthy 0.
const livenessGate = "and on() (max(pa_monitor_sessions_count) >= 0)"

func readOverview(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(overviewPath)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parseOverview(t *testing.T, b []byte) []panel {
	t.Helper()
	var dash struct {
		Panels []panel `json:"panels"`
	}
	if err := json.Unmarshal(b, &dash); err != nil {
		t.Fatalf("overview JSON does not parse: %v", err)
	}
	return dash.Panels
}

// mutate returns a copy of raw with old replaced by repl, failing the test if
// old is absent so a negative control cannot silently become a no-op.
func mutate(t *testing.T, raw []byte, old, repl string) []byte {
	t.Helper()
	s := string(raw)
	if !strings.Contains(s, old) {
		t.Fatalf("negative control target not found in dashboard JSON: %q", old)
	}
	return []byte(strings.Replace(s, old, repl, 1))
}

func flatten(ps []panel) []panel {
	var out []panel
	for _, p := range ps {
		out = append(out, p)
		out = append(out, flatten(p.Panels)...)
	}
	return out
}

var fixedWindow = regexp.MustCompile(`(?:rate|increase)\([^()]*\[\d+[a-z]+\]`)

// fixedWindowProblems lists exprs with a literal range window inside rate( or
// increase( on a panel whose description does not carry "window: fixed".
func fixedWindowProblems(ps []panel) []string {
	var out []string
	for _, p := range flatten(ps) {
		if strings.Contains(p.Description, "window: fixed") {
			continue
		}
		for _, tg := range p.Targets {
			if fixedWindow.MatchString(tg.Expr) {
				out = append(out, p.Title+": "+tg.Expr)
			}
		}
	}
	return out
}

// activeSessionsProblems: every value query of the Active sessions table (all
// but the label-carrying info query A) must collapse by session_id first, or a
// duplicated series renders the same session twice.
func activeSessionsProblems(ps []panel) []string {
	found := findPanels(ps, "Active sessions")
	if len(found) == 0 {
		return []string{`no panel titled "Active sessions"`}
	}
	var out []string
	for _, p := range found {
		if len(p.Targets) < 3 {
			out = append(out, "Active sessions has fewer than 3 targets")
		}
		for _, tg := range p.Targets {
			if tg.RefID == "A" {
				continue
			}
			if !strings.HasPrefix(tg.Expr, "max by (session_id)") {
				out = append(out, "Active sessions target "+tg.RefID+": "+tg.Expr)
			}
		}
	}
	return out
}

func funnelProblems(ps []panel) []string {
	found := findPanels(ps, "Nudge funnel")
	if len(found) == 0 {
		return []string{`no panel titled "Nudge funnel"`}
	}
	var out []string
	for _, p := range found {
		if p.Type != "bargauge" {
			out = append(out, "Nudge funnel type is "+p.Type+", want bargauge")
		}
		for _, tg := range p.Targets {
			if !tg.Instant {
				out = append(out, "Nudge funnel target "+tg.RefID+" is not instant")
			}
		}
	}
	return out
}

func sessionMixProblems(ps []panel) []string {
	var out []string
	for _, p := range findPanels(ps, "Session state mix") {
		out = append(out, "panel still present: "+p.Title)
	}
	return out
}

// bareVectorZero matches the masking form `or vector(0)` (not the gated
// `or (vector(0) and on() ...)`).
var bareVectorZero = regexp.MustCompile(`or\s+vector\(0\)`)

// livenessProblems: a pushed counter stat must read a zero only while the
// daemon is exporting (livenessGate) or show a non-numeric noValue; no panel
// may use a bare `or vector(0)`; the separate liveness stats must exist.
func livenessProblems(ps []panel) []string {
	var out []string
	for _, p := range flatten(ps) {
		for _, tg := range p.Targets {
			if bareVectorZero.MatchString(tg.Expr) {
				out = append(out, p.Title+": bare or vector(0): "+tg.Expr)
			}
		}
	}
	for _, prefix := range []string{
		"Stuck (blocked", "Nudges suppressed", "Caffeinate grace expirations",
		"Context-limit hits", "Auto-resume deferred", "Sessions errored",
		"Nudge send failures", "Authentication",
	} {
		found := findPanels(ps, prefix)
		if len(found) == 0 {
			out = append(out, "missing panel "+prefix)
		}
		for _, p := range found {
			nv := p.FieldConfig.Defaults.NoValue
			textNoValue := nv != "" && func() bool { _, err := strconv.ParseFloat(nv, 64); return err != nil }()
			for _, tg := range p.Targets {
				if !strings.Contains(tg.Expr, livenessGate) && !textNoValue {
					out = append(out, p.Title+": neither liveness-gated nor text noValue: "+tg.Expr)
				}
			}
		}
	}
	for _, title := range []string{"Authentication", "Daemon connection"} {
		if len(findPanels(ps, title)) == 0 {
			out = append(out, "liveness panel missing: "+title)
		}
	}
	return out
}

// layoutProblems: every top-level panel fits the 24-column grid and no two
// panels overlap.
func layoutProblems(ps []panel) []string {
	var out []string
	for i, a := range ps {
		ga := a.GridPos
		if ga.X < 0 || ga.W <= 0 || ga.X+ga.W > 24 {
			out = append(out, a.Title+": outside the 24-column grid")
		}
		for _, b := range ps[i+1:] {
			gb := b.GridPos
			if ga.X < gb.X+gb.W && gb.X < ga.X+ga.W && ga.Y < gb.Y+gb.H && gb.Y < ga.Y+ga.H {
				out = append(out, a.Title+" overlaps "+b.Title)
			}
		}
	}
	return out
}

func wantNoProblems(t *testing.T, name string, got []string) {
	t.Helper()
	for _, g := range got {
		t.Errorf("%s: %s", name, g)
	}
}

func wantProblems(t *testing.T, name string, got []string) {
	t.Helper()
	if len(got) == 0 {
		t.Errorf("negative control %s: expected a problem, found none", name)
	}
}

func TestOverviewNoUnmarkedFixedWindows(t *testing.T) {
	raw := readOverview(t)
	wantNoProblems(t, "dashboard", fixedWindowProblems(parseOverview(t, raw)))

	bad := mutate(t, raw, "pa_monitor_poll_tick_duration_bucket[$__rate_interval]", "pa_monitor_poll_tick_duration_bucket[5m]")
	wantProblems(t, "literal [5m]", fixedWindowProblems(parseOverview(t, bad)))

	marked := mutate(t, bad, `"description": "p95 duration of the EMIT tick only`, `"description": "window: fixed - test. p95 duration of the EMIT tick only`)
	wantNoProblems(t, "window: fixed marker exempts", fixedWindowProblems(parseOverview(t, marked)))
}

func TestOverviewActiveSessionsCollapseBySessionID(t *testing.T) {
	raw := readOverview(t)
	wantNoProblems(t, "dashboard", activeSessionsProblems(parseOverview(t, raw)))

	bad := mutate(t, raw, "max by (session_id) (pa_monitor_session_tokens)", "pa_monitor_session_tokens")
	wantProblems(t, "bare tokens query", activeSessionsProblems(parseOverview(t, bad)))
}

func TestOverviewNudgeFunnelIsInstantBargauge(t *testing.T) {
	raw := readOverview(t)
	wantNoProblems(t, "dashboard", funnelProblems(parseOverview(t, raw)))

	bad := mutate(t, raw, `"type": "bargauge"`, `"type": "timeseries"`)
	wantProblems(t, "timeseries funnel", funnelProblems(parseOverview(t, bad)))

	bad = mutate(t, raw, `"legendFormat": "queued",
          "instant": true,`, `"legendFormat": "queued",`)
	wantProblems(t, "non-instant target", funnelProblems(parseOverview(t, bad)))
}

func TestOverviewSessionStateMixRemoved(t *testing.T) {
	raw := readOverview(t)
	wantNoProblems(t, "dashboard", sessionMixProblems(parseOverview(t, raw)))

	bad := mutate(t, raw, `"title": "Sessions by state"`, `"title": "Session state mix (now)"`)
	wantProblems(t, "reintroduced pie", sessionMixProblems(parseOverview(t, bad)))
}

func TestOverviewCounterStatsAreLivenessGated(t *testing.T) {
	raw := readOverview(t)
	wantNoProblems(t, "dashboard", livenessProblems(parseOverview(t, raw)))

	gated := "(sum(increase(pa_monitor_nudge_suppressed_total[$__range]))) or (vector(0) and on() (max(pa_monitor_sessions_count) >= 0))"
	bad := mutate(t, raw, gated, "sum(increase(pa_monitor_nudge_suppressed_total[$__range]))")
	wantProblems(t, "ungated counter stat", livenessProblems(parseOverview(t, bad)))

	bad = mutate(t, raw, gated, "sum(increase(pa_monitor_nudge_suppressed_total[$__range])) or vector(0)")
	wantProblems(t, "bare or vector(0)", livenessProblems(parseOverview(t, bad)))

	bad = mutate(t, raw, `"title": "Daemon connection"`, `"title": "Daemon link"`)
	wantProblems(t, "missing Daemon connection", livenessProblems(parseOverview(t, bad)))
}

func TestOverviewLayoutHasNoOverlapAndFits24Columns(t *testing.T) {
	raw := readOverview(t)
	ps := parseOverview(t, raw)
	wantNoProblems(t, "dashboard", layoutProblems(ps))

	// Negative control: put Authentication where Sessions (current) sits.
	bad := parseOverview(t, raw)
	for i := range bad {
		if bad[i].Title == "Authentication" {
			bad[i].GridPos.X, bad[i].GridPos.Y, bad[i].GridPos.W, bad[i].GridPos.H = 0, 4, 12, 3
		}
	}
	wantProblems(t, "overlap", layoutProblems(bad))
}

type panel struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Type        string `json:"type"`
	Description string `json:"description"`
	GridPos     struct {
		X int `json:"x"`
		Y int `json:"y"`
		W int `json:"w"`
		H int `json:"h"`
	} `json:"gridPos"`
	Targets []struct {
		Expr    string `json:"expr"`
		Instant bool   `json:"instant"`
		RefID   string `json:"refId"`
	} `json:"targets"`
	FieldConfig struct {
		Defaults struct {
			NoValue string `json:"noValue"`
		} `json:"defaults"`
	} `json:"fieldConfig"`
	Panels []panel `json:"panels"`
}

// findPanels returns every panel (including those nested in collapsed rows)
// whose title starts with prefix. Panels are located by title, never by id.
func findPanels(ps []panel, prefix string) []panel {
	var out []panel
	for _, p := range ps {
		if strings.HasPrefix(p.Title, prefix) {
			out = append(out, p)
		}
		out = append(out, findPanels(p.Panels, prefix)...)
	}
	return out
}

// pg2-ph81c (operator ruling on pg2-3m9ci option 1, 2026-10-14): git_branch is
// counted under pa_monitor_subprocess_spawns_total but is a filesystem read of
// .git/HEAD, not a spawn, so the "Subprocess spawns" panel must exclude it.
// The emitted metric and its kind values are deliberately unchanged.
func TestSubprocessSpawnsPanelExcludesGitBranch(t *testing.T) {
	b, err := os.ReadFile("../../grafana/pa-monitor-overview.json")
	if err != nil {
		t.Fatal(err)
	}
	var dash struct {
		Panels []panel `json:"panels"`
	}
	if err := json.Unmarshal(b, &dash); err != nil {
		t.Fatal(err)
	}

	found := findPanels(dash.Panels, "Subprocess spawns")
	if len(found) == 0 {
		t.Fatal(`no panel titled "Subprocess spawns..." found`)
	}
	for _, p := range found {
		if len(p.Targets) == 0 {
			t.Errorf("panel %q has no targets", p.Title)
		}
		for i, tg := range p.Targets {
			if !strings.Contains(tg.Expr, "pa_monitor_subprocess_spawns_total") {
				t.Errorf("panel %q target %d no longer queries pa_monitor_subprocess_spawns_total: %s", p.Title, i, tg.Expr)
			}
			if !strings.Contains(tg.Expr, `kind!="git_branch"`) {
				t.Errorf("panel %q target %d must exclude kind=git_branch (pg2-ph81c): %s", p.Title, i, tg.Expr)
			}
		}
	}
}
