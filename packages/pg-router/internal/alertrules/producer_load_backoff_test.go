package alertrules

import (
	"strings"
	"testing"
	"time"
)

// Host-load backoff and the two rules that share its cause: the per-source
// pg-router-source-persistent-failure rule (which must stand aside for a stall
// caused only by the router skipping its producer ticks) and
// pg-router-producer-load-backoff (which names that cause once).
//
// On 2026-11-12 host load1 sat above the backoff threshold for about 50 minutes,
// every producer tick was skipped, every source's last-success age grew together
// and the per-source rule filed about 24 escalated beads. As with the sibling
// persistent-failure test, no PromQL engine is a module dependency, so the
// expressions are pinned to their exact text and modelled here; the PromQL
// behaviour itself runs under promtool (rule-tests/*.test.yaml, flake check
// test-pg-router-alert-rules).

const (
	// the per-source rule, as deployed: the age clause, then the backoff exclusion
	wantPersistentFailureExclusion = ` unless on() (max_over_time(pg_router_producer_load_backoff[10m]) > 0)`
	wantBackoffExpr                = `avg_over_time(pg_router_producer_load_backoff[30m]) > 0.7`

	backoffExcludeWindow = 10 * time.Minute
	backoffAvgWindow     = 30 * time.Minute
	backoffAvgThreshold  = 0.7
	backoffFor           = 5 * time.Minute
)

func TestPersistentFailureStandsAsideForBackoff(t *testing.T) {
	got := ruleExpr(t, ruleBlock(t, "pg-router-source-persistent-failure"))
	if got != wantPersistentFailureExpr+wantPersistentFailureExclusion {
		t.Errorf("persistent-failure expr:\n got %q\nwant %q", got, wantPersistentFailureExpr+wantPersistentFailureExclusion)
	}
}

func TestBackoffRuleShape(t *testing.T) {
	r := ruleBlock(t, "pg-router-producer-load-backoff")
	if got := ruleExpr(t, r); got != wantBackoffExpr {
		t.Errorf("backoff expr:\n got %q\nwant %q", got, wantBackoffExpr)
	}
	for _, need := range []string{"for: 5m", "noDataState: OK", "execErrState: Error", "severity: warning", "params: [0]"} {
		if !strings.Contains(r, need) {
			t.Errorf("backoff rule lost %q", need)
		}
	}
	if strings.Contains(r, "escalation:") {
		t.Error("backoff rule must not carry an escalation label; probe registration is a separate, deployment-repo change")
	}
}

// bkSeries is one evaluation minute's inputs.
type bkSeries struct {
	// backoff[i] is the load-backoff gauge sampled at minute i (1 = the last
	// producer tick was skipped for host load).
	backoff []float64
	// pass[i] is whether the source had a successful pass at minute i. A source
	// whose passes FAIL never succeeds; a source that was merely not polled never
	// does either, which is the point: the age alone cannot tell them apart.
	pass []bool
}

// ageAt is the last-success age in minutes at minute i (process start at 0).
func (s bkSeries) ageAt(i int) int {
	last := 0
	for j := 0; j <= i; j++ {
		if s.pass[j] {
			last = j
		}
	}
	return i - last
}

func (s bkSeries) maxBackoffIn(i, window int) float64 {
	m := 0.0
	for j := max(0, i-window+1); j <= i; j++ {
		m = max(m, s.backoff[j])
	}
	return m
}

func (s bkSeries) avgBackoffIn(i, window int) float64 {
	sum, n := 0.0, 0
	for j := max(0, i-window+1); j <= i; j++ {
		sum += s.backoff[j]
		n++
	}
	return sum / float64(n)
}

// sourceCond models the per-source expr; withExclusion=false is the retired form
// (the negative control that shows the scenario is one the old rule fired on).
func (s bkSeries) sourceCond(i int, period time.Duration, withExclusion bool) bool {
	age := time.Duration(s.ageAt(i)) * time.Minute
	if age <= persistThreshold(period) {
		return false
	}
	return !(withExclusion && s.maxBackoffIn(i, int(backoffExcludeWindow/time.Minute)) > 0)
}

func (s bkSeries) backoffCond(i int) bool {
	return s.avgBackoffIn(i, int(backoffAvgWindow/time.Minute)) > backoffAvgThreshold
}

// firing reports whether cond held for the whole trailing `for` window ending at i.
func firing(i int, f time.Duration, cond func(int) bool) bool {
	n := int(f / time.Minute)
	if i < n {
		return false
	}
	for j := i - n; j <= i; j++ {
		if !cond(j) {
			return false
		}
	}
	return true
}

func everFiring(minutes int, cond func(int) bool) bool {
	for i := 0; i < minutes; i++ {
		if firing(i, persistFor, cond) {
			return true
		}
	}
	return false
}

// build makes a run of `minutes` minutes with backoff active in [from, to) per
// skipPattern (nil = every minute), and successful passes every minute outside
// the backoff when passing is true.
func buildRun(minutes, from, to int, skipPattern func(i int) bool, passing bool) bkSeries {
	s := bkSeries{backoff: make([]float64, minutes), pass: make([]bool, minutes)}
	for i := 0; i < minutes; i++ {
		inBackoff := i >= from && i < to && (skipPattern == nil || skipPattern(i))
		if inBackoff {
			s.backoff[i] = 1
		}
		s.pass[i] = passing && !inBackoff
	}
	return s
}

// The 2026-11-12 episode: 50 minutes of backoff, the source healthy before and
// after. The per-source rule must stay quiet for it; the retired form fired.
func TestBackoffOnlyStallDoesNotFirePerSourceRule(t *testing.T) {
	for _, period := range []time.Duration{10 * time.Second, time.Minute, 30 * time.Minute} {
		s := buildRun(120, 20, 70, nil, true)
		if everFiring(120, func(i int) bool { return s.sourceCond(i, period, true) }) {
			t.Errorf("period %v: per-source rule fired for a stall caused only by host-load backoff", period)
		}
		if !everFiring(120, func(i int) bool { return s.sourceCond(i, period, false) }) && period <= time.Minute {
			t.Errorf("period %v: negative control: the retired form (no exclusion) should fire on this episode", period)
		}
	}
	// The flapping shape of the real log (47 to 60 of 60 ticks skipped): the gauge
	// reads 0 in some minutes, the 10m exclusion window still covers it.
	s := buildRun(120, 20, 70, func(i int) bool { return i%5 != 0 }, true)
	if everFiring(120, func(i int) bool { return s.sourceCond(i, time.Minute, true) }) {
		t.Error("per-source rule fired for a flapping backoff stall")
	}
}

// A source whose passes fail, with no backoff at all, still fires.
func TestFailingSourceStillFiresPerSourceRule(t *testing.T) {
	s := buildRun(120, 0, 0, nil, false)
	if !everFiring(120, func(i int) bool { return s.sourceCond(i, time.Minute, true) }) {
		t.Error("per-source rule did not fire for a source whose passes all fail")
	}
}

// A source that keeps failing AFTER a backoff episode ends fires once the 10m
// exclusion window has passed.
func TestFailureAfterBackoffFiresOnceExclusionLapses(t *testing.T) {
	s := buildRun(200, 10, 20, nil, false)
	if !everFiring(200, func(i int) bool { return s.sourceCond(i, time.Minute, true) }) {
		t.Error("per-source rule never fired for a source that kept failing after a short backoff")
	}
}

func TestSustainedBackoffRuleFiresAndShortOneStaysQuiet(t *testing.T) {
	long := buildRun(120, 20, 70, nil, true)
	if !everFiring(120, long.backoffCond) {
		t.Error("backoff rule did not fire for a 50 minute backoff")
	}
	flap := buildRun(120, 20, 70, func(i int) bool { return i%5 != 0 }, true)
	if !everFiring(120, flap.backoffCond) {
		t.Error("backoff rule did not fire for a flapping 50 minute backoff (4 of 5 minutes skipped)")
	}
	short := buildRun(120, 20, 35, nil, true)
	if everFiring(120, short.backoffCond) {
		t.Error("backoff rule fired for a 15 minute backoff")
	}
	none := buildRun(120, 0, 0, nil, true)
	if everFiring(120, none.backoffCond) {
		t.Error("backoff rule fired with no backoff")
	}
}
