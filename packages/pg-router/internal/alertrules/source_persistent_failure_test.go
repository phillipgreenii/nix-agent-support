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
//
// Bead pg2-ez292 changed what the rule measures. The expression used to be
// `time() - ..._last_success_timestamp_seconds`: WALL-clock time, so a laptop
// that slept for longer than a source's threshold made every slow source look
// silent and the rule fired for all of them with nothing failing. It now reads
// the router's own last-success age, which is measured on the router's monotonic
// clock and does not advance while the host sleeps. The model therefore computes
// the age as AWAKE time (srcRun.age); the retired wall-clock reading survives
// only as srcRun.wallCondition, the negative control that shows each sleep
// scenario below is one the retired expression really did fire on.

const wantPersistentFailureExpr = `pg_router_source_last_success_age_seconds > on(source) clamp_min(3 * pg_router_source_expected_interval_seconds, 1800)`

// retiredPersistentFailureExpr is the wall-clock expression this rule used before
// bead pg2-ez292. It is kept only so a failure message can name what must not come back.
const retiredPersistentFailureExpr = `time() - pg_router_source_last_success_timestamp_seconds > on(source) clamp_min(3 * pg_router_source_expected_interval_seconds, 1800)`

func TestPersistentFailureRule(t *testing.T) {
	r := ruleBlock(t, "pg-router-source-persistent-failure")
	got := ruleExpr(t, r)
	if got != wantPersistentFailureExpr {
		t.Errorf("persistent-failure expr:\n got %q\nwant %q", got, wantPersistentFailureExpr)
	}
	// The regression guard for pg2-ez292, stated as the property rather than the
	// text: the staleness must come from the router-measured age, never from
	// subtracting a timestamp from the evaluator's wall clock, which a host sleep
	// inflates.
	if strings.Contains(got, "time()") || strings.Contains(got, "last_success_timestamp") {
		t.Errorf("persistent-failure expr %q measures wall-clock time; a host sleep would fire it for every slow source (retired form: %q)", got, retiredPersistentFailureExpr)
	}
	if !strings.Contains(got, "pg_router_source_last_success_age_seconds") {
		t.Errorf("persistent-failure expr %q does not read the router-measured last-success age", got)
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
		// the age is awake time, so the text must say so: a reader seeing "has not
		// succeeded for 1h 35m" must not think a laptop's sleep is in that figure
		"of awake time",
		"host sleep is not counted",
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

// The comment block above the rule states what its staleness measures (bead
// pg2-ez292): awake time on the router's own clock, with a host sleep excluded,
// and that the retired wall-clock form must not come back.
func TestPersistentFailureHeaderStatesAwakeTime(t *testing.T) {
	b, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	var header []string
	for i, l := range lines {
		if strings.TrimSpace(l) != "- uid: pg-router-source-persistent-failure" {
			continue
		}
		for j := i - 1; j >= 0 && strings.HasPrefix(strings.TrimSpace(lines[j]), "#"); j-- {
			header = append([]string{lines[j]}, header...)
		}
		break
	}
	if len(header) == 0 {
		t.Fatal("no comment block above pg-router-source-persistent-failure")
	}
	text := strings.Join(header, "\n")
	for _, need := range []string{
		"awake time",
		"monotonic clock",
		"host sleep",
		"pg_router_source_last_success_age_seconds",
	} {
		if !strings.Contains(text, need) {
			t.Errorf("rule header comment lost %q:\n%s", need, text)
		}
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

// sleepSpan is a window [from, to) in which the host was asleep: the wall clock
// ran on, but the router's monotonic clock, Prometheus and Grafana did not.
type sleepSpan struct{ from, to time.Time }

// srcRun is one process run of one pull source: the gauge starts at start
// (process start) and ticks are sorted by time. asleep lists the windows the host
// slept through (testdata `Z` lines); a run without any is awake throughout, so its
// awake time IS its wall-clock time, which is how every run recorded before bead
// pg2-ez292 reads.
type srcRun struct {
	scenario, source string
	period           time.Duration
	start, end       time.Time
	ticks            []srcTick
	asleep           []sleepSpan
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

// asleepAt reports whether the host is asleep at t.
func (r srcRun) asleepAt(t time.Time) bool {
	for _, w := range r.asleep {
		if !t.Before(w.from) && t.Before(w.to) {
			return true
		}
	}
	return false
}

// awakeBetween is the time from a to b that the host was awake: the span minus
// the sleep inside it. This is what the router's monotonic clock measures.
func (r srcRun) awakeBetween(a, b time.Time) time.Duration {
	if !b.After(a) {
		return 0
	}
	d := b.Sub(a)
	for _, w := range r.asleep {
		lo, hi := w.from, w.to
		if lo.Before(a) {
			lo = a
		}
		if hi.After(b) {
			hi = b
		}
		if hi.After(lo) {
			d -= hi.Sub(lo)
		}
	}
	return d
}

// age models pg_router_source_last_success_age_seconds at t: the awake time since
// the gauge's last success-or-pause (or process start). A host sleep adds nothing.
func (r srcRun) age(t time.Time) time.Duration {
	return r.awakeBetween(r.lastSuccess(t), t)
}

// condition models the expression at instant t: the router-measured age strictly
// above the threshold.
func (r srcRun) condition(t time.Time) bool {
	return r.age(t) > persistThreshold(r.period)
}

// wallCondition models the RETIRED expression, time() - last_success_timestamp: the
// wall-clock age, which a host sleep inflates. It exists only as the negative control
// proving a sleep scenario is one the retired rule fired on.
func (r srcRun) wallCondition(t time.Time) bool {
	return t.Sub(r.lastSuccess(t)) > persistThreshold(r.period)
}

// deployedCondition returns the model of the expression alerts.yaml ACTUALLY carries
// for this rule, so a replay exercises the rule as deployed instead of an
// idealisation of it. An expression with no model is a failure: it needs one before
// anything can be said about how it behaves across a sleep.
func deployedCondition(t *testing.T, r srcRun) func(time.Time) bool {
	t.Helper()
	switch got := ruleExpr(t, ruleBlock(t, "pg-router-source-persistent-failure")); got {
	case wantPersistentFailureExpr:
		return r.condition
	case retiredPersistentFailureExpr:
		return r.wallCondition
	default:
		t.Fatalf("persistent-failure expr %q has no model in this test", got)
		return nil
	}
}

// alertingWith models `for: 5m` the way the queue-depth model does: the condition
// must hold at every evaluation in the trailing persistFor. Evaluations happen once a
// minute from the run's start, and only while the host is awake (Prometheus and
// Grafana sleep with the router); an evaluation before the run's start does not exist
// (a restart resets the series), so an alert needs the whole window inside one run.
// The state at a sleeping instant is the state at the last evaluation before it, and a
// sleep inside the window is not an evaluation: the pending clock runs on across it, as
// Grafana's does. A run with no sleep evaluates every minute, which is the original
// model unchanged.
func (r srcRun) alertingWith(cond func(time.Time) bool, t time.Time) bool {
	last := t
	for !last.Before(r.start) && r.asleepAt(last) {
		last = last.Add(-persistEvalEvery)
	}
	for e := last; !e.Before(r.start); e = e.Add(-persistEvalEvery) {
		if r.asleepAt(e) {
			continue
		}
		if !cond(e) {
			return false
		}
		if !e.After(last.Add(-persistFor)) {
			return true
		}
	}
	return false
}

func (r srcRun) alerting(t time.Time) bool { return r.alertingWith(r.condition, t) }

// firstAlertingWith returns the first evaluation minute in [from, to) at which the rule
// is Alerting under cond.
func (r srcRun) firstAlertingWith(cond func(time.Time) bool, from, to time.Time) (time.Time, bool) {
	for t := from; t.Before(to); t = t.Add(persistEvalEvery) {
		if !r.asleepAt(t) && r.alertingWith(cond, t) {
			return t, true
		}
	}
	return time.Time{}, false
}

// firstAlerting returns the first minute in [from, to) at which the rule is
// Alerting.
func (r srcRun) firstAlerting(from, to time.Time) (time.Time, bool) {
	return r.firstAlertingWith(r.condition, from, to)
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
		case "Z":
			if cur == nil || len(f) != 3 {
				t.Fatalf("source_ticks.txt:%d: bad Z line %q", n+1, line)
			}
			from, e1 := strconv.Atoi(f[1])
			to, e2 := strconv.Atoi(f[2])
			if e1 != nil || e2 != nil || to <= from {
				t.Fatalf("source_ticks.txt:%d: bad Z offsets %q", n+1, line)
			}
			cur.asleep = append(cur.asleep, sleepSpan{
				from: cur.start.Add(time.Duration(from) * time.Second),
				to:   cur.start.Add(time.Duration(to) * time.Second),
			})
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

// --- host sleep (bead pg2-ez292) --------------------------------------------

// 2026-10-12: the laptop slept through the morning and most of the afternoon, then woke;
// the rule fired for six slow pull sources within 16 s of each other although every one
// of them recovered on its own, and each firing filed an escalated bead. Two of the six
// have their tick times and the host's awake accounting recorded in testdata/
// source_ticks.txt (the other four are not: only their bead times are known).
//
// The router paces a source on its monotonic clock, which stops while the host sleeps,
// so after the wake a slow source is next due only when the REST of its period has passed
// in awake time. In the recorded data the awake time between the two ticks is within 2%
// of the period for both sources (1,767 s of a 1,800 s period; 3,540 s of 3,600 s), while
// the wall-clock gap is 28,505 s and 24,014 s against thresholds of 5,400 s and 10,800 s.
// The retired wall-clock expression therefore read both sources as silent for hours; the
// age the router now exports is the awake time and stays far below the threshold.
func TestReplay_2026_10_12_HostSleepDoesNotFire(t *testing.T) {
	runs := loadSourceTicks(t)
	for _, c := range []struct {
		source       string
		period       time.Duration
		first, again string
		wallGap      time.Duration
		awakeGap     time.Duration
	}{
		// 138 s of dark wakes + 829 s (16:32:15-16:46:04) + 800 s (16:47:46-17:01:06).
		{"pg-router-probe-tick", 30 * time.Minute, "2026-10-12T09:06:01Z", "2026-10-12T17:01:06Z", 28505 * time.Second, 1767 * time.Second},
		// 116 s of dark wakes + 829 s + 2,595 s (16:47:46-17:31:01).
		{"work-report-pull", time.Hour, "2026-10-12T10:50:47Z", "2026-10-12T17:31:01Z", 24014 * time.Second, 3540 * time.Second},
	} {
		rs := runsFor(runs, "2026-10-12", c.source)
		if len(rs) != 1 {
			t.Fatalf("%s: %d segments, want 1", c.source, len(rs))
		}
		r := rs[0]
		a, b := utc(t, c.first), utc(t, c.again)
		threshold := persistThreshold(c.period)

		// The fixture says what the bead recorded.
		if got := b.Sub(a); got != c.wallGap {
			t.Fatalf("%s: wall-clock gap between the ticks = %v, want %v", c.source, got, c.wallGap)
		}
		if got := r.awakeBetween(a, b); got != c.awakeGap {
			t.Fatalf("%s: awake time between the ticks = %v, want %v", c.source, got, c.awakeGap)
		}
		if c.wallGap <= threshold {
			t.Fatalf("%s: the gap %v does not exceed the threshold %v, so the scenario proves nothing", c.source, c.wallGap, threshold)
		}
		if c.awakeGap > c.period || c.awakeGap < c.period*95/100 {
			t.Errorf("%s: awake gap %v is not within 5%% below the %v period: the router ticks once a period of awake time has passed", c.source, c.awakeGap, c.period)
		}
		if len(r.asleep) < 2 {
			t.Fatalf("%s: %d sleep windows, want the long sleep and the 16:46-16:47 clamshell sleep", c.source, len(r.asleep))
		}

		// The router-measured age never gets near the threshold, so the rule never
		// fires: before the second tick it peaks at the awake gap, and it stays under
		// the threshold for the rest of the segment.
		var peakBefore, peak time.Duration
		for e := r.start; e.Before(r.end); e = e.Add(persistEvalEvery) {
			peak = max(peak, r.age(e))
			if e.Before(b) {
				peakBefore = max(peakBefore, r.age(e))
			}
		}
		if peakBefore > c.awakeGap || peak >= threshold {
			t.Errorf("%s: peak age %v before the second tick (want at most the awake gap %v) and %v overall (want under the %v threshold)", c.source, peakBefore, c.awakeGap, peak, threshold)
		}
		if at, ok := r.firstAlertingWith(deployedCondition(t, r), r.start, r.end); ok {
			t.Errorf("%s: the rule in alerts.yaml reached Alerting at %s although the source ticked on time in awake terms", c.source, at.Format(time.RFC3339))
		}

		// Control: the retired wall-clock expression fires on exactly this data, one
		// `for` after the first evaluation following the wake (the observed alert
		// started at 16:36:00Z).
		wake := r.asleep[0].to
		at, ok := r.firstAlertingWith(r.wallCondition, r.start, r.end)
		if !ok {
			t.Fatalf("%s: control: the retired wall-clock expression never fires, so the fixture does not reproduce the incident", c.source)
		}
		if at.Before(wake.Add(persistFor)) || at.After(wake.Add(persistFor+2*persistEvalEvery)) {
			t.Errorf("%s: control: retired expression first fires at %s, want %v to %v after the wake at %s",
				c.source, at.Format(time.RFC3339), persistFor, persistFor+2*persistEvalEvery, wake.Format(time.RFC3339))
		}
	}
}

// sleepRun is a healthy source that ticks every period of AWAKE time and whose host
// sleeps for sleepLen starting sleepAt (an awake offset) into a 4h awake run. A tick that
// falls due after the sleep begins appears after the wake, shifted by the sleep: the
// router's cadence is monotonic.
func sleepRun(period, sleepAt, sleepLen time.Duration, during byte) srcRun {
	start := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	r := srcRun{scenario: "synthetic", source: "src", period: period, start: start, end: start.Add(4*time.Hour + sleepLen)}
	r.asleep = []sleepSpan{{start.Add(sleepAt), start.Add(sleepAt + sleepLen)}}
	for o := time.Duration(0); o < 4*time.Hour; o += period {
		at := start.Add(o)
		if o >= sleepAt {
			at = at.Add(sleepLen)
		}
		r.ticks = append(r.ticks, srcTick{at, during})
	}
	return r
}

// The regression for bead pg2-ez292, stated generally: a host sleep that advances the
// wall clock by at least one threshold, while the monotonic clock does not, MUST NOT fire
// the rule for a healthy source of any period. The retired wall-clock expression does for
// the slow ones (its `for: 5m` is satisfied whenever a source's next tick is at least 5
// minutes of awake time after the wake), which is exactly the population that paged; the
// controls below pin that.
func TestHostSleepDoesNotFireForAnyPeriod(t *testing.T) {
	for _, c := range []struct {
		period       time.Duration
		retiredFires bool
	}{
		{10 * time.Second, false},
		{time.Minute, false},
		{5 * time.Minute, false},
		{30 * time.Minute, true},
		{35 * time.Minute, true},
	} {
		sleepLen := persistThreshold(c.period) + 2*time.Hour // wall clock advances by more than one threshold
		r := sleepRun(c.period, 2*c.period+time.Minute, sleepLen, tickSuccess)
		if got := r.awakeBetween(r.start, r.end); got != 4*time.Hour {
			t.Fatalf("period %v: awake time = %v, want exactly the 4h run", c.period, got)
		}
		if at, ok := r.firstAlertingWith(deployedCondition(t, r), r.start, r.end); ok {
			t.Errorf("period %v: the rule in alerts.yaml reached Alerting at %s across a %v sleep although every tick was on time in awake terms", c.period, at.Format(time.RFC3339), sleepLen)
		}
		_, retired := r.firstAlertingWith(r.wallCondition, r.start, r.end)
		if retired != c.retiredFires {
			t.Errorf("period %v: control: retired wall-clock expression fires = %v, want %v", c.period, retired, c.retiredFires)
		}
	}
}

// The age is AWAKE time, not "never fires across a sleep": a source that really goes
// silent still alerts, once it has been silent for the threshold plus `for` of awake
// time, however long the host slept in between. Here the last success is the tick at the
// run's start, the source fails from then on, and the host sleeps 8h after 10 awake
// minutes. The first evaluation with an age over 90m is the one at 91m awake, so the rule
// fires 5m later, at 96m awake: the sleep delays the alert by exactly its own length.
// The retired expression fires 5m after the wake instead, with only 10 awake minutes of
// silence behind it.
func TestHostSleepDelaysButDoesNotHideARealOutage(t *testing.T) {
	period := 30 * time.Minute
	sleepLen := 8 * time.Hour
	r := sleepRun(period, 10*time.Minute, sleepLen, tickFailure)
	r.ticks[0].kind = tickSuccess // the last success: the run's first tick

	deployed := deployedCondition(t, r)
	at, ok := r.firstAlertingWith(deployed, r.start, r.end)
	if !ok {
		t.Fatal("a source silent for 4h of awake time never reached Alerting")
	}
	want := r.start.Add(96*time.Minute + sleepLen)
	if !at.Equal(want) {
		t.Errorf("fired at %s, want %s (96m of awake silence, delayed by the %v sleep)", at.Format(time.RFC3339), want.Format(time.RFC3339), sleepLen)
	}
	if r.alertingWith(deployed, r.start.Add(95*time.Minute+sleepLen)) {
		t.Error("still Alerting a minute before the awake threshold plus for was reached")
	}

	wake := r.start.Add(10*time.Minute + sleepLen)
	early, ok := r.firstAlertingWith(r.wallCondition, r.start, r.end)
	if !ok || !early.Equal(wake.Add(persistFor)) {
		t.Errorf("control: retired expression first fires at %s (ok=%v), want %s: 5m after the wake, on 10 awake minutes of silence", early.Format(time.RFC3339), ok, wake.Add(persistFor).Format(time.RFC3339))
	}
}

// awakeBetween takes every sleep window out of the span it is asked about, however the
// span and the windows overlap.
func TestAwakeBetweenExcludesSleepWindows(t *testing.T) {
	start := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	r := srcRun{start: start, asleep: []sleepSpan{
		{start.Add(10 * time.Minute), start.Add(70 * time.Minute)},
		{start.Add(100 * time.Minute), start.Add(110 * time.Minute)},
	}}
	for _, c := range []struct {
		a, b time.Duration
		want time.Duration
	}{
		{0, 5 * time.Minute, 5 * time.Minute},                                    // before any sleep
		{0, 70 * time.Minute, 10 * time.Minute},                                  // ends exactly at the wake
		{0, 120 * time.Minute, 10*time.Minute + 30*time.Minute + 10*time.Minute}, // across both windows
		{20 * time.Minute, 60 * time.Minute, 0},                                  // entirely asleep
		{5 * time.Minute, 15 * time.Minute, 5 * time.Minute},                     // starts awake, falls asleep
		{65 * time.Minute, 80 * time.Minute, 10 * time.Minute},                   // starts asleep, wakes
		{80 * time.Minute, 70 * time.Minute, 0},                                  // reversed
	} {
		if got := r.awakeBetween(start.Add(c.a), start.Add(c.b)); got != c.want {
			t.Errorf("awakeBetween(+%v, +%v) = %v, want %v", c.a, c.b, got, c.want)
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
