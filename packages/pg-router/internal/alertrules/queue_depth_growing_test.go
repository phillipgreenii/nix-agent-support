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

// legacyRule models the c2e2fb21 expression (`delta(30m) > 0` plus the
// count_over_time >= 54 presence guard) that the pr.reconcile sawtooth paged
// under. It exists only so the sawtooth fixtures below can prove they
// reproduce that false positive; the live rule is firingNow/alerting.
func legacyRule(series []sample, t time.Time) bool {
	in := inRange(series, t.Add(-window), t)
	return len(in) >= minSamples && rise(in) > 0
}

func legacyAlerting(series []sample, t time.Time) bool {
	for e := t.Add(-forDuration); !e.After(t); e = e.Add(evalEvery) {
		if !legacyRule(series, e) {
			return false
		}
	}
	return true
}

// pg2-nw41e: a batch type refilled by a 30m sweep (type=pr.reconcile). Each
// sweep re-enqueues every open PR (~70) and the queue drops ids already
// queued, so depth jumps to ~70 and the single consumer drains it back down
// before the next sweep. preSweep is the depth in the last bucket before each
// of 17 consecutive sweeps measured on 2026-09-29 17:55Z-02:55Z (queue.jsonl
// reconstruction): healthy cycles end near 0-29, slow ones (42, 54) are the
// post-restart head-of-line plateau. 0 means the type drained completely and
// its series is ABSENT (the gauge observes only types present in the queue).
var preSweep = []float64{4, 42, 32, 8, 3, 0, 15, 54, 18, 11, 0, 0, 2, 29, 8, 17, 5}

// sawtoothSeries builds 30s samples: at every 30m boundary depth is the peak,
// then drains linearly to the cycle's preSweep depth (a cycle ending at 0
// finishes draining at 26m and stays absent for the last 4m).
func sawtoothSeries(peak float64, ends []float64) []sample {
	var series []sample
	start := at("00:00:00")
	for c, end := range ends {
		drain := 30.0
		if end == 0 {
			drain = 26
		}
		for i := 0; i < 60; i++ {
			m := float64(i) / 2
			v := peak
			if m >= drain {
				v = end
			} else {
				v = peak - (peak-end)*m/drain
			}
			if v = float64(int(v + 0.5)); v > 0 {
				series = append(series, sample{start.Add(time.Duration(c*60+i) * scrape), v})
			}
		}
	}
	return series
}

// pg2-nw41e R1: the periodic pr.reconcile sawtooth must not reach Alerting,
// and must not even evaluate true once (a true evaluation is a Pending
// transition in Grafana). Evaluations start 1h in so every guard sees its
// full lookback; a shorter lookback would under-state the rise and make this
// pass vacuously.
func TestReconcileSawtoothDoesNotAlertOrPend(t *testing.T) {
	series := sawtoothSeries(70, preSweep)
	from, to := at("00:00:00").Add(time.Hour), series[len(series)-1].at
	legacy := false
	for tt := from; !tt.After(to); tt = tt.Add(evalEvery) {
		if firingNow(series, tt) {
			t.Fatalf("sawtooth evaluated true (would go Pending) at %s", tt)
		}
		if alerting(series, tt) {
			t.Fatalf("sawtooth reached Alerting at %s", tt)
		}
		legacy = legacy || legacyAlerting(series, tt)
	}
	if !legacy {
		t.Fatal("fixture does not reproduce the c2e2fb21 false positive")
	}
}

// knot is one 4-minute Prometheus sample of pg_router_queue_depth{type="pr.reconcile"}.
type knot struct {
	hhmm string
	v    float64
}

// pr0594oKnots are the recorded 4-minute samples of 2026-09-29 (21:30-22:58
// are the pg2-0594o samples verbatim; the hour before and the half hour after
// are the same-day queue.jsonl reconstruction, which matches the recorded
// samples within 1 at every shared point). It covers a slow cycle
// (Pending 21:58), then a restart burst where depth sat flat at 54 while the
// consumer worked a pr.changed run (Alerting 22:13 -> Normal 22:28 under
// c2e2fb21). 0 means the series was absent.
var pr0594oKnots = []knot{
	{"20:10", 40},
	{"20:14", 29},
	{"20:18", 22},
	{"20:22", 13},
	{"20:26", 2},
	{"20:30", 63},
	{"20:34", 54},
	{"20:38", 44},
	{"20:42", 34},
	{"20:46", 24},
	{"20:50", 13},
	{"20:54", 3},
	{"20:58", 66},
	{"21:02", 55},
	{"21:06", 41},
	{"21:10", 30},
	{"21:14", 20},
	{"21:18", 9},
	{"21:22", 0},
	{"21:26", 0},
	{"21:30", 60},
	{"21:34", 50},
	{"21:38", 40},
	{"21:42", 32},
	{"21:46", 26},
	{"21:50", 21},
	{"21:54", 16},
	{"21:58", 66},
	{"22:02", 60},
	{"22:06", 54},
	{"22:10", 54},
	{"22:14", 54},
	{"22:18", 54},
	{"22:22", 54},
	{"22:26", 54},
	{"22:30", 66},
	{"22:34", 56},
	{"22:38", 50},
	{"22:42", 43},
	{"22:46", 33},
	{"22:50", 27},
	{"22:54", 20},
	{"22:58", 17},
	{"23:02", 67},
	{"23:06", 58},
	{"23:10", 52},
	{"23:14", 41},
	{"23:18", 30},
	{"23:22", 17},
	{"23:26", 6},
	{"23:30", 69},
}

// knotsTo30s expands the 4-minute knots to the 30s scrape grid. A rise between
// two knots is a sweep refill, which lands as a step (hold, then jump 30s
// before the next knot); a fall is a linear drain. Zero is dropped (absent).
func knotsTo30s(knots []knot) []sample {
	var out []sample
	for i := 0; i+1 < len(knots); i++ {
		a, b := knots[i], knots[i+1]
		ta, tb := at(a.hhmm+":00"), at(b.hhmm+":00")
		for t := ta; t.Before(tb); t = t.Add(scrape) {
			v := a.v
			switch {
			case b.v > a.v && tb.Sub(t) <= scrape:
				v = b.v
			case b.v < a.v:
				v = a.v + (b.v-a.v)*float64(t.Sub(ta))/float64(tb.Sub(ta))
			}
			if v = float64(int(v + 0.5)); v > 0 {
				out = append(out, sample{t, v})
			}
		}
	}
	return out
}

// pg2-nw41e R1: replay the recorded pg2-0594o episode. Evaluations start 1h
// after the first knot (full lookback). The fixture must reproduce the
// 22:13-22:28 Alerting window of the c2e2fb21 expression, and the live rule
// must not evaluate true anywhere.
func TestReconcileRecordedEpisodeDoesNotAlertOrPend(t *testing.T) {
	series := knotsTo30s(pr0594oKnots)
	from, to := at("21:10:00"), at("23:30:00")
	var legacyAt []time.Time
	for tt := from; !tt.After(to); tt = tt.Add(evalEvery) {
		if firingNow(series, tt) {
			t.Fatalf("recorded episode evaluated true (would go Pending) at %s", tt)
		}
		if legacyAlerting(series, tt) {
			legacyAt = append(legacyAt, tt)
		}
	}
	if len(legacyAt) == 0 || legacyAt[0].After(at("22:20:00")) || legacyAt[len(legacyAt)-1].Before(at("22:20:00")) {
		t.Fatalf("fixture does not reproduce the recorded c2e2fb21 Alerting window around 22:13-22:28: %v", legacyAt)
	}
}
