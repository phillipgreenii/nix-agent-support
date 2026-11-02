package alertrules

import (
	"strings"
	"testing"
)

// The two "ccpool went dark" rules (bead pg2-oewtdh): the recorder daemon was offline
// for 33 h on 2026-10-31/11-02 and only the generic failure-rate rule fired.
const (
	wantCapacityAbsentExpr = `absent_over_time(ccpool_pool_capacity[10m])`
	wantNoLaunchExpr       = `(max(min_over_time(pg_router_queue_depth{type=~"review[.]ready|feedback[.]ready|work[.]ready|drain[.](zr|pg2)|escalated[.](zr|pg2)"}[2h]) > 0 and count_over_time(pg_router_queue_depth{type=~"review[.]ready|feedback[.]ready|work[.]ready|drain[.](zr|pg2)|escalated[.](zr|pg2)"}[2h]) >= 200) unless on() (sum(count_over_time(ccpool_launch_outcome_total[2h])) > 0)) unless on() (max(last_over_time(pg_router_gates_active[10m])) > 0)`
)

func TestCcpoolDarkRules(t *testing.T) {
	for _, tc := range []struct {
		uid, expr, forDur string
	}{
		{"pg-router-ccpool-pool-capacity-absent", wantCapacityAbsentExpr, "for: 5m"},
		{"pg-router-ccpool-no-launch-while-queued", wantNoLaunchExpr, "for: 15m"},
	} {
		r := ruleBlock(t, tc.uid)
		if got := ruleExpr(t, r); got != tc.expr {
			t.Errorf("%s expr:\n got %q\nwant %q", tc.uid, got, tc.expr)
		}
		for _, need := range []string{
			tc.forDur,
			// absent_over_time and the backlog clause both return nothing when healthy,
			// so no data is the healthy state.
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
		i, j := strings.Index(r, "annotations:"), strings.Index(r, "data:")
		if i < 0 || j < i || strings.Contains(r[i:j], "{{") {
			t.Errorf("%s annotations must exist and be static", tc.uid)
		}
		if !strings.Contains(r[i:j], "First check") {
			t.Errorf("%s description must name the first check", tc.uid)
		}
	}
}

// The no-launch rule counts samples, not increase(): the counter has a sample only at a
// launch, so a lone sample in the window has no increase. Command-role queues must not
// be selected.
func TestCcpoolNoLaunchSelectsOnlyCcpoolQueues(t *testing.T) {
	got := ruleExpr(t, ruleBlock(t, "pg-router-ccpool-no-launch-while-queued"))
	if strings.Contains(got, "increase(ccpool_launch_outcome_total") {
		t.Errorf("must count samples, not increase(): %q", got)
	}
	for _, bad := range []string{"pr[.]changed", "repo[.]sync", "disk[.]check", `type=~".*"`} {
		if strings.Contains(got, bad) {
			t.Errorf("command-role queue %q must not be selected: %q", bad, got)
		}
	}
	if !strings.Contains(got, "pg_router_gates_active") {
		t.Errorf("a held gate must suppress the rule: %q", got)
	}
}
