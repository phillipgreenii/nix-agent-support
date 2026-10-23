package alertrules

import (
	"strings"
	"testing"
)

// pg-router-worktree-growth (pg2-hxxtt): the floor of pg_router_worktree_count over the
// last 12h rising by 10 or more against the floor of the 12h before it. The PromQL
// behaviour is covered by rule-tests/pg-router-worktree-growth.test.yaml; this pins the
// Grafana-only fields promtool cannot run. Raise the 10 here and in alerts.yaml together.
const wantWorktreeGrowthExpr = `min_over_time(sum(pg_router_worktree_count)[12h:10m]) - min_over_time(sum(pg_router_worktree_count)[12h:10m] offset 12h) >= 10`

func TestWorktreeGrowthRule(t *testing.T) {
	r := ruleBlock(t, "pg-router-worktree-growth")
	if got := ruleExpr(t, r); got != wantWorktreeGrowthExpr {
		t.Errorf("worktree-growth expr:\n got %q\nwant %q", got, wantWorktreeGrowthExpr)
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
			t.Errorf("worktree-growth rule lost %q", need)
		}
	}
}

// The notifier pages on critical only (pg2-lu6dm), so this stays a warning without an
// operator ruling, and carries no escalation label.
func TestWorktreeGrowthIsWarningWithoutEscalation(t *testing.T) {
	r := ruleBlock(t, "pg-router-worktree-growth")
	if strings.Contains(r, "severity: critical") {
		t.Error("worktree-growth must stay severity: warning without an operator ruling")
	}
	if strings.Contains(r, "escalation:") {
		t.Error("worktree-growth must not carry an escalation label without an operator ruling")
	}
}

// pg-router-probe copies a rule's annotations verbatim into the bead it files, so they
// MUST be static: no {{ }} interpolation.
func TestWorktreeGrowthAnnotationsAreStatic(t *testing.T) {
	r := ruleBlock(t, "pg-router-worktree-growth")
	i := strings.Index(r, "annotations:")
	j := strings.Index(r, "data:")
	if i < 0 || j < i {
		t.Fatal("annotations block not found")
	}
	if ann := r[i:j]; strings.Contains(ann, "{{") {
		t.Errorf("annotations must not interpolate labels/values: %q", ann)
	}
}
