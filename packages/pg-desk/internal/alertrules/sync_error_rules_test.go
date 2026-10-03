// Package alertrules guards the Grafana alert rules in grafana/alerting/alerts.yaml.
//
// No PromQL engine is a dependency of this module, so the tests parse the
// rule file as YAML and pin each rule's expression, severity and hold time.
package alertrules

import (
	"os"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/metrics"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/sync"
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
			Expr       string `yaml:"expr"`
			Conditions []struct {
				Evaluator struct {
					Type   string    `yaml:"type"`
					Params []float64 `yaml:"params"`
				} `yaml:"evaluator"`
			} `yaml:"conditions"`
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

// threshold returns the single "gt" threshold of the rule's refId C.
func threshold(t *testing.T, r rule) float64 {
	t.Helper()
	for _, d := range r.Data {
		if d.RefID != "C" {
			continue
		}
		if len(d.Model.Conditions) != 1 || d.Model.Conditions[0].Evaluator.Type != "gt" ||
			len(d.Model.Conditions[0].Evaluator.Params) != 1 {
			t.Fatalf("rule %s refId C is not a single gt threshold", r.UID)
		}
		return d.Model.Conditions[0].Evaluator.Params[0]
	}
	t.Fatalf("rule %s has no refId C threshold", r.UID)
	return 0
}

// reconcileCadence mirrors the 30m "desk-reconcile" period in
// phillipg-nix-ziprecruiter's modules/zm/default.nix. Automatic retries run
// only when `pg-desk reconcile` does, and that schedule lives outside this
// repo, so it cannot be read from the Go package.
const reconcileCadence = 30 * time.Minute

// pg2-h7grf: pg_desk_oldest_sync_error_age_seconds has no per-state variant,
// so a healthily retrying row ages too. The age rule stays critical but its
// threshold MUST exceed the retry horizon, or it pages on self-healing rows.
// The horizon is derived from the retry defaults so this fails if they drift.
func TestUnreconciledAnchorAgeAboveRetryHorizon(t *testing.T) {
	rules := loadRules(t)
	r, ok := rules["pg-desk-unreconciled-anchor-age"]
	if !ok {
		t.Fatal("rule pg-desk-unreconciled-anchor-age missing (its uid must stay unchanged)")
	}
	if got := exprA(t, r); got != metrics.MetricOldestSyncErrorAge {
		t.Errorf("age rule expr = %q, want %q", got, metrics.MetricOldestSyncErrorAge)
	}
	if r.Labels["severity"] != "critical" {
		t.Errorf("age rule severity = %q, want critical", r.Labels["severity"])
	}
	if r.For != "5m" || r.NoData != "OK" || r.ExecEr != "Error" {
		t.Errorf("age rule for/noDataState/execErrState = %q/%q/%q, want 5m/OK/Error", r.For, r.NoData, r.ExecEr)
	}

	p := sync.RetryPolicyFor(nil) // the defaults
	// Backoff alone: the wait before each of the MaxRetries retries, i.e. the
	// earliest instant the final retry can run (and the row be exhausted).
	var backoffSum time.Duration
	for attempt := 1; attempt <= p.MaxRetries; attempt++ {
		backoffSum += p.Backoff(attempt)
	}
	if want := 181 * time.Minute; backoffSum != want {
		t.Errorf("default backoff sum = %s, want %s; the alert comment and docs quote it, update them", backoffSum, want)
	}
	// Tick-bound: each retry waits for the next reconcile tick, so once a
	// backoff fits inside one cadence interval every retry costs a whole one.
	tickBound := time.Duration(p.MaxRetries) * max(reconcileCadence, p.MaxBackoff)

	got := time.Duration(threshold(t, r)) * time.Second
	if got != 6*time.Hour {
		t.Errorf("age rule threshold = %s, want 6h0m0s", got)
	}
	if got <= backoffSum {
		t.Errorf("age rule threshold %s must exceed the retry backoff horizon %s", got, backoffSum)
	}
	if got <= tickBound {
		t.Errorf("age rule threshold %s must exceed the reconcile-tick-bound retry horizon %s", got, tickBound)
	}
}
