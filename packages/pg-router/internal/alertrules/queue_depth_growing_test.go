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
			var v float64
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

// ---------------------------------------------------------------------------
// pg2-o6z19 / pg2-n1pm4 / pg2-p93c0: rule pg-router-queue-stalled
//
// The growth rule above measures depth TREND; this one measures dispatch
// PROGRESS: a type held at depth >= stallFloor for a whole 30m window in which
// pg_router_throughput_total did not advance, sustained for `for: 30m`, then
// held by `keepFiringFor: 30m`. Fixtures are 30s scrapes; a sample is omitted
// where the gauge has no series (depth 0).
// ---------------------------------------------------------------------------

const (
	// wantStallExpr MUST equal the expr of rule pg-router-queue-stalled.
	wantStallExpr = `min_over_time(pg_router_queue_depth[30m]) >= 10 and count_over_time(pg_router_queue_depth[30m]) >= 54 unless on(type) (sum by (type) (increase(pg_router_throughput_total[30m])) > 0)`

	stallFloor = 10
	stallFor   = 30 * time.Minute
	stallKeep  = 30 * time.Minute
)

// stallInput is what Prometheus holds for one type: the depth gauge and the
// instants at which the throughput counter advanced.
type stallInput struct {
	depth   []sample
	accepts []time.Time
}

func (in stallInput) acceptedIn(from, to time.Time) bool {
	for _, a := range in.accepts {
		if a.After(from) && !a.After(to) {
			return true
		}
	}
	return false
}

// depthHeld models the left operand: >= minSamples present scrapes in the 30m
// window and min_over_time >= stallFloor.
func (in stallInput) depthHeld(t time.Time) bool {
	w := inRange(in.depth, t.Add(-window), t)
	if len(w) < minSamples {
		return false
	}
	for _, s := range w {
		if s.v < stallFloor {
			return false
		}
	}
	return true
}

// stalledNow models the whole expression at instant t. increase() reads
// the counter between the window's FIRST scrape and its last, so an advance
// is only visible if it lands after the first scrape (t-window+scrape, t].
func (in stallInput) stalledNow(t time.Time) bool {
	return in.depthHeld(t) && !in.acceptedIn(t.Add(-window+scrape), t)
}

type stallState struct{ cond, alerting, firing bool }

// stallReplay evaluates the rule every minute in [from, to] and applies
// `for: 30m` (Pending until the condition has held at every evaluation of the
// trailing 30m) and `keepFiringFor: 30m` (an alert that is firing stays firing
// until 30m after the condition was last true, and a condition that comes back
// inside that period continues it without a new Pending phase).
func stallReplay(in stallInput, from, to time.Time) map[time.Time]stallState {
	var times []time.Time
	for t := from; !t.After(to); t = t.Add(evalEvery) {
		times = append(times, t)
	}
	out := make(map[time.Time]stallState, len(times))
	forN, keepN := int(stallFor/evalEvery), int(stallKeep/evalEvery)
	run, lastTrue, firing := 0, 0, false
	for i, t := range times {
		c := in.stalledNow(t)
		run = map[bool]int{true: run + 1, false: 0}[c]
		alerting := false
		switch {
		case c && (firing || run > forN):
			firing, alerting, lastTrue = true, true, i
		case firing && i-lastTrue > keepN:
			firing = false
		}
		out[t] = stallState{c, alerting, firing}
	}
	return out
}

func anyState(m map[time.Time]stallState, from, to time.Time, pick func(stallState) bool) (time.Time, bool) {
	var first time.Time
	found := false
	for t, s := range m {
		if t.Before(from) || t.After(to) || !pick(s) {
			continue
		}
		if !found || t.Before(first) {
			first, found = t, true
		}
	}
	return first, found
}

func isCond(s stallState) bool     { return s.cond }
func isAlerting(s stallState) bool { return s.alerting }
func isFiring(s stallState) bool   { return s.firing }

// longestCondRun is the longest stretch of consecutive true evaluations, in
// minutes, within [from, to].
func longestCondRun(m map[time.Time]stallState, from, to time.Time) int {
	best, run := 0, 0
	for t := from; !t.After(to); t = t.Add(evalEvery) {
		if m[t].cond {
			run++
			best = max(best, run)
		} else {
			run = 0
		}
	}
	return best
}

func atDay(day, hms string) time.Time {
	t, err := time.Parse(time.RFC3339, day+"T"+hms+"Z")
	if err != nil {
		panic(err)
	}
	return t
}

// loadStallCSV reads a testdata series (columns documented in the file header).
// metricView selects the accept column the daemon's throughput counter would
// have shown; otherwise the accepts the queue log records.
func loadStallCSV(t *testing.T, path, day string, metricView bool) stallInput {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var in stallInput
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, ",")
		if len(f) != 4 {
			t.Fatalf("%s: bad row %q", path, line)
		}
		at := atDay(day, f[0])
		var depth, qacc, macc int
		for i, dst := range []*int{&depth, &qacc, &macc} {
			n := 0
			for _, c := range f[i+1] {
				n = n*10 + int(c-'0')
			}
			*dst = n
		}
		if depth > 0 {
			in.depth = append(in.depth, sample{at, float64(depth)})
		}
		n := qacc
		if metricView {
			n = macc
		}
		for i := 0; i < n; i++ {
			in.accepts = append(in.accepts, at)
		}
	}
	return in
}

// acceptsFromDrops derives dispatches from a synthetic depth series: every
// unit the depth falls by between scrapes is one accept (a series that goes
// absent has drained to zero). Arrivals landing in the same scrape as a
// dispatch hide it, so this under-counts accepts, which only makes the
// no-alert assertions below harder to pass.
func acceptsFromDrops(series []sample) []time.Time {
	var out []time.Time
	for i := 1; i < len(series); i++ {
		prev, cur := series[i-1], series[i]
		next := cur.v
		if cur.at.Sub(prev.at) > scrape { // gap: the series was absent, i.e. drained to 0
			for k := 0; k < int(prev.v); k++ {
				out = append(out, prev.at.Add(scrape))
			}
			continue
		}
		for k := 0; k < int(prev.v-next); k++ {
			out = append(out, cur.at)
		}
	}
	return out
}

func stallBlock(t *testing.T, uid string) string {
	t.Helper()
	b, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "- uid: "+uid)
	if i < 0 {
		t.Fatalf("rule %s not found", uid)
	}
	rest := src[i+len("- uid:"):]
	if j := strings.Index(rest, "- uid:"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func TestStallRuleExprMatchesModel(t *testing.T) {
	rest := stallBlock(t, "pg-router-queue-stalled")
	m := regexp.MustCompile(`(?m)^\s*expr: (.+)$`).FindStringSubmatch(rest)
	if m == nil {
		t.Fatal("no expr in rule")
	}
	if m[1] != wantStallExpr {
		t.Errorf("rule expr drifted from the modelled one:\n got %q\nwant %q", m[1], wantStallExpr)
	}
	// keepFiringFor is camelCase: file provisioning ignores keep_firing_for.
	for _, need := range []string{"for: 30m", "keepFiringFor: 30m", "noDataState: OK", "execErrState: Error", "severity: warning"} {
		if !strings.Contains(rest, need) {
			t.Errorf("rule lost %q", need)
		}
	}
	if strings.Contains(rest, "keep_firing_for") {
		t.Error("snake_case keep_firing_for is silently ignored by file provisioning; use keepFiringFor")
	}
}

// pg2-p93c0: the aggregate rule was deleted (see the decision comment in
// alerts.yaml and TestWholeSystemStallSurfacesViaHeartbeat), so nothing may
// provision its uid again by accident, and the growth rule keeps its identity.
func TestBacklogGrowingRuleIsGoneAndGrowthRuleKept(t *testing.T) {
	b, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "- uid: pg-router-backlog-growing") {
		t.Error("pg-router-backlog-growing was deleted by pg2-p93c0 and must not be re-provisioned")
	}
	growth := stallBlock(t, "pg-router-queue-depth-growing")
	for _, need := range []string{"title: pg-router queue depth is growing (by type)", "severity: warning", "for: 15m"} {
		if !strings.Contains(growth, need) {
			t.Errorf("growth rule fingerprint/identity lost %q", need)
		}
	}
}

// pg2-87bbd: removing a rule's entry from alerts.yaml does NOT remove it from
// Grafana -- file provisioning only adds/updates, and the deleted
// pg-router-backlog-growing stayed live (provenance=file) and evaluating after
// apply. Only an explicit `deleteRules:` entry for the uid removes it, so the
// file must carry one (orgId 1, matching the group's orgId) and it must sit in
// the top-level deleteRules list, not be mistaken for a provisioned rule.
func TestBacklogGrowingRuleHasExplicitDeleteEntry(t *testing.T) {
	b, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	re := regexp.MustCompile(`(?m)^deleteRules:\n(?:[ ]+#[^\n]*\n)*((?:[ ]+- orgId: \d+\n[ ]+uid: [^\n]+\n(?:[ ]+#[^\n]*\n)*)+)`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatal("alerts.yaml has no top-level deleteRules: list; Grafana will keep the removed rule live")
	}
	if !strings.Contains(m[1], "- orgId: 1\n    uid: pg-router-backlog-growing\n") {
		t.Errorf("deleteRules lacks `orgId: 1` + `uid: pg-router-backlog-growing`; got:\n%s", m[1])
	}
	// deleteRules must precede groups: a delete entry nested under a group
	// would be provisioned (or rejected) as something else.
	if di, gi := strings.Index(src, "\ndeleteRules:"), strings.Index(src, "\ngroups:"); di < 0 || gi < 0 || di > gi {
		t.Error("deleteRules must be a top-level key (before groups)")
	}
	// An active rule must never share a uid with a delete entry.
	if strings.Contains(src, "- uid: pg-router-backlog-growing") {
		t.Error("pg-router-backlog-growing is both deleted and provisioned")
	}
}

// pg2-n1pm4: replay of the recorded 2026-09-30 pr.changed series (12:00Z-22:00Z,
// testdata/pr_changed_2026-09-30.csv, metric view = what Prometheus held).
func TestPrChangedSeries20260930(t *testing.T) {
	const day = "2026-09-30"
	in := loadStallCSV(t, "testdata/pr_changed_2026-09-30.csv", day, true)
	m := stallReplay(in, atDay(day, "12:31:00"), atDay(day, "22:00:00"))

	// The absorbed 12:49Z and 13:49Z bursts (cleared in 30-40m, dispatching
	// throughout) neither page nor even evaluate true before the stall begins.
	if tt, bad := anyState(m, atDay(day, "12:31:00"), atDay(day, "14:30:00"), isCond); bad {
		t.Errorf("absorbed bursts evaluated true at %s", tt.Format("15:04"))
	}
	// The ~55-deep stall pages, inside the recorded 14:19Z-17:29Z window.
	first, ok := anyState(m, atDay(day, "12:31:00"), atDay(day, "22:00:00"), isAlerting)
	if !ok {
		t.Fatal("the 14:19Z-17:29Z stall never reached Alerting")
	}
	if first.Before(atDay(day, "14:19:00")) || first.After(atDay(day, "17:29:00")) {
		t.Errorf("first Alerting at %s, want inside 14:19-17:29", first.Format("15:04"))
	}
	if !m[atDay(day, "17:00:00")].firing {
		t.Error("alert not firing at 17:00, mid-stall")
	}
	// Once dispatches resume (17:20Z) the alert resolves after keepFiringFor and the
	// 20:43Z episode (depth ~16, nearly flat, a dispatch inside every 30m) stays quiet.
	if tt, bad := anyState(m, atDay(day, "18:00:00"), atDay(day, "22:00:00"), isFiring); bad {
		t.Errorf("still firing / re-fired at %s after the stall", tt.Format("15:04"))
	}
	// Non-vacuity: the 20:43Z episode is close (the floor holds and the counter is
	// silent for a while) but never for the 30 minutes the rule needs.
	run := longestCondRun(m, atDay(day, "20:00:00"), atDay(day, "22:00:00"))
	if run == 0 || run >= int(window/evalEvery) {
		t.Errorf("20:43Z episode longest true run = %dm, want 0 < run < 30", run)
	}
}

// MODEL test over RECORDED data (not live code): the 2026-09-30 queue log records
// pr.changed accepts continuing from 15:00Z, but the throughput counter recorded
// at the time missed them because events restored by a daemon restart never
// reached Emitter.OnEnqueue, so the 15:00Z-17:20Z part of the "stall" only existed
// in the metric view. That defect is FIXED in the emitter (bead pg2-0efop:
// eventqueue.RestoreObserver / Emitter.OnRestore seed restored events, so
// throughput counts them from the first post-restart accept). This test still pins
// the recorded-data fact that, with the accepts the queue actually recorded, only
// the 14:10Z-14:55Z silence (45m, shorter than 30m + for 30m) remains and nothing
// pages; the test above keeps the metric-view fixture as the pre-fix replay.
func TestPrChangedStallAlertDependsOnRestartBlindMetric(t *testing.T) {
	const day = "2026-09-30"
	queue := loadStallCSV(t, "testdata/pr_changed_2026-09-30.csv", day, false)
	m := stallReplay(queue, atDay(day, "12:31:00"), atDay(day, "22:00:00"))
	if tt, bad := anyState(m, atDay(day, "12:31:00"), atDay(day, "22:00:00"), isAlerting); bad {
		t.Errorf("queue-log view reached Alerting at %s; the recorded stall is no longer an artifact of the metric", tt.Format("15:04"))
	}
	// ... but it is a genuine silence while it lasts.
	if run := longestCondRun(m, atDay(day, "14:00:00"), atDay(day, "15:10:00")); run < 10 {
		t.Errorf("genuine 14:10Z-14:55Z silence evaluated true for only %dm", run)
	}
}

// pg2-ralaa/pg2-nw41e: the 2026-10-01 issue.changed surge (depth 0 -> 38) is
// absorbed by a working consumer; it never reaches Alerting, in either view.
func TestIssueChangedSurge20261001DoesNotAlert(t *testing.T) {
	const day = "2026-10-01"
	for _, metricView := range []bool{true, false} {
		in := loadStallCSV(t, "testdata/issue_changed_2026-10-01.csv", day, metricView)
		m := stallReplay(in, atDay(day, "20:31:00"), atDay(day, "23:30:00"))
		if tt, bad := anyState(m, atDay(day, "20:31:00"), atDay(day, "23:30:00"), isAlerting); bad {
			t.Errorf("metricView=%v: surge reached Alerting at %s", metricView, tt.Format("15:04"))
		}
	}
}

// pg2-o6z19: a standing flat backlog (pr.changed 43 -> 56 over 25m) that is not
// being drained alerts; the same backlog with a dispatch every 20m does not.
func TestStandingFlatBacklogAlertsOnlyWithoutDispatches(t *testing.T) {
	var depth []sample
	start := at("10:00:00")
	for i := 0; i < 360; i++ { // 3h
		v := 56.0
		if i < 50 { // 25m ramp 43 -> 56
			v = 43 + 13*float64(i)/50
		}
		depth = append(depth, sample{start.Add(time.Duration(i) * scrape), float64(int(v))})
	}
	end := start.Add(3 * time.Hour)
	stalled := stallInput{depth: depth, accepts: []time.Time{start.Add(2 * time.Minute)}}
	if _, ok := anyState(stallReplay(stalled, start.Add(30*time.Minute), end), start, end, isAlerting); !ok {
		t.Fatal("standing undrained backlog never reached Alerting")
	}
	progressing := stallInput{depth: depth}
	for a := start.Add(time.Minute); a.Before(end); a = a.Add(20 * time.Minute) {
		progressing.accepts = append(progressing.accepts, a)
	}
	if tt, bad := anyState(stallReplay(progressing, start.Add(30*time.Minute), end), start, end, isCond); bad {
		t.Errorf("a standing backlog with a dispatch every 20m evaluated true at %s", tt.Format("15:04"))
	}
}

// pg2-o6z19: a periodic type whose troughs rise by 15 each 30m cycle. It alerts
// once its consumer goes quiet for a whole window; while the consumer keeps
// dispatching it does NOT (documented known gap: a pure stall signal cannot see
// a backlog that grows under a working consumer; the growth rule's own
// 5-variant analysis in pg2-nw41e found the same blind spot).
func TestRisingTroughPeriodicType(t *testing.T) {
	build := func(consumerQuitsAfter time.Duration) stallInput {
		var depth []sample
		var accepts []time.Time
		start := at("00:00:00")
		trough := 15.0
		for c := 0; c < 12; c++ { // 6h of 30m cycles; troughs 15, 30, 45, ...
			peak := trough + 40
			for i := 0; i < 60; i++ {
				at := start.Add(time.Duration(c*60+i) * scrape)
				v := peak - (peak-(trough+15))*float64(i)/59 // drains to the NEXT trough
				if at.Sub(start) > consumerQuitsAfter {
					v = peak // consumer wedged: nothing drains
				} else if i%4 == 0 {
					accepts = append(accepts, at) // working consumer: a dispatch every 2m
				}
				depth = append(depth, sample{at, float64(int(v))})
			}
			trough += 15
		}
		return stallInput{depth, accepts}
	}
	start, end := at("00:00:00"), at("00:00:00").Add(6*time.Hour)
	working := build(1000 * time.Hour)
	if tt, bad := anyState(stallReplay(working, start.Add(time.Hour), end), start, end, isCond); bad {
		t.Errorf("rising troughs under a working consumer evaluated true at %s: the known gap closed, update the rule comment", tt.Format("15:04"))
	}
	wedged := build(2 * time.Hour)
	if _, ok := anyState(stallReplay(wedged, start.Add(time.Hour), end), start, end, isAlerting); !ok {
		t.Fatal("rising troughs with a consumer that went quiet never reached Alerting")
	}
}

// pg2-nw41e: the healthy pr.reconcile sawtooth never even evaluates true, and
// the floor+presence half of the expression alone WOULD (slow 42/54-deep cycles
// hold >= 10 for 30m), so the throughput clause is what keeps it quiet.
func TestHealthyReconcileSawtoothNeverStalls(t *testing.T) {
	series := sawtoothSeries(70, preSweep)
	in := stallInput{depth: series, accepts: acceptsFromDrops(series)}
	from, to := at("00:00:00").Add(time.Hour), series[len(series)-1].at
	floorOnly := false
	for tt := from; !tt.After(to); tt = tt.Add(evalEvery) {
		floorOnly = floorOnly || in.depthHeld(tt)
	}
	if !floorOnly {
		t.Fatal("fixture never holds the depth floor, so the test is vacuous")
	}
	if tt, bad := anyState(stallReplay(in, from, to), from, to, isCond); bad {
		t.Errorf("sawtooth evaluated true at %s", tt.Format("15:04"))
	}
	rec := knotsTo30s(pr0594oKnots)
	recIn := stallInput{depth: rec, accepts: acceptsFromDrops(rec)}
	rf, rt := at("21:10:00"), at("23:30:00")
	if tt, bad := anyState(stallReplay(recIn, rf, rt), rf, rt, isCond); bad {
		t.Errorf("recorded pg2-0594o episode evaluated true at %s", tt.Format("15:04"))
	}
}

// pg2-nw41e R5 / pg2-mr0sl: review.ready sits behind a max_sessions=1 pool, so
// a review session keeps the head event waiting. Depth recorded since the
// apply is 1-4 (below the floor); a deeper queue is fine while a review
// completes inside every 30m; a deep queue with no completion for over an hour
// (30m of silence, then `for: 30m`) is indistinguishable from a stall and pages
// by design.
func TestReviewReadyBackpressure(t *testing.T) {
	mk := func(level float64, every time.Duration) stallInput {
		var in stallInput
		start := at("08:00:00")
		for i := 0; i < 480; i++ { // 4h
			in.depth = append(in.depth, sample{start.Add(time.Duration(i) * scrape), level})
		}
		for a := start.Add(every / 2); a.Before(start.Add(4 * time.Hour)); a = a.Add(every) {
			in.accepts = append(in.accepts, a)
		}
		return in
	}
	from, to := at("08:31:00"), at("12:00:00")
	if tt, bad := anyState(stallReplay(mk(4, 90*time.Minute), from, to), from, to, isCond); bad {
		t.Errorf("review.ready at depth 4 evaluated true at %s", tt.Format("15:04"))
	}
	if tt, bad := anyState(stallReplay(mk(15, 25*time.Minute), from, to), from, to, isCond); bad {
		t.Errorf("review.ready at depth 15 with a completion every 25m evaluated true at %s", tt.Format("15:04"))
	}
	if _, ok := anyState(stallReplay(mk(15, 90*time.Minute), from, to), from, to, isAlerting); !ok {
		t.Error("review.ready at depth 15 with 90m between completions never reached Alerting")
	}
}

// pg2-xepg6: a type that blips above the floor for one scrape and drains has
// min_over_time >= 10 on its lone sample; the presence guard keeps it quiet.
func TestStallIgnoresSparseBlips(t *testing.T) {
	var in stallInput
	start := at("10:00:00")
	for i := 0; i < 360; i++ {
		if i%20 == 0 { // one scrape of depth 12 every 10m, drained in between
			in.depth = append(in.depth, sample{start.Add(time.Duration(i) * scrape), 12})
		}
	}
	from, to := start.Add(31*time.Minute), start.Add(3*time.Hour)
	for tt := from; !tt.After(to); tt = tt.Add(evalEvery) {
		if in.stalledNow(tt) {
			t.Fatalf("sparse blips evaluated true at %s", tt.Format("15:04"))
		}
	}
}

// pg2-p93c0: the aggregate backlog rule is deleted because a whole-system stall
// reaches the per-type rule through desk.heartbeat (about one event a minute)
// within the hour. The coverage given up is many types each below the floor.
func TestWholeSystemStallSurfacesViaHeartbeat(t *testing.T) {
	var hb stallInput
	start := at("06:00:00")
	for i := 0; i < 480; i++ { // 4h, nothing dispatched, one heartbeat enqueued a minute
		hb.depth = append(hb.depth, sample{start.Add(time.Duration(i) * scrape), float64(1 + i/2)})
	}
	m := stallReplay(hb, start.Add(31*time.Minute), start.Add(4*time.Hour))
	first, ok := anyState(m, start, start.Add(4*time.Hour), isAlerting)
	if !ok || first.After(start.Add(75*time.Minute)) {
		t.Fatalf("whole-system stall reached Alerting at %v (ok=%v), want within 75m", first, ok)
	}
	// Given-up coverage, pinned: five types stuck at 4-9 each (aggregate 35) stay quiet.
	for _, level := range []float64{4, 6, 9} {
		var quiet stallInput
		for i := 0; i < 480; i++ {
			quiet.depth = append(quiet.depth, sample{start.Add(time.Duration(i) * scrape), level})
		}
		if tt, bad := anyState(stallReplay(quiet, start.Add(31*time.Minute), start.Add(4*time.Hour)), start, start.Add(4*time.Hour), isCond); bad {
			t.Errorf("depth %.0f (below the floor) evaluated true at %s", level, tt.Format("15:04"))
		}
	}
}
