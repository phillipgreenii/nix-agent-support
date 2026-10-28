package alertrules

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// pg-router-event-dispatch-failed (docket pg2-2qyyh.5, INV-ICD-21 item 4): a decider
// dispatch for an issue.changed event that ended failed after the router's retry window.
// The source is pg_router_dispatch_failed_total{type,role}. No PromQL engine is a
// dependency, so the rule is modelled here: the YAML expr is pinned to the exact text, and
// a simplified increase() (first-sample baseline, no extrapolation) is replayed over
// synthetic counter samples. The promtool cases run the real engine
// (rule-tests/pg-router-event-dispatch-failed.test.yaml).

const wantEventDispatchFailedExpr = `sum by (type, role) (increase(pg_router_dispatch_failed_total{type="issue.changed"}[1h])) >= 1`

const (
	edfWindow    = time.Hour
	edfThreshold = 1.0
	edfScrape    = 30 * time.Second
	edfEval      = time.Minute
	edfFor       = 5 * time.Minute
)

func TestEventDispatchFailedRule(t *testing.T) {
	r := ruleBlock(t, "pg-router-event-dispatch-failed")
	if got := ruleExpr(t, r); got != wantEventDispatchFailedExpr {
		t.Errorf("event-dispatch-failed expr:\n got %q\nwant %q", got, wantEventDispatchFailedExpr)
	}
	// from: 3600 matches the [1h] range; gt 0 over the PromQL-filtered value (no gte
	// evaluator); only type and role are grouping labels.
	for _, need := range []string{
		"for: 5m", "noDataState: OK", "execErrState: Error", "severity: warning",
		"{{ $labels.role }}", "from: 3600", "type: gt", "params: [0]", "instant: true", "condition: C",
	} {
		if !strings.Contains(r, need) {
			t.Errorf("event-dispatch-failed rule lost %q", need)
		}
	}
	if regexp.MustCompile(`by \([^)]*(class|reason|source)`).MatchString(wantEventDispatchFailedExpr) {
		t.Error("expr must not group by class, reason or source")
	}
}

// The rule cannot tell whether the issue changed since the failure; its description MUST
// name that approximation (the design names the gap).
func TestEventDispatchFailedDescriptionNamesTheApproximation(t *testing.T) {
	r := ruleBlock(t, "pg-router-event-dispatch-failed")
	i := strings.Index(r, "description:")
	j := strings.Index(r, "data:")
	if i < 0 || j < i {
		t.Fatal("description not found")
	}
	d := r[i:j]
	for _, need := range []string{"cannot tell whether the issue changed since the failure", "no per-entity label"} {
		if !strings.Contains(d, need) {
			t.Errorf("description lost %q", need)
		}
	}
}

type edfSample struct {
	at time.Time
	v  float64
}

// edfIncrease models increase(m[1h]) at t: the last minus the first sample inside the
// window, and 0 for fewer than two samples, so a new series' first sample is a baseline.
func edfIncrease(series []edfSample, t time.Time) float64 {
	var in []edfSample
	for _, s := range series {
		if s.at.After(t.Add(-edfWindow)) && !s.at.After(t) {
			in = append(in, s)
		}
	}
	if len(in) < 2 {
		return 0
	}
	return in[len(in)-1].v - in[0].v
}

// edfSeries builds a counter scraped every edfScrape over [from, to] that steps by one at
// each failure time. A series that already exists at from holds base until the failure.
func edfSeries(from, to time.Time, base float64, failures []time.Time) []edfSample {
	var out []edfSample
	for t := from; !t.After(to); t = t.Add(edfScrape) {
		v := base
		for _, f := range failures {
			if !f.After(t) {
				v++
			}
		}
		out = append(out, edfSample{t, v})
	}
	return out
}

// edfAlerting models `for: 5m`: the expression must hold at every evaluation in the
// trailing edfFor ending at t.
func edfAlerting(series []edfSample, t time.Time) bool {
	for e := t.Add(-edfFor); !e.After(t); e = e.Add(edfEval) {
		if edfIncrease(series, e) < edfThreshold {
			return false
		}
	}
	return true
}

func edfEver(series []edfSample, from, to time.Time) bool {
	for t := from; !t.After(to); t = t.Add(edfEval) {
		if edfAlerting(series, t) {
			return true
		}
	}
	return false
}

func TestEventDispatchFailedFiresAfterForAndClearsAfterTheWindow(t *testing.T) {
	start := at("10:00:00")
	fail := start.Add(10 * time.Minute)
	series := edfSeries(start, start.Add(3*time.Hour), 4, []time.Time{fail})
	if edfAlerting(series, fail.Add(2*time.Minute)) {
		t.Error("alerting before the for hold elapsed")
	}
	if !edfAlerting(series, fail.Add(10*time.Minute)) {
		t.Error("one failed dispatch did not reach Alerting after for")
	}
	if edfAlerting(series, fail.Add(edfWindow+2*time.Minute)) {
		t.Error("still alerting after the failure left the 1h window")
	}
}

func TestEventDispatchFailedQuietWithoutFailures(t *testing.T) {
	start := at("10:00:00")
	series := edfSeries(start, start.Add(3*time.Hour), 7, nil)
	if edfEver(series, start, start.Add(3*time.Hour)) {
		t.Fatal("a flat counter reached Alerting")
	}
}

// Accepted limitation, pinned: the first failure of a role after a restart creates the
// series with value 1, which increase() takes as its baseline, so it is not counted.
func TestEventDispatchFailedFreshSeriesFirstFailureIsNotCounted(t *testing.T) {
	start := at("10:00:00")
	fail := start.Add(10 * time.Minute)
	var series []edfSample
	for _, s := range edfSeries(start, start.Add(3*time.Hour), 0, []time.Time{fail}) {
		if !s.at.Before(fail) {
			series = append(series, s)
		}
	}
	if edfEver(series, start, start.Add(3*time.Hour)) {
		t.Fatal("the first failure of a fresh series reached Alerting; increase() takes it as the baseline")
	}
	// A second failure on that series is counted.
	series = nil
	for _, s := range edfSeries(start, start.Add(3*time.Hour), 0, []time.Time{fail, fail.Add(5 * time.Minute)}) {
		if !s.at.Before(fail) {
			series = append(series, s)
		}
	}
	if !edfEver(series, start, start.Add(3*time.Hour)) {
		t.Fatal("a second failure on the fresh series never reached Alerting")
	}
}
