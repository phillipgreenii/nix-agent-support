package alertrules

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Rule pg-router-source-persistent-failure (bead pg2-tv11a, DEC-OBS-4).
//
// No PromQL engine is a dependency of this module, so the rule is modelled
// here (the same model-plus-pinned-expression pattern as
// queue_depth_growing_test.go) and the test pins the YAML expression to the
// exact text the model implements. The model is replayed against per-tick
// outcomes extracted from the daemon's stderr log (testdata/source_ticks.txt,
// whose header documents the extraction and its inference rule), because
// Prometheus history is too short and the failure counter resets on restart.

const wantPersistentFailureExpr = `time() - pg_router_source_last_success_timestamp_seconds > on(source) clamp_min(3 * pg_router_source_expected_interval_seconds, 1800)`

func TestPersistentFailureRule(t *testing.T) {
	r := ruleBlock(t, "pg-router-source-persistent-failure")
	if got := ruleExpr(t, r); got != wantPersistentFailureExpr {
		t.Errorf("persistent-failure expr:\n got %q\nwant %q", got, wantPersistentFailureExpr)
	}
	for _, need := range []string{
		"for: 5m",
		// Series exist from daemon start, so absence means the daemon is down,
		// which pg-router-liveness-down covers (TestLivenessRuleCoversDeadDaemon).
		"noDataState: OK",
		"execErrState: Error",
		"severity: warning",
		"params: [0]",
	} {
		if !strings.Contains(r, need) {
			t.Errorf("persistent-failure rule lost %q", need)
		}
	}
}

// The annotations are static-safe: only the source label and the age value are
// interpolated (no error text, URLs or other free-form values), and they carry
// the two inspection commands.
func TestPersistentFailureAnnotations(t *testing.T) {
	r := ruleBlock(t, "pg-router-source-persistent-failure")
	i, j := strings.Index(r, "annotations:"), strings.Index(r, "data:")
	if i < 0 || j < i {
		t.Fatal("annotations block not found")
	}
	ann := r[i:j]
	for _, need := range []string{
		"{{ $labels.source }}",
		"{{ humanizeDuration $values.B.Value }}",
		"max(3 x its expected interval, 30m)",
		// raw YAML: the single-quoted description doubles its own quotes
		`pg-router status --json | jq ''.sources[]|select(.name=="{{ $labels.source }}")|.failure''`,
		`sum by (reason)(increase(pg_router_source_failures_total{source="{{ $labels.source }}"}[1h]))`,
	} {
		if !strings.Contains(ann, need) {
			t.Errorf("annotations lost %q", need)
		}
	}
	// Strip the two allowed interpolations; nothing templated may remain.
	rest := strings.NewReplacer("{{ $labels.source }}", "", "{{ humanizeDuration $values.B.Value }}", "").Replace(ann)
	if strings.Contains(rest, "{{") {
		t.Errorf("annotations interpolate something other than the source label and the age: %q", rest)
	}
}

// pg-router-source-failure-rate stays the early-warning ratio signal (its expr
// is pinned by wantSourceFailureExpr); the persistent rule must not have
// replaced or altered it, and neither is registered with pg-router-probe from
// this repo.
func TestSourceFailureRateStaysUnchangedAlongsidePersistentRule(t *testing.T) {
	if got := ruleExpr(t, ruleBlock(t, "pg-router-source-failure-rate")); got != wantSourceFailureExpr {
		t.Errorf("source-failure-rate expr changed:\n got %q\nwant %q", got, wantSourceFailureExpr)
	}
	if strings.Contains(ruleBlock(t, "pg-router-source-persistent-failure"), "escalation:") {
		t.Error("persistent-failure rule must not carry an escalation label; probe registration is a separate, ZR-side change")
	}
}

// --- model ------------------------------------------------------------------

const (
	persistFor       = 5 * time.Minute // `for: 5m`
	persistFloor     = 30 * time.Minute
	persistMultiple  = 3
	persistEvalEvery = time.Minute
)

// persistThreshold is clamp_min(3 * expected_interval, 1800s).
func persistThreshold(period time.Duration) time.Duration {
	return max(persistMultiple*period, persistFloor)
}

// Tick outcomes. tickSkip is a tick that did not run at all (used only by the
// negative controls that model the gauge WITHOUT pause handling).
const (
	tickSuccess byte = 'S'
	tickFailure byte = 'F'
	tickPaused  byte = 'P' // gate- or halt-skipped pass: not a failure
	tickSkip    byte = 'N'
)

type srcTick struct {
	at   time.Time
	kind byte
}

// srcRun is one process run of one pull source: the gauge starts at start
// (process start) and ticks are sorted by time.
type srcRun struct {
	scenario, source string
	period           time.Duration
	start, end       time.Time
	ticks            []srcTick
}

// lastSuccess is the gauge value at t: the latest success-or-pause tick at or
// before t, else process start. A failure never advances it.
func (r srcRun) lastSuccess(t time.Time) time.Time {
	ls := r.start
	for _, k := range r.ticks {
		if k.at.After(t) {
			break
		}
		if k.kind == tickSuccess || k.kind == tickPaused {
			ls = k.at
		}
	}
	return ls
}

// condition models the expression at instant t: age strictly above the threshold.
func (r srcRun) condition(t time.Time) bool {
	return t.Sub(r.lastSuccess(t)) > persistThreshold(r.period)
}

// alerting models `for: 5m` the way the queue-depth model does: the condition
// must hold at every evaluation in the trailing persistFor. Evaluations before
// the run's start do not exist (a restart resets the series), so an alert needs
// the whole window inside one run.
func (r srcRun) alerting(t time.Time) bool {
	for e := t.Add(-persistFor); !e.After(t); e = e.Add(persistEvalEvery) {
		if e.Before(r.start) || !r.condition(e) {
			return false
		}
	}
	return true
}

// firstAlerting returns the first minute in [from, to) at which the rule is
// Alerting.
func (r srcRun) firstAlerting(from, to time.Time) (time.Time, bool) {
	for t := from; t.Before(to); t = t.Add(persistEvalEvery) {
		if r.alerting(t) {
			return t, true
		}
	}
	return time.Time{}, false
}

func (r srcRun) everAlerting() bool {
	_, ok := r.firstAlerting(r.start, r.end)
	return ok
}

func (r srcRun) count(kind byte) int {
	n := 0
	for _, k := range r.ticks {
		if k.kind == kind {
			n++
		}
	}
	return n
}

// --- testdata loader --------------------------------------------------------

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad time %q: %v", s, err)
	}
	return v
}

// loadSourceTicks parses testdata/source_ticks.txt (format in its header).
func loadSourceTicks(t *testing.T) []srcRun {
	t.Helper()
	b, err := os.ReadFile("testdata/source_ticks.txt")
	if err != nil {
		t.Fatal(err)
	}
	var runs []srcRun
	var cur *srcRun
	for n, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		switch f[0] {
		case "segment":
			if len(f) != 7 {
				t.Fatalf("source_ticks.txt:%d: bad segment line %q", n+1, line)
			}
			p, err := strconv.Atoi(f[3])
			if err != nil {
				t.Fatalf("source_ticks.txt:%d: bad period %q", n+1, f[3])
			}
			runs = append(runs, srcRun{
				scenario: f[1], source: f[2], period: time.Duration(p) * time.Second,
				start: mustTime(t, f[4]), end: mustTime(t, f[5]),
			})
			cur = &runs[len(runs)-1]
		case "S":
			if cur == nil || len(f) != 3 {
				t.Fatalf("source_ticks.txt:%d: bad S line %q", n+1, line)
			}
			from, e1 := strconv.Atoi(f[1])
			to, e2 := strconv.Atoi(f[2])
			if e1 != nil || e2 != nil {
				t.Fatalf("source_ticks.txt:%d: bad S offsets %q", n+1, line)
			}
			for o := from; o <= to; o += int(cur.period / time.Second) {
				cur.ticks = append(cur.ticks, srcTick{cur.start.Add(time.Duration(o) * time.Second), tickSuccess})
			}
		case "F":
			if cur == nil {
				t.Fatalf("source_ticks.txt:%d: F before any segment", n+1)
			}
			for _, o := range f[1:] {
				v, err := strconv.Atoi(o)
				if err != nil {
					t.Fatalf("source_ticks.txt:%d: bad F offset %q", n+1, o)
				}
				cur.ticks = append(cur.ticks, srcTick{cur.start.Add(time.Duration(v) * time.Second), tickFailure})
			}
		default:
			t.Fatalf("source_ticks.txt:%d: unknown directive %q", n+1, line)
		}
	}
	for i := range runs {
		tk := runs[i].ticks
		sort.SliceStable(tk, func(a, b int) bool { return tk[a].at.Before(tk[b].at) })
	}
	return runs
}

func runsFor(runs []srcRun, scenario, source string) []srcRun {
	var out []srcRun
	for _, r := range runs {
		if r.scenario == scenario && r.source == source {
			out = append(out, r)
		}
	}
	return out
}

func utc(t *testing.T, s string) time.Time { return mustTime(t, s) }

// --- replay of the recorded log --------------------------------------------

// 2026-10-05: the pr-team (and pr-mine) transient-upstream storms that put
// pg-router-source-failure-rate through 19 Alerting transitions must NOT fire
// the persistent rule, and neither may thread-me's isolated single-tick
// failures (e.g. the 14:05:56Z = 10:05 EDT one). The fixture must be
// non-vacuous: it has to hold the storms, and the failure-rate rule's model has
// to fire on them.
func TestReplay_2026_10_05_TransientStormsAndIsolatedFailuresDoNotFire(t *testing.T) {
	runs := loadSourceTicks(t)
	for _, c := range []struct {
		source      string
		minFailures int
	}{{"pr-team", 100}, {"pr-mine", 1}, {"thread-me", 3}} {
		rs := runsFor(runs, "2026-10-05", c.source)
		if len(rs) != 7 {
			t.Fatalf("%s: %d segments, want the 7 process runs of the day", c.source, len(rs))
		}
		failures, legacyEpisodes := 0, 0
		for _, r := range rs {
			failures += r.count(tickFailure)
			if r.everAlerting() {
				at, _ := r.firstAlerting(r.start, r.end)
				t.Errorf("%s run %s..%s reached Alerting at %s", c.source, r.start.Format(time.RFC3339), r.end.Format(time.RFC3339), at.Format(time.RFC3339))
			}
			legacyEpisodes += legacyEpisodeCount(r)
		}
		if failures < c.minFailures {
			t.Errorf("%s: fixture holds %d failed ticks, want >= %d (vacuous otherwise)", c.source, failures, c.minFailures)
		}
		if c.source == "pr-team" && legacyEpisodes < 5 {
			t.Errorf("pr-team: the failure-rate rule model fires %d times on this day, want >= 5 -- the fixture does not reproduce the noise this rule exists to avoid", legacyEpisodes)
		}
	}

	// The isolated thread-me failure at 14:05:56Z is in the data.
	iso := utc(t, "2026-10-05T14:05:56Z")
	found := false
	for _, r := range runsFor(runs, "2026-10-05", "thread-me") {
		for _, k := range r.ticks {
			found = found || (k.kind == tickFailure && k.at.Equal(iso))
		}
	}
	if !found {
		t.Error("thread-me's isolated 2026-10-05T14:05:56Z failure is missing from the fixture")
	}
}

// legacyEpisodeCount models pg-router-source-failure-rate (rate(...[10m]) > 0,
// for: 10m) on a run: the condition is "a failure in the trailing 10m" and an
// episode is one maximal stretch of Alerting evaluations.
func legacyEpisodeCount(r srcRun) int {
	cond := func(t time.Time) bool {
		for _, k := range r.ticks {
			if k.kind == tickFailure && k.at.After(t.Add(-10*time.Minute)) && !k.at.After(t) {
				return true
			}
		}
		return false
	}
	episodes, in := 0, false
	for t := r.start; t.Before(r.end); t = t.Add(persistEvalEvery) {
		alerting := true
		for e := t.Add(-10 * time.Minute); !e.After(t); e = e.Add(persistEvalEvery) {
			alerting = alerting && !e.Before(r.start) && cond(e)
		}
		if alerting && !in {
			episodes++
		}
		in = alerting
	}
	return episodes
}

// thread-me, 2026-10-01 12:42-15:34 EDT (16:42-19:34Z): six consecutive failed
// 30m ticks, ~4h with no success, in the process run that began 16:05:41Z. Its
// threshold is 3 x 30m = 90m, so the alert cannot precede 17:35:41Z + 5m.
// The runs around it must not fire, with ONE documented boundary case: the run
// from 22:08:31Z holds two consecutive failed ticks (22:43:01Z, 23:17:35Z). Even
// if the next tick is as early as the period allows (23:47:35Z, a period after
// the 23:17:35Z failure), the last success (22:08:31Z) is then 99m old, over the
// 90m threshold for the 9 minutes before it, which exceeds `for: 5m`. So two
// consecutive failed thread-me ticks with the ~35m tick spacing the log shows
// DO reach Alerting briefly -- an accepted property of the approved
// 3 x period threshold, pinned here so it is a visible decision, not a surprise.
func TestReplay_2026_10_01_ThreadMeSixConsecutiveFailedTicksFires(t *testing.T) {
	runs := runsFor(loadSourceTicks(t), "2026-10-01", "thread-me")
	if len(runs) != 4 {
		t.Fatalf("%d segments, want the 4 process runs", len(runs))
	}
	outageStart := utc(t, "2026-10-01T16:05:41Z")
	for _, r := range runs {
		if r.start.Equal(utc(t, "2026-10-01T22:08:31Z")) {
			firing := 0
			for e := r.start; e.Before(r.end); e = e.Add(persistEvalEvery) {
				if r.alerting(e) {
					firing++
				}
			}
			if firing == 0 || firing > 10 {
				t.Errorf("two consecutive failed ticks: Alerting for %d minutes, want a brief (1-10 minute) episode", firing)
			}
			continue
		}
		if !r.start.Equal(outageStart) {
			if r.everAlerting() {
				t.Errorf("run from %s must not fire (isolated failures only)", r.start.Format(time.RFC3339))
			}
			continue
		}
		if n := r.count(tickFailure); n != 6 {
			t.Fatalf("outage run holds %d failed ticks, want the 6 consecutive ones", n)
		}
		if n := r.count(tickSuccess); n > 2 {
			t.Fatalf("outage run holds %d successes; the failed run must be unbroken", n)
		}
		at, ok := r.firstAlerting(r.start, r.end)
		if !ok {
			t.Fatal("six consecutive failed 30m ticks never reached Alerting")
		}
		earliest := outageStart.Add(persistThreshold(30*time.Minute) + persistFor)
		if at.Before(earliest) {
			t.Errorf("fired at %s, before the physical earliest %s (threshold 90m + for 5m)", at.Format(time.RFC3339), earliest.Format(time.RFC3339))
		}
		if !at.Before(utc(t, "2026-10-01T19:34:19Z")) {
			t.Errorf("fired at %s, want it to fire well before the sixth failed tick at 19:34:19Z", at.Format(time.RFC3339))
		}
	}
}

// 2026-09-28 bd outage: worker/review/feedback (10s sources) failing
// continuously 14:08:50Z-20:39:48Z. Threshold max(30s, 30m) = 30m.
func TestReplay_2026_09_28_QueueSourceOutageFires(t *testing.T) {
	runs := loadSourceTicks(t)
	outageStart, outageEnd := utc(t, "2026-09-28T14:08:50Z"), utc(t, "2026-09-28T20:39:48Z")
	for _, src := range []string{"worker-source", "review-source", "feedback-source"} {
		rs := runsFor(runs, "2026-09-28", src)
		if len(rs) != 1 {
			t.Fatalf("%s: %d segments, want 1", src, len(rs))
		}
		r := rs[0]
		if r.count(tickFailure) < 1000 {
			t.Fatalf("%s: fixture holds %d failed ticks, want the outage's >= 1000", src, r.count(tickFailure))
		}
		at, ok := r.firstAlerting(r.start, r.end)
		if !ok {
			t.Fatalf("%s: the outage never reached Alerting", src)
		}
		if earliest := outageStart.Add(persistThreshold(10*time.Second) + persistFor); at.Before(earliest) {
			t.Errorf("%s fired at %s, before the physical earliest %s", src, at.Format(time.RFC3339), earliest.Format(time.RFC3339))
		}
		if !at.Before(outageStart.Add(time.Hour)) {
			t.Errorf("%s fired at %s, want within an hour of the outage starting", src, at.Format(time.RFC3339))
		}
		// Still firing late in the outage, and clear once ticks succeed again.
		if !r.alerting(outageEnd.Add(-time.Hour)) {
			t.Errorf("%s not Alerting an hour before the outage ended", src)
		}
		if r.alerting(outageEnd.Add(10 * time.Minute)) {
			t.Errorf("%s still Alerting 10m after the outage ended", src)
		}
	}
}

// --- deliberate pauses ------------------------------------------------------

// healthyRun builds a 4h run of successes every period, then replaces
// [from, to) with ticks of the given kind.
func healthyRun(period time.Duration, from, to time.Duration, during byte) srcRun {
	start := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	r := srcRun{scenario: "synthetic", source: "src", period: period, start: start, end: start.Add(4 * time.Hour)}
	for o := time.Duration(0); o < 4*time.Hour; o += period {
		kind := tickSuccess
		if o >= from && o < to {
			kind = during
		}
		r.ticks = append(r.ticks, srcTick{start.Add(o), kind})
	}
	return r
}

// The periods in the deployment run from 10s to 35m (17 pull sources).
var pausePeriods = []time.Duration{10 * time.Second, time.Minute, 5 * time.Minute, 30 * time.Minute, 35 * time.Minute}

// A 60m gate (an active gate blocking the source, e.g. a pg-router pause) or a
// 60m emitters-halted window (log-size limit) skips every due pass: each is
// reported as not-a-failure and advances the gauge, so no source may alert.
func TestPausedWindowsDoNotFire(t *testing.T) {
	for _, name := range []string{"60m gate", "60m emitters-halted"} {
		for _, p := range pausePeriods {
			r := healthyRun(p, time.Hour, 2*time.Hour, tickPaused)
			if r.count(tickPaused) == 0 {
				t.Fatalf("%s/%v: no paused ticks in the scenario", name, p)
			}
			if r.everAlerting() {
				t.Errorf("%s with period %v reached Alerting", name, p)
			}
		}
	}
}

// Negative controls: the same 60m, modelled WITHOUT pause handling (the gauge
// frozen, as a naive implementation would leave it) or as real failures, DOES
// fire for the short-period sources, so the pause tests above are not vacuous.
func TestPausedWindowControls(t *testing.T) {
	for _, kind := range []byte{tickSkip, tickFailure} {
		for _, p := range []time.Duration{10 * time.Second, time.Minute, 5 * time.Minute} {
			if !healthyRun(p, time.Hour, 2*time.Hour, kind).everAlerting() {
				t.Errorf("control %q with period %v never reached Alerting; the pause test cannot distinguish handling from none", string(kind), p)
			}
		}
	}
	// A 30m-period source has a 90m threshold: a frozen 60m window alone does
	// not reach it, which is exactly why the 10s-5m periods are the controls.
	if healthyRun(30*time.Minute, time.Hour, 2*time.Hour, tickSkip).everAlerting() {
		t.Error("a frozen 60m window unexpectedly fires a 30m-period source (threshold 90m)")
	}
}

// The threshold floor: a 1m source needs a 30m silence (not 3m), and the
// strict > means exactly the threshold does not yet fire.
func TestThresholdIsMaxOfThreePeriodsAndThirtyMinutes(t *testing.T) {
	cases := map[time.Duration]time.Duration{
		10 * time.Second: 30 * time.Minute,
		time.Minute:      30 * time.Minute,
		10 * time.Minute: 30 * time.Minute,
		30 * time.Minute: 90 * time.Minute,
		35 * time.Minute: 105 * time.Minute,
	}
	for p, want := range cases {
		if got := persistThreshold(p); got != want {
			t.Errorf("threshold(%v) = %v, want %v", p, got, want)
		}
	}
	r := srcRun{period: time.Minute, start: time.Time{}}
	if r.condition(r.start.Add(30 * time.Minute)) {
		t.Error("age == threshold must not fire (strict >)")
	}
	if !r.condition(r.start.Add(30*time.Minute + time.Second)) {
		t.Error("age just over the threshold must satisfy the condition")
	}
}

// A restart resets the clock: a source that keeps failing across a restart
// re-alerts only after threshold + for following it.
func TestRestartResetsTheClock(t *testing.T) {
	start := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	mk := func(from time.Time, d time.Duration) srcRun {
		r := srcRun{period: time.Minute, start: from, end: from.Add(d)}
		for o := time.Duration(0); o < d; o += time.Minute {
			r.ticks = append(r.ticks, srcTick{from.Add(o), tickFailure})
		}
		return r
	}
	first := mk(start, time.Hour)
	if !first.everAlerting() {
		t.Fatal("a continuously failing source never alerted")
	}
	second := mk(start.Add(time.Hour), 2*time.Hour)
	at, ok := second.firstAlerting(second.start, second.end)
	if !ok {
		t.Fatal("the still-failing source never re-alerted after the restart")
	}
	if earliest := second.start.Add(persistThreshold(time.Minute) + persistFor); at.Before(earliest) {
		t.Errorf("re-alerted at %s, before threshold + for after the restart (%s)", at, earliest)
	}
}
