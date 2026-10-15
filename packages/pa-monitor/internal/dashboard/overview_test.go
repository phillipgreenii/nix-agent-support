// Package dashboard guards the provisioned Grafana dashboard shipped under
// packages/pa-monitor/grafana. The dashboard is JSON consumed by Grafana, so
// these tests parse it and pin load-bearing panel queries.
package dashboard

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type panel struct {
	ID      int    `json:"id"`
	Title   string `json:"title"`
	Targets []struct {
		Expr string `json:"expr"`
	} `json:"targets"`
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
