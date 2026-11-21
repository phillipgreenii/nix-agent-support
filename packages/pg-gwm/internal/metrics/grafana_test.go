package metrics

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-gwm/internal/state"
)

var metricToken = regexp.MustCompile(`pg_gwm_[a-z_]+`)

// exportedNames are the series names Render emits, with the histogram suffixes.
func exportedNames() map[string]bool {
	free, floor, preserved, age := uint64(1), uint64(1), 1, 1.0
	out := Render(Snapshot{
		ReaperLastSuccessAge: &age, ConfigValid: true, Unmanaged: &preserved,
		Repos: []Repo{{
			Label: "r-abcdef", HaveState: true, FreeBytes: &free, FloorBytes: &floor, Preserved: &preserved,
			LastReapAge: &age, Worktrees: map[string]int{}, Counters: &state.RepoCounters{},
		}},
	})
	names := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		names[metricToken.FindString(line)] = true
	}
	return names
}

// Every pg_gwm_* token in an alert expression or a dashboard panel is a series
// the exporter serves; a rename in Render without the rule files fails here.
func TestGrafanaFilesOnlyReadExportedSeries(t *testing.T) {
	names := exportedNames()
	for _, f := range []string{
		"../../grafana/alerting/alerts.yaml", "../../grafana/alerting/alerts-reaper.yaml", "../../grafana/pg-gwm.json",
	} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		toks := metricToken.FindAllString(string(b), -1)
		if len(toks) == 0 && strings.HasSuffix(f, ".json") {
			t.Fatalf("%s: no metric tokens found", f)
		}
		for _, tok := range toks {
			// "pg_gwm_" alone is prose.
			if tok == Prefix {
				continue
			}
			if !names[tok] {
				t.Errorf("%s reads %s, which Render does not export", f, tok)
			}
		}
	}
}
