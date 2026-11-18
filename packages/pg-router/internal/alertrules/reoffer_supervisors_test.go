package alertrules

import (
	"strings"
	"testing"
)

// pg-router-reoffered-after-shutdown: three or more shutdown hand-backs in an hour
// (a restart loop), no `for:` delay. One or two are an ordinary restart.
const wantReofferedExpr = `sum by (role)(increase(pg_router_events_reoffered_after_shutdown_total[1h])) >= 3`

// pg-router-supervisors-per-session: the supervisor count the latest ended dispatch
// reported for a session is above one, no `for:` delay.
const wantSupervisorsExpr = `max by (role, session)(last_over_time(pg_router_supervisors_per_session[10m])) > 1`

func TestReofferedAfterShutdownRule(t *testing.T) {
	r := ruleBlock(t, "pg-router-reoffered-after-shutdown")
	if got := unquoteExpr(ruleExpr(t, r)); got != wantReofferedExpr {
		t.Errorf("expr:\n got %q\nwant %q", got, wantReofferedExpr)
	}
	for _, need := range []string{"for: 0s", "noDataState: OK", "execErrState: Error", "severity: warning", "condition: C", "params: [0]", "type: gt", "reducer: last", "from: 3600"} {
		if !strings.Contains(r, need) {
			t.Errorf("rule lost %q", need)
		}
	}
}

func TestSupervisorsPerSessionRule(t *testing.T) {
	r := ruleBlock(t, "pg-router-supervisors-per-session")
	if got := unquoteExpr(ruleExpr(t, r)); got != wantSupervisorsExpr {
		t.Errorf("expr:\n got %q\nwant %q", got, wantSupervisorsExpr)
	}
	for _, need := range []string{"for: 0s", "noDataState: OK", "execErrState: Error", "severity: warning", "condition: C", "params: [0]", "type: gt", "reducer: last"} {
		if !strings.Contains(r, need) {
			t.Errorf("rule lost %q", need)
		}
	}
}

// pg-router-probe copies a rule's annotations verbatim into the bead it files, so
// they MUST be static: no {{ }} interpolation.
func TestReofferAndSupervisorAnnotationsAreStatic(t *testing.T) {
	for _, uid := range []string{"pg-router-reoffered-after-shutdown", "pg-router-supervisors-per-session"} {
		r := ruleBlock(t, uid)
		i := strings.Index(r, "annotations:")
		j := strings.Index(r, "data:")
		if i < 0 || j < i {
			t.Fatalf("%s: annotations block not found", uid)
		}
		if strings.Contains(r[i:j], "{{") {
			t.Errorf("%s: annotations must not interpolate labels/values", uid)
		}
	}
}
