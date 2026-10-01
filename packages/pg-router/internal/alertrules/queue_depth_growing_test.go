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
	wantExpr = `delta(pg_router_queue_depth[1h]) >= 25 and delta(pg_router_queue_depth[30m]) >= 10 and delta(pg_router_queue_depth[15m]) >= 5 and delta(pg_router_queue_depth[15m] offset 15m) >= 5 and delta(pg_router_queue_depth[30m]) > 0.25 * avg_over_time(pg_router_queue_depth[30m]) and count_over_time(pg_router_queue_depth[30m]) >= 54`

	window      = 30 * time.Minute
	minSamples  = 54   // 90% of the 60 samples a 30s scrape yields over 30m
	minDelta    = 10   // absolute rise over the window
	minRatio    = 0.25 // rise as a fraction of the window average
	minDelta1h  = 25   // pg2-mpgfa: absolute rise over the trailing hour
	minHalf     = 5    // pg2-mpgfa: rise required in EACH 15m half of the 30m window
	forDuration = 15 * time.Minute
	evalEvery   = time.Minute
	scrape      = 30 * time.Second
)

type sample struct {
	at time.Time
	v  float64
}

// inRange returns the samples in (from, to].
func inRange(series []sample, from, to time.Time) []sample {
	var in []sample
	for _, s := range series {
		if s.at.After(from) && !s.at.After(to) {
			in = append(in, s)
		}
	}
	return in
}

// rise is last-first over the samples (Prometheus delta() without range
// extrapolation, which cannot change the sign); fewer than 2 samples is 0.
func rise(in []sample) float64 {
	if len(in) < 2 {
		return 0
	}
	return in[len(in)-1].v - in[0].v
}

// firingNow models the rule expression at instant t: the 30m window must hold
// >= minSamples samples, rise >= minDelta and > minRatio*avg; the trailing 1h
// must rise >= minDelta1h; and EACH 15m half of the 30m window must rise
// >= minHalf (pg2-mpgfa).
func firingNow(series []sample, t time.Time) bool {
	in := inRange(series, t.Add(-window), t)
	if len(in) < minSamples {
		return false
	}
	d := rise(in)
	var sum float64
	for _, s := range in {
		sum += s.v
	}
	h1 := rise(inRange(series, t.Add(-window), t.Add(-window/2)))
	h2 := rise(inRange(series, t.Add(-window/2), t))
	return rise(inRange(series, t.Add(-time.Hour), t)) >= minDelta1h &&
		d >= minDelta && d > minRatio*sum/float64(len(in)) &&
		h1 >= minHalf && h2 >= minHalf
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
	for i := 0; i < 180; i++ { // 90 minutes of 30s scrapes, +1 every 2 minutes
		series = append(series, sample{start.Add(time.Duration(i) * scrape), float64(1 + i/4)})
	}
	if !everAlerting(series, start, start.Add(90*time.Minute)) {
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

// pg2-7xvn8: a dense plateau in a 52-58 band with delta ~ +3 must not alert,
// even with a slow upward drift (+3 over 30m) and noise.
func TestDensePlateauNoiseDoesNotAlert(t *testing.T) {
	var series []sample
	start := at("10:00:00")
	for i := 0; i < 360; i++ { // 3 hours of 30s scrapes
		v := 55 + float64((i*7)%5) - 2 + float64(i%120)/40 // band ~53-60, drifts +3 per 60m cycle
		series = append(series, sample{start.Add(time.Duration(i) * scrape), v})
	}
	if everAlerting(series, start, start.Add(180*time.Minute)) {
		t.Fatal("dense plateau noise reached Alerting")
	}
}

// Sustained growth on top of a non-trivial baseline must still alert.
func TestSustainedGrowthFromBaselineAlerts(t *testing.T) {
	var series []sample
	start := at("10:00:00")
	for i := 0; i < 240; i++ { // +1 every minute from 20
		series = append(series, sample{start.Add(time.Duration(i) * scrape), float64(20 + i/2)})
	}
	if !everAlerting(series, start, start.Add(120*time.Minute)) {
		t.Fatal("growth from baseline never reached Alerting")
	}
}

// pg2-mpgfa: a periodic sawtooth whose per-tooth rise over 30m is ~12 (the
// pr.reconcile Pending episodes recorded at A=11.19 / 13.22) must not even
// reach a single true evaluation, because the trailing 1h never rises 25.
func TestSawtoothDoesNotAlertOrPend(t *testing.T) {
	var series []sample
	start := at("10:00:00")
	for i := 0; i < 480; i++ { // 4h; 40m teeth: 19 -> 31 over 30m, then drops back
		m := (i * 30 / 60) % 40 // minute within the tooth
		v := 19.0
		if m < 30 {
			v += float64(m) * 12 / 30
		} else {
			v += 12 - float64(m-30)*1.2
		}
		series = append(series, sample{start.Add(time.Duration(i) * scrape), v})
	}
	for tt := start; !tt.After(start.Add(4 * time.Hour)); tt = tt.Add(evalEvery) {
		if firingNow(series, tt) {
			t.Fatalf("sawtooth evaluated true at %s", tt)
		}
	}
}

// pg2-mpgfa: a small burst onto a quiet type (the pr.changed 1 -> 12 episode
// recorded at A=11.28) must not Pend either.
func TestSmallBurstDoesNotPend(t *testing.T) {
	var series []sample
	start := at("10:00:00")
	for i := 0; i < 240; i++ {
		v := 1.0
		if i > 120 { // step up by 11 and hold
			v = 12
		}
		series = append(series, sample{start.Add(time.Duration(i) * scrape), v})
	}
	for tt := start; !tt.After(start.Add(2 * time.Hour)); tt = tt.Add(evalEvery) {
		if firingNow(series, tt) {
			t.Fatalf("small burst evaluated true at %s", tt)
		}
	}
}
