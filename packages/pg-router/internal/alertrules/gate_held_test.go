package alertrules

import (
	"strings"
	"testing"
)

// pg-router-gate-held (pg2-l6ody): a Gate Registry gate in force for longer than the
// alert's `for:`. pg_router_gates_active is the only gate-age signal (a 1-per-TYPE
// gauge, absent when nothing is gated; there is no since-timestamp), so the hold
// duration is expressed by the rule's `for:`. last_over_time bridges the gap a
// daemon restart or a sleeping laptop leaves in the series.
const wantGateHeldExpr = `max by (type)(last_over_time(pg_router_gates_active[10m])) > 0`

func TestGateHeldRule(t *testing.T) {
	r := ruleBlock(t, "pg-router-gate-held")
	if got := ruleExpr(t, r); got != wantGateHeldExpr {
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
	for _, need := range []string{"pg-router gate list", "pg-router gate clear"} {
		if !strings.Contains(ann, need) {
			t.Errorf("annotations lost the remediation %q", need)
		}
	}
}
