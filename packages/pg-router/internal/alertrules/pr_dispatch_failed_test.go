package alertrules

import (
	"strings"
	"testing"
	"time"
)

// pg-router-pr-dispatch-failed (bead pg2-87pjb1): three or more pr.changed dispatches
// ended failed within an hour. Same source and model as event_dispatch_failed_test.go
// (pg_router_dispatch_failed_total{type,role}); the promtool cases run the real engine
// (rule-tests/pg-router-pr-dispatch-failed.test.yaml). The rule is deliberately NOT in
// the router probe's registeredRuleUIDs at first.

const wantPRDispatchFailedExpr = `sum by (type, role) (increase(pg_router_dispatch_failed_total{type="pr.changed"}[1h])) >= 3`

func TestPRDispatchFailedRule(t *testing.T) {
	r := ruleBlock(t, "pg-router-pr-dispatch-failed")
	if got := ruleExpr(t, r); got != wantPRDispatchFailedExpr {
		t.Errorf("pr-dispatch-failed expr:\n got %q\nwant %q", got, wantPRDispatchFailedExpr)
	}
	for _, need := range []string{
		"for: 10m", "noDataState: OK", "execErrState: Error", "severity: warning",
		"{{ $labels.role }}", "from: 3600", "type: gt", "params: [0]", "instant: true", "condition: C",
		"pg-decider work-item lookup backend", "beads dolt health", "host load",
	} {
		if !strings.Contains(r, need) {
			t.Errorf("pr-dispatch-failed rule lost %q", need)
		}
	}
}

// Three failures inside the hour reach Alerting after for: 10m; two never do.
func TestPRDispatchFailedNeedsThreeFailures(t *testing.T) {
	start := at("10:00:00")
	f := start.Add(10 * time.Minute)
	three := []time.Time{f, f.Add(time.Minute), f.Add(2 * time.Minute)}
	model := func(series []edfSample, tm time.Time) bool {
		for e := tm.Add(-10 * time.Minute); !e.After(tm); e = e.Add(edfEval) {
			if edfIncrease(series, e) < 3 {
				return false
			}
		}
		return true
	}
	s3 := edfSeries(start, start.Add(3*time.Hour), 4, three)
	if model(s3, f.Add(5*time.Minute)) {
		t.Error("alerting before the for hold elapsed")
	}
	if !model(s3, f.Add(15*time.Minute)) {
		t.Error("three failures did not reach Alerting after for")
	}
	if model(s3, f.Add(edfWindow+15*time.Minute)) {
		t.Error("still alerting after the failures left the 1h window")
	}
	s2 := edfSeries(start, start.Add(3*time.Hour), 4, three[:2])
	for tm := start; tm.Before(start.Add(3 * time.Hour)); tm = tm.Add(edfEval) {
		if model(s2, tm) {
			t.Fatalf("two failures reached Alerting at %v", tm)
		}
	}
}
