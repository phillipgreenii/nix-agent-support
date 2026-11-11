package alertrules

import (
	"strings"
	"testing"
)

// pg-router-gate-held: a resource below its best level AND a registration blocked on
// it, for longer than the alert's `for:`. pg_router_resource_level (0 is best) says the
// resource is below best; pg_router_requirement_blocked_total rising for the same
// resource says a registration is held back by it. The hold duration is not a metric, so
// it is expressed by the rule's `for:`. last_over_time and the 10m increase window bridge
// the gap a daemon restart or a sleeping laptop leaves in the series.
const wantGateHeldExpr = `max by (resource)(last_over_time(pg_router_resource_level[10m]) > 0) and on(resource) (sum by (resource)(increase(pg_router_requirement_blocked_total[10m])) > 0)`

func TestGateHeldRule(t *testing.T) {
	r := ruleBlock(t, "pg-router-gate-held")
	if got := unquoteExpr(ruleExpr(t, r)); got != wantGateHeldExpr {
		t.Errorf("gate-held expr:\n got %q\nwant %q", got, wantGateHeldExpr)
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
		"from: 600",
	} {
		if !strings.Contains(r, need) {
			t.Errorf("gate-held rule lost %q", need)
		}
	}
}

// Desktop notifications are critical-only (ADR 0040 amendment), so this rule is a
// warning on purpose; it must not be promoted without an operator ruling, and it
// carries no escalation label (pinned to origin-unavailable alone).
func TestGateHeldIsWarningWithoutEscalation(t *testing.T) {
	r := ruleBlock(t, "pg-router-gate-held")
	if strings.Contains(r, "severity: critical") {
		t.Error("gate-held must stay severity: warning without an operator ruling")
	}
	if strings.Contains(r, "escalation:") {
		t.Error("gate-held must not carry an escalation label without an operator ruling")
	}
}

// pg-router-probe copies a rule's annotations verbatim into the bead it files, so
// they MUST be static: no {{ }} interpolation.
func TestGateHeldAnnotationsAreStatic(t *testing.T) {
	r := ruleBlock(t, "pg-router-gate-held")
	i := strings.Index(r, "annotations:")
	j := strings.Index(r, "data:")
	if i < 0 || j < i {
		t.Fatal("annotations block not found")
	}
	ann := r[i:j]
	if strings.Contains(ann, "{{") {
		t.Errorf("annotations must not interpolate labels/values: %q", ann)
	}
	for _, need := range []string{"pg-router level list", "pg-router level clear", "pg_router_resource_level"} {
		if !strings.Contains(ann, need) {
			t.Errorf("annotations lost the remediation %q", need)
		}
	}
	for _, gone := range []string{"pg_router_gates" + "_active", "pg-router gate" + " list", "pg-router gate" + " clear"} {
		if strings.Contains(ann, gone) {
			t.Errorf("annotations still name the removed %q", gone)
		}
	}
}
