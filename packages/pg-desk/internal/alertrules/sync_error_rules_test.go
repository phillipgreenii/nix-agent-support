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
		Interval string `yaml:"interval"`
		Rules    []rule `yaml:"rules"`
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

// sample is one evaluation's reduced input: ok false is NoData (no series).
type sample struct {
	v  float64
	ok bool
}

// alertState is the Grafana state of a rule after an evaluation.
type alertState string

const (
	stateNormal   alertState = "Normal"
	statePending  alertState = "Pending"
	stateAlerting alertState = "Alerting"
)

// evaluate models Grafana unified alerting for one of these rules: each
// interval it reduces its query to the last sample, applies `gt threshold`
// (NoData is Normal under noDataState: OK), stays Pending from the first
// breach until the breach has lasted `for`, then Alerts; the first
// non-breaching evaluation resolves it to Normal. It returns the state after
// each evaluation.
func evaluate(forDur, interval time.Duration, thr float64, samples []sample) []alertState {
	var states []alertState
	var breachSince time.Duration
	breaching := false
	for i, s := range samples {
		at := time.Duration(i) * interval
		if !s.ok || s.v <= thr {
			breaching = false
			states = append(states, stateNormal)
			continue
		}
		if !breaching {
			breaching, breachSince = true, at
		}
		if at-breachSince >= forDur {
			states = append(states, stateAlerting)
		} else {
			states = append(states, statePending)
		}
	}
	return states
}

func repeatSample(s sample, n int) []sample {
	out := make([]sample, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// TestSyncErrorRulesFireAfterForAndResolve is the modelled end of bead
// pg2-pyewq's proof that a recorded sync_error pages and clears: for each
// sync_error-driven rule, taken from alerts.yaml (its `for`, its gt threshold
// and the group's evaluation interval), a value just above the threshold
// becomes Pending, is still Pending one interval short of `for`, Alerts at
// `for`, and resolves on the first evaluation back at 0 or with no data. A
// value AT the threshold never alerts. It proves the MODELED rule only (no
// PromQL or Grafana engine is a dependency of this module); the real firing
// stays a post-apply smoke check (pg2-zlc7h).
//
// Delete with the sync_error alerts (pg2-6mo45.3.34).
func TestSyncErrorRulesFireAfterForAndResolve(t *testing.T) {
	b, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var f ruleFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	interval := time.Duration(0)
	for _, g := range f.Groups {
		for _, r := range g.Rules {
			if r.UID == "pg-desk-sync-error-rows" {
				interval, err = time.ParseDuration(g.Interval)
				if err != nil {
					t.Fatalf("group interval %q: %v", g.Interval, err)
				}
			}
		}
	}
	if interval != time.Minute {
		t.Fatalf("evaluation interval = %s, want 1m", interval)
	}

	rules := loadRules(t)
	for _, want := range []struct {
		uid       string
		forDur    time.Duration
		threshold float64
	}{
		{"pg-desk-sync-error-rows", 5 * time.Minute, 0},
		{"pg-desk-sync-error-retrying", 30 * time.Minute, 0},
		{"pg-desk-unreconciled-anchor-age", 5 * time.Minute, 6 * 3600},
	} {
		uid := want.uid
		t.Run(uid, func(t *testing.T) {
			r, ok := rules[uid]
			if !ok {
				t.Fatalf("rule %s missing", uid)
			}
			forDur, err := time.ParseDuration(r.For)
			if err != nil {
				t.Fatalf("for %q: %v", r.For, err)
			}
			thr := threshold(t, r)
			if forDur != want.forDur || thr != want.threshold {
				t.Fatalf("rule %s for/threshold = %s/%v, want %s/%v: the simulation below is only meaningful for the pinned values", uid, forDur, thr, want.forDur, want.threshold)
			}
			above := sample{v: thr + 1, ok: true}
			clear := sample{v: 0, ok: true}
			holdEvals := int(forDur / interval) // evaluations strictly before `for` has elapsed

			// Breach, hold, then recover.
			samples := append(append(repeatSample(above, holdEvals+3), repeatSample(clear, 2)...), repeatSample(sample{}, 1)...)
			got := evaluate(forDur, interval, thr, samples)
			if got[0] != statePending {
				t.Errorf("first breaching evaluation = %s, want Pending", got[0])
			}
			if got[holdEvals-1] != statePending {
				t.Errorf("one interval short of for (%s) = %s, want Pending", r.For, got[holdEvals-1])
			}
			if got[holdEvals] != stateAlerting {
				t.Errorf("evaluation at for (%s) = %s, want Alerting", r.For, got[holdEvals])
			}
			if got[holdEvals+2] != stateAlerting {
				t.Errorf("still breaching after for = %s, want Alerting", got[holdEvals+2])
			}
			if got[holdEvals+3] != stateNormal {
				t.Errorf("first evaluation back at 0 = %s, want Normal (resolved)", got[holdEvals+3])
			}

			// NoData is OK, not an alert and not a held breach.
			if rules[uid].NoData != "OK" {
				t.Fatalf("noDataState = %q, want OK", rules[uid].NoData)
			}
			gotNoData := evaluate(forDur, interval, thr, append(append(repeatSample(above, holdEvals+1), sample{}), above))
			if gotNoData[holdEvals] != stateAlerting || gotNoData[holdEvals+1] != stateNormal || gotNoData[holdEvals+2] != statePending {
				t.Errorf("no-data handling = %v, want Alerting then Normal (resolved) then a fresh Pending", gotNoData[holdEvals:])
			}

			// A value AT the threshold is not a breach (gt, not gte).
			for i, st := range evaluate(forDur, interval, thr, repeatSample(sample{v: thr, ok: true}, holdEvals+2)) {
				if st != stateNormal {
					t.Errorf("evaluation %d at the threshold = %s, want Normal", i, st)
				}
			}
		})
	}
}
