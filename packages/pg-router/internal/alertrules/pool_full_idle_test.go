package alertrules

import (
	"strings"
	"testing"
)

// pg-router-pool-full-idle (pg2-owccx): a ccpool pool is full (free == 0) while
// none of its counted sessions is active and demand is waiting. The demand
// clause is the LEFT-hand side so the alert value is the decline count (> 0) and
// the instance carries `role`; role -> pool is mapped EXPLICITLY (review, worker,
// feedback -> pg-router-ccpool-<role>; *triager -> pg-router-ccpool), because
// mapping every role with "(.*)" would invent pools like pg-router-ccpool-desk-pr.
const wantPoolFullIdleExpr = `(label_replace(sum by (role)(increase(pg_router_failures_total{class="declined",reason="at-capacity",role=~"review|worker|feedback"}[15m])), "pool", "pg-router-ccpool-$1", "role", "(.*)") or label_replace(sum by (role)(increase(pg_router_failures_total{class="declined",reason="at-capacity",role=~".*triager"}[15m])), "pool", "pg-router-ccpool", "", "")) > 0 and on(pool) (max by (pool)(last_over_time(ccpool_pool_capacity{dim="free"}[10m])) == 0) and on(pool) (sum by (pool)(last_over_time(ccpool_session_states{live="true",state=~"starting|ready|working"}[10m])) == 0)`

func TestPoolFullIdleRule(t *testing.T) {
	r := ruleBlock(t, "pg-router-pool-full-idle")
	if got := ruleExpr(t, r); got != wantPoolFullIdleExpr {
		t.Errorf("pool-full-idle expr:\n got %q\nwant %q", got, wantPoolFullIdleExpr)
	}
	for _, need := range []string{
		"for: 15m",
		"noDataState: OK",
		"execErrState: Error",
		"severity: warning",
		"condition: C",
		"params: [0]",
		"type: gt",
		"reducer: last",
		"from: 900",
		"{{ $labels.pool }}",
		"{{ $labels.role }}",
		"ccpool --pool ~/.local/state/{{ $labels.pool }} list --json",
	} {
		if !strings.Contains(r, need) {
			t.Errorf("pool-full-idle rule lost %q", need)
		}
	}
}

// The pool mapping is explicit: the rule must never map every role through a
// catch-all label_replace, which would invent pools for command roles.
func TestPoolFullIdleMapsRolesExplicitly(t *testing.T) {
	got := ruleExpr(t, ruleBlock(t, "pg-router-pool-full-idle"))
	for _, need := range []string{
		`role=~"review|worker|feedback"`,
		`"pool", "pg-router-ccpool-$1", "role", "(.*)"`,
		`role=~".*triager"`,
		`"pool", "pg-router-ccpool", "", ""`,
	} {
		if !strings.Contains(got, need) {
			t.Errorf("pool-full-idle expr lost %q", need)
		}
	}
	if strings.Contains(got, `{class="declined",reason="at-capacity"}`) || strings.Contains(got, `role=~".*"`) {
		t.Errorf("pool-full-idle must not select every role: %q", got)
	}
}
