package alertrules

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The failure-rate alert is split by cause (pg2-irowq): budget stops get their
// own rule, at-capacity stays excluded from both, and the original rule
// (pg-router-failure-rate) becomes the RESIDUAL that keeps every failure not
// matched by the first two.
const (
	wantBudgetExpr   = `sum by (role) (rate(pg_router_failures_total{class="handler-error",reason="budget-exceeded"}[10m]))`
	wantResidualExpr = `sum by (class, role) (rate(pg_router_failures_total{reason!~"at-capacity|budget-exceeded"}[10m]))`
)

func ruleBlock(t *testing.T, uid string) string {
	t.Helper()
	b, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "- uid: "+uid+"\n")
	if i < 0 {
		t.Fatalf("rule %s not found", uid)
	}
	rest := src[i+1:]
	if j := strings.Index(rest, "\n      - uid:"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func ruleExpr(t *testing.T, block string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^\s*expr: (.+)$`).FindStringSubmatch(block)
	if m == nil {
		t.Fatal("no expr in rule")
	}
	return m[1]
}

func TestBudgetStopRule(t *testing.T) {
	r := ruleBlock(t, "pg-router-budget-stops")
	if got := ruleExpr(t, r); got != wantBudgetExpr {
		t.Errorf("budget-stops expr:\n got %q\nwant %q", got, wantBudgetExpr)
	}
	for _, need := range []string{"for: 10m", "noDataState: OK", "execErrState: Error", "severity: warning", "{{ $labels.role }}"} {
		if !strings.Contains(r, need) {
			t.Errorf("budget-stops rule lost %q", need)
		}
	}
}

func TestResidualFailureRateRule(t *testing.T) {
	r := ruleBlock(t, "pg-router-failure-rate")
	got := ruleExpr(t, r)
	if got != wantResidualExpr {
		t.Errorf("residual expr:\n got %q\nwant %q", got, wantResidualExpr)
	}
	// The residual excludes exactly the causes handled elsewhere: at-capacity
	// (intentionally silent) and budget-exceeded (its own rule). Nothing else.
	if !strings.Contains(got, `reason!~"at-capacity|budget-exceeded"`) {
		t.Errorf("residual must exclude exactly at-capacity and budget-exceeded: %q", got)
	}
	for _, need := range []string{"for: 10m", "noDataState: OK", "execErrState: Error", "{{ $labels.role }}", "{{ $labels.class }}"} {
		if !strings.Contains(r, need) {
			t.Errorf("residual rule lost %q", need)
		}
	}
}
