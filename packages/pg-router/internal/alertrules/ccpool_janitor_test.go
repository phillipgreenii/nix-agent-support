package alertrules

import (
	"strings"
	"testing"
)

// The ccpool handler janitor's three rules (bead pg2-fx5x0l.7, INV-CCH-41). The
// janitor pushes its gauges through `ccpool emit-gauge`, one push per run.
const (
	wantJanitorStaleExpr    = `(time() - max(last_over_time(ccpool_janitor_last_success_timestamp_seconds[7d]))) > 1800`
	wantJanitorDeadlineExpr = `(min_over_time(ccpool_janitor_deadline_hit[35m]) == 1) and (count_over_time(ccpool_janitor_deadline_hit[35m]) >= 3)`
	wantJanitorIdleExpr     = `((min_over_time(ccpool_janitor_pass_errors[35m]) > 0) and (count_over_time(ccpool_janitor_pass_errors[35m]) >= 3)) or ((count(count_over_time(ccpool_janitor_reclaimed[6h]) >= 30)) and on() (sum(max_over_time(ccpool_janitor_reclaimed[6h])) == 0) and on() (max(min_over_time(ccpool_pool_capacity{dim="free"}[6h])) == 0))`
	wantJanitorStagnantExpr = `max by (pass) (last_over_time(ccpool_janitor_oldest_unexamined_age_seconds[35m])) > 21600`
)

func TestCcpoolJanitorRules(t *testing.T) {
	for _, tc := range []struct {
		uid, expr, forDur string
	}{
		{"pg-router-ccpool-janitor-stale", wantJanitorStaleExpr, "for: 1h"},
		{"pg-router-ccpool-janitor-deadline", wantJanitorDeadlineExpr, "for: 10m"},
		{"pg-router-ccpool-janitor-idle", wantJanitorIdleExpr, "for: 30m"},
		{"pg-router-ccpool-janitor-stagnant", wantJanitorStagnantExpr, "for: 30m"},
	} {
		r := ruleBlock(t, tc.uid)
		if got := ruleExpr(t, r); got != tc.expr {
			t.Errorf("%s expr:\n got %q\nwant %q", tc.uid, got, tc.expr)
		}
		for _, need := range []string{
			tc.forDur,
			// noDataState is explicit: a host without the janitor has no series, which
			// must not fire (the rule is registered everywhere the router alerts are).
			"noDataState: OK",
			"execErrState: Error",
			"severity: warning",
			"condition: C",
			"params: [0]",
			"type: gt",
			"reducer: last",
			"from: 600",
		} {
			if !strings.Contains(r, need) {
				t.Errorf("%s lost %q", tc.uid, need)
			}
		}
		if strings.Contains(r, "severity: critical") || strings.Contains(r, "escalation:") {
			t.Errorf("%s must stay a warning without an escalation label", tc.uid)
		}
		i, j := strings.Index(r, "annotations:"), strings.Index(r, "data:")
		if i < 0 || j < i || strings.Contains(r[i:j], "{{") {
			t.Errorf("%s annotations must exist and be static", tc.uid)
		}
		// Each action text names the first check.
		if !strings.Contains(r[i:j], "First check") {
			t.Errorf("%s description must name the first check", tc.uid)
		}
	}
}

// A janitor that never ran (no series) must not make the stale rule fire: the
// window selector keeps a stopped janitor visible for 7 days but a host without one
// has nothing to read.
func TestCcpoolJanitorStaleReadsAWindowNotAnInstant(t *testing.T) {
	got := ruleExpr(t, ruleBlock(t, "pg-router-ccpool-janitor-stale"))
	if !strings.Contains(got, "last_over_time(ccpool_janitor_last_success_timestamp_seconds[7d])") {
		t.Errorf("stale rule must read a 7d window: %q", got)
	}
}
