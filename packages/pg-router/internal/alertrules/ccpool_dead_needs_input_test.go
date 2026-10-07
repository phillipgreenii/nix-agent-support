package alertrules

import (
	"strings"
	"testing"
)

// pg-router-ccpool-dead-needs-input (pg2-s0pa3, ADR 0086): a ccpool session parked
// for a human whose tmux session is gone, held for more than 2 days. The signal is
// ccpool_session_states{live="false",state="needs_input"}, which reap stops counting
// once the row is closed, so the alert resolves on a close.
const wantDeadNeedsInputExpr = `max by (pool)(last_over_time(ccpool_session_states{live="false",state="needs_input"}[1h])) > 0`

func TestCcpoolDeadNeedsInputRule(t *testing.T) {
	r := ruleBlock(t, "pg-router-ccpool-dead-needs-input")
	if got := ruleExpr(t, r); got != wantDeadNeedsInputExpr {
		t.Errorf("dead-needs-input expr:\n got %q\nwant %q", got, wantDeadNeedsInputExpr)
	}
	for _, need := range []string{
		"for: 48h",
		"noDataState: OK",
		"execErrState: Error",
		"severity: warning",
		"condition: C",
		"params: [0]",
		"type: gt",
		"reducer: last",
		"from: 3600",
	} {
		if !strings.Contains(r, need) {
			t.Errorf("dead-needs-input rule lost %q", need)
		}
	}
}

// pg-router-probe copies a rule's annotations verbatim into the bead it files, so
// they MUST be static: no {{ }} interpolation.
func TestCcpoolDeadNeedsInputAnnotationsAreStatic(t *testing.T) {
	r := ruleBlock(t, "pg-router-ccpool-dead-needs-input")
	i := strings.Index(r, "annotations:")
	j := strings.Index(r, "data:")
	if i < 0 || j < i {
		t.Fatal("annotations block not found")
	}
	ann := r[i:j]
	if strings.Contains(ann, "{{") {
		t.Errorf("annotations must not interpolate labels/values: %q", ann)
	}
	for _, need := range []string{"ccpool attach", "ccpool close", "dead_needs_input_ttl"} {
		if !strings.Contains(ann, need) {
			t.Errorf("annotations lost the remediation %q", need)
		}
	}
}

// The alert must key on the NOT-live bucket of needs_input only: a live parked
// session is a human mid-conversation, not a stuck dead row.
func TestCcpoolDeadNeedsInputSelectsOnlyDeadNeedsInput(t *testing.T) {
	got := ruleExpr(t, ruleBlock(t, "pg-router-ccpool-dead-needs-input"))
	for _, need := range []string{`live="false"`, `state="needs_input"`} {
		if !strings.Contains(got, need) {
			t.Errorf("dead-needs-input expr lost %q", need)
		}
	}
	if strings.Contains(got, `live="true"`) || strings.Contains(got, `state=~`) {
		t.Errorf("dead-needs-input expr must not widen beyond live=false needs_input: %q", got)
	}
}

// escalation: human is pinned to pg-router-origin-unavailable alone
// (TestEscalationHumanLabelOnlyOnOriginUnavailable); this rule keeps the default
// triager routing until the operator rules otherwise.
func TestCcpoolDeadNeedsInputHasNoHumanEscalationLabel(t *testing.T) {
	if strings.Contains(ruleBlock(t, "pg-router-ccpool-dead-needs-input"), "escalation:") {
		t.Error("dead-needs-input must not carry an escalation label without an operator ruling")
	}
}
