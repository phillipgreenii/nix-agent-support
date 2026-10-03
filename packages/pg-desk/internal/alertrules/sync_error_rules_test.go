// Package alertrules guards the Grafana alert rules in grafana/alerting/alerts.yaml.
//
// No PromQL engine is a dependency of this module, so the tests parse the
// rule file as YAML and pin each rule's expression, severity and hold time.
package alertrules

import (
	"os"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/metrics"
	"gopkg.in/yaml.v3"
)

type ruleFile struct {
	Groups []struct {
		Rules []rule `yaml:"rules"`
	} `yaml:"groups"`
}

type rule struct {
	UID    string            `yaml:"uid"`
	For    string            `yaml:"for"`
	Labels map[string]string `yaml:"labels"`
	NoData string            `yaml:"noDataState"`
	ExecEr string            `yaml:"execErrState"`
	Data   []struct {
		RefID string `yaml:"refId"`
		Model struct {
			Expr string `yaml:"expr"`
		} `yaml:"model"`
	} `yaml:"data"`
}

func loadRules(t *testing.T) map[string]rule {
	t.Helper()
	b, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var f ruleFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatalf("alerts.yaml does not parse: %v", err)
	}
	out := map[string]rule{}
	for _, g := range f.Groups {
		for _, r := range g.Rules {
			if _, dup := out[r.UID]; dup {
				t.Errorf("duplicate rule uid %q", r.UID)
			}
			out[r.UID] = r
		}
	}
	return out
}

func exprA(t *testing.T, r rule) string {
	t.Helper()
	for _, d := range r.Data {
		if d.RefID == "A" {
			return d.Model.Expr
		}
	}
	t.Fatalf("rule %s has no refId A query", r.UID)
	return ""
}

// pg2-qki4v: only exhausted (non-self-healing) sync_error rows page critical;
// the transient retrying rows a restart leaves behind are a warning.
func TestSyncErrorRowsSplitBySeverity(t *testing.T) {
	rules := loadRules(t)

	crit, ok := rules["pg-desk-sync-error-rows"]
	if !ok {
		t.Fatal("rule pg-desk-sync-error-rows missing")
	}
	if got := exprA(t, crit); got != metrics.MetricSyncErrorExhaustedRows {
		t.Errorf("critical rule expr = %q, want %q", got, metrics.MetricSyncErrorExhaustedRows)
	}
	if crit.Labels["severity"] != "critical" {
		t.Errorf("exhausted rule severity = %q, want critical", crit.Labels["severity"])
	}

	warn, ok := rules["pg-desk-sync-error-retrying"]
	if !ok {
		t.Fatal("rule pg-desk-sync-error-retrying missing")
	}
	if got := exprA(t, warn); got != metrics.MetricSyncErrorRetryingRows {
		t.Errorf("retrying rule expr = %q, want %q", got, metrics.MetricSyncErrorRetryingRows)
	}
	if warn.Labels["severity"] != "warning" {
		t.Errorf("retrying rule severity = %q, want warning", warn.Labels["severity"])
	}
	if warn.For != "30m" {
		t.Errorf("retrying rule for = %q, want 30m (outlive a typical self-heal)", warn.For)
	}

	for _, r := range []rule{crit, warn} {
		if r.NoData != "OK" || r.ExecEr != "Error" {
			t.Errorf("rule %s noDataState/execErrState = %q/%q, want OK/Error", r.UID, r.NoData, r.ExecEr)
		}
	}

	// The total (retrying + exhausted) must not page by itself again.
	for uid, r := range rules {
		if exprA(t, r) == metrics.MetricSyncErrorRows {
			t.Errorf("rule %s alerts on %s directly; use the retrying/exhausted split", uid, metrics.MetricSyncErrorRows)
		}
	}
}
