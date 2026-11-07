package metrics

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var metricToken = regexp.MustCompile(`pg_checkout_pool_[a-z_]+`)

// exportedNames are the series names Render emits, with the histogram suffixes.
func exportedNames(t *testing.T) map[string]bool {
	t.Helper()
	free, d := uint64(1), 1
	out := Render(Gauges{FreeBytes: &free, Daemons: &d}, Journal{})
	names := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		names[metricToken.FindString(line)] = true
	}
	return names
}

// Every pg_checkout_pool_* token in an alert expression or a dashboard panel is a series the
// `metrics` command exports; a rename in Render without the rule files fails here.
func TestGrafanaFilesOnlyReadExportedSeries(t *testing.T) {
	names := exportedNames(t)
	for _, f := range []string{"../../grafana/alerting/alerts.yaml", "../../grafana/pg-checkout-pool.json"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		toks := metricToken.FindAllString(string(b), -1)
		if len(toks) == 0 {
			t.Fatalf("%s: no metric tokens found", f)
		}
		for _, tok := range toks {
			// "pg_checkout_pool_" alone and the package-name token "pg_checkout_pool" are prose.
			if tok == Prefix || tok == "pg_checkout_pool_alert_rules" {
				continue
			}
			if !names[tok] {
				t.Errorf("%s reads %s, which Render does not export", f, tok)
			}
		}
	}
}
