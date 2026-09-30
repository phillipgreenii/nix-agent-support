// Package alertrules guards the Grafana alert rules in grafana/alerting/alerts.yaml.
//
// No PromQL engine is a dependency of this module, so the rule's semantics are
// modelled here (windowed delta plus a series-presence count) and the test pins
// the YAML expression to the exact text the model implements.
package alertrules

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

const (
	// wantExpr MUST equal the expr of rule pg-router-queue-depth-growing.
	wantExpr = `delta(pg_router_queue_depth[30m]) > 0 and count_over_time(pg_router_queue_depth[30m]) >= 54`

	window      = 30 * time.Minute
	minSamples  = 54 // 90% of the 60 samples a 30s scrape yields over 30m
	forDuration = 15 * time.Minute
	evalEvery   = time.Minute
	scrape      = 30 * time.Second
)

type sample struct {
	at time.Time
	v  float64
}

// firingNow models the rule expression at instant t: the series must have
// >= minSamples samples in (t-window, t] and last-first > 0 (delta, without
// range extrapolation, which cannot change the sign).
func firingNow(series []sample, t time.Time) bool {
	var in []sample
	for _, s := range series {
		if s.at.After(t.Add(-window)) && !s.at.After(t) {
			in = append(in, s)
		}
	}
	if len(in) < minSamples {
		return false
	}
	return in[len(in)-1].v-in[0].v > 0
}

// alerting models `for: 15m`: the expression must hold at every evaluation in
// the trailing forDuration ending at t.
func alerting(series []sample, t time.Time) bool {
	for e := t.Add(-forDuration); !e.After(t); e = e.Add(evalEvery) {
		if !firingNow(series, e) {
			return false
		}
	}
	return true
}

func everAlerting(series []sample, from, to time.Time) bool {
	for t := from; !t.After(to); t = t.Add(evalEvery) {
		if alerting(series, t) {
			return true
		}
	}
	return false
}

func TestRuleExprMatchesModel(t *testing.T) {
	b, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "uid: pg-router-queue-depth-growing")
	if i < 0 {
		t.Fatal("rule pg-router-queue-depth-growing not found")
	}
	rest := src[i:]
	if j := strings.Index(rest, "- uid:"); j >= 0 {
		rest = rest[:j]
	}
	m := regexp.MustCompile(`(?m)^\s*expr: (.+)$`).FindStringSubmatch(rest)
	if m == nil {
		t.Fatal("no expr in rule")
	}
	if m[1] != wantExpr {
		t.Errorf("rule expr drifted from the modelled one:\n got %q\nwant %q", m[1], wantExpr)
	}
	for _, need := range []string{"for: 15m", "noDataState: OK", "execErrState: Error", "severity: warning"} {
		if !strings.Contains(rest, need) {
			t.Errorf("rule lost %q", need)
		}
	}
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, "2026-09-29T"+s+"Z")
	if err != nil {
		panic(err)
	}
	return t
}

// R1: the sparse issue.changed samples from pg2-xepg6 must never alert.
func TestSparseSelfClearingBlipsDoNotAlert(t *testing.T) {
	series := []sample{
		{at("21:07:32"), 1},
		{at("21:28:32"), 1},
		{at("21:39:02"), 2},
		{at("22:08:32"), 2},
		{at("22:35:32"), 2},
		{at("22:36:02"), 2},
		{at("22:36:33"), 1},
		{at("22:41:32"), 1},
	}
	if everAlerting(series, at("21:00:00"), at("23:00:00")) {
		t.Fatal("sparse blips reached Alerting")
	}
	// Sanity: the OLD rule (delta > 0 only) would have fired on this data.
	old := func(t time.Time) bool {
		var in []sample
		for _, s := range series {
			if s.at.After(t.Add(-window)) && !s.at.After(t) {
				in = append(in, s)
			}
		}
		return len(in) >= 2 && in[len(in)-1].v-in[0].v > 0
	}
	oldFired := false
	for tt := at("21:00:00"); !tt.After(at("23:00:00")); tt = tt.Add(evalEvery) {
		ok := true
		for e := tt.Add(-forDuration); !e.After(tt); e = e.Add(evalEvery) {
			ok = ok && old(e)
		}
		oldFired = oldFired || ok
	}
	if !oldFired {
		t.Fatal("test data does not reproduce the old false positive")
	}
}

// R2: a type that stays non-zero and keeps rising for > 15m must alert.
func TestSustainedGrowthAlerts(t *testing.T) {
	var series []sample
	start := at("10:00:00")
	for i := 0; i < 120; i++ { // 60 minutes of 30s scrapes, +1 every 2 minutes
		series = append(series, sample{start.Add(time.Duration(i) * scrape), float64(1 + i/4)})
	}
	if !everAlerting(series, start, start.Add(60*time.Minute)) {
		t.Fatal("sustained growth never reached Alerting")
	}
}

// A steadily non-empty but flat or falling type must not alert.
func TestFlatOrFallingDoesNotAlert(t *testing.T) {
	var flat, falling []sample
	start := at("10:00:00")
	for i := 0; i < 120; i++ {
		flat = append(flat, sample{start.Add(time.Duration(i) * scrape), 3})
		falling = append(falling, sample{start.Add(time.Duration(i) * scrape), float64(30 - i/4)})
	}
	for name, s := range map[string][]sample{"flat": flat, "falling": falling} {
		if everAlerting(s, start, start.Add(60*time.Minute)) {
			t.Errorf("%s series reached Alerting", name)
		}
	}
}
