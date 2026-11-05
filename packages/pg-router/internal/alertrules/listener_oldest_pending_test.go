package alertrules

import (
	"strings"
	"testing"
)

// pg-router-listener-oldest-pending: the oldest pending age of a role's listener above
// 4 h (14400 s). The PromQL behaviour is covered by
// rule-tests/pg-router-listener-oldest-pending.test.yaml; this pins the Grafana-only
// fields promtool cannot run. The derivation of 14400 is in the alerts.yaml comment above
// the rule. Raise it there and here together. The per-listener pending count gauge has no
// alert by operator ruling (Phillip, 2026-11-02).
const wantListenerOldestPendingExpr = `max by (role) (pg_router_listener_oldest_pending_age_seconds) > 14400`

func TestListenerOldestPendingRule(t *testing.T) {
	r := ruleBlock(t, "pg-router-listener-oldest-pending")
	if got := ruleExpr(t, r); got != wantListenerOldestPendingExpr {
		t.Errorf("listener-oldest-pending expr:\n got %q\nwant %q", got, wantListenerOldestPendingExpr)
	}
	for _, need := range []string{
		"for: 30m",
		"noDataState: OK",
		"execErrState: Error",
		"severity: warning",
		"condition: C",
		"params: [0]",
		"type: gt",
		"reducer: last",
	} {
		if !strings.Contains(r, need) {
			t.Errorf("listener-oldest-pending rule lost %q", need)
		}
	}
	if strings.Contains(r, "pg_router_listener_pending{") || strings.Contains(r, "(pg_router_listener_pending") {
		t.Error("listener-oldest-pending must not alert on the per-listener pending count")
	}
}

func TestListenerOldestPendingNoEscalationAndStaticAnnotations(t *testing.T) {
	r := ruleBlock(t, "pg-router-listener-oldest-pending")
	if strings.Contains(r, "severity: critical") || strings.Contains(r, "escalation:") {
		t.Error("listener-oldest-pending must stay a plain warning without an operator ruling")
	}
	i := strings.Index(r, "annotations:")
	j := strings.Index(r, "data:")
	if i < 0 || j < i {
		t.Fatal("annotations block not found")
	}
	if ann := r[i:j]; strings.Contains(ann, "{{") {
		t.Errorf("annotations must not interpolate labels/values: %q", ann)
	}
}
