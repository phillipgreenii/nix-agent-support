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
	wantBudgetExpr   = `sum by (role) (increase(pg_router_failures_total{class="handler-error",reason="budget-exceeded"}[1h])) >= 2`
	wantResidualExpr = `sum by (class, role) (rate(pg_router_failures_total{reason!~"at-capacity|budget-exceeded|origin-unavailable|triager-failure|upstream-killed|skipped-.+"}[10m]))`
	wantTriagerExpr  = `sum by (role) (rate(pg_router_failures_total{class="handler-error",reason="triager-failure"}[10m]))`
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

// pg2-vn4jb: pg-router-budget-stops is repeat-only. The expr pins the exact
// text (the `>= 2` lives in PromQL, the threshold node stays `gt 0`), the
// relativeTimeRange matches the [1h] range, `for` is 0m (the window already
// encodes "repeated"), and the only grouping label is role. The window
// semantics are replayed in budget_stops_test.go.
func TestBudgetStopRule(t *testing.T) {
	r := ruleBlock(t, "pg-router-budget-stops")
	if got := ruleExpr(t, r); got != wantBudgetExpr {
		t.Errorf("budget-stops expr:\n got %q\nwant %q", got, wantBudgetExpr)
	}
	for _, need := range []string{
		"for: 0m", "noDataState: OK", "execErrState: Error", "severity: warning",
		"{{ $labels.role }}", "from: 3600", "type: gt", "params: [0]", "instant: true",
	} {
		if !strings.Contains(r, need) {
			t.Errorf("budget-stops rule lost %q", need)
		}
	}
	// Role-only cardinality: the only grouping label is role, and only one
	// aggregation exists.
	if !strings.Contains(wantBudgetExpr, "sum by (role) (") || strings.Count(wantBudgetExpr, " by (") != 1 {
		t.Error("expr must aggregate by role only")
	}
	if regexp.MustCompile(`by \([^)]*(class|reason|source|bead|pool)`).MatchString(wantBudgetExpr) {
		t.Error("expr must not group by any label other than role")
	}
	// The old behavior (rate over 10m, for: 10m, paged while the rate stayed
	// above 0) is gone, and the annotation no longer describes it.
	for _, gone := range []string{"for: 10m", "from: 600", "stayed above 0", "[10m]"} {
		if strings.Contains(r, gone) {
			t.Errorf("budget-stops rule still carries the old %q", gone)
		}
	}
	if !strings.Contains(r, wantBudgetExpr) || !strings.Contains(r, "2 or more times in the last hour") {
		t.Error("budget-stops annotations must state the repeat-only (>= 2 in 1h) condition")
	}
}

func TestResidualFailureRateRule(t *testing.T) {
	r := ruleBlock(t, "pg-router-failure-rate")
	got := ruleExpr(t, r)
	if got != wantResidualExpr {
		t.Errorf("residual expr:\n got %q\nwant %q", got, wantResidualExpr)
	}
	// The residual excludes exactly the causes handled elsewhere: origin-unavailable
	// (own rule, pg2-o03wl so an outage pages once), at-capacity
	// (intentionally silent), budget-exceeded (its own rule) and triager-failure
	// (its own rule, pg2-u2yub: a failing escalation triager must not fire the
	// worker/review failure-rate alert), upstream-killed (a gh call killed by the
	// 30s scriptout timeout; its own sustained-only rule, pg2-fy2pm). And the
	// handler's review-precheck
	// declines (skipped-*, pg2-5x29j), which are healthy and moot by construction. Nothing else.
	if !strings.Contains(got, `reason!~"at-capacity|budget-exceeded|origin-unavailable|triager-failure|upstream-killed|skipped-.+"`) {
		t.Errorf("residual must exclude exactly at-capacity, budget-exceeded, origin-unavailable, triager-failure, upstream-killed and skipped-*: %q", got)
	}
	for _, need := range []string{"for: 10m", "noDataState: OK", "execErrState: Error", "{{ $labels.role }}", "{{ $labels.class }}"} {
		if !strings.Contains(r, need) {
			t.Errorf("residual rule lost %q", need)
		}
	}
}

// pg2-u2yub: triager failures are bulkheaded out of the residual and alerted
// on under their own rule, selecting exactly the series the residual excludes.
func TestTriagerFailuresRule(t *testing.T) {
	r := ruleBlock(t, "pg-router-triager-failures")
	if got := ruleExpr(t, r); got != wantTriagerExpr {
		t.Errorf("triager-failures expr:\n got %q\nwant %q", got, wantTriagerExpr)
	}
	for _, need := range []string{"for: 30m", "noDataState: OK", "execErrState: Error", "severity: warning", "{{ $labels.role }}"} {
		if !strings.Contains(r, need) {
			t.Errorf("triager-failures rule lost %q", need)
		}
	}
}

// pg2-fy2pm: the residual no longer matches reason=upstream-killed, but still
// matches every reason the Emitter can attach otherwise (and no reason at
// all), so an OOM/SIGKILL without "scriptout:" keeps paging individually.
// A Prometheus `!~` matcher is fully anchored and treats an absent label as "".
func TestResidualExcludesUpstreamKilledOnly(t *testing.T) {
	m := regexp.MustCompile(`reason!~"([^"]+)"`).FindStringSubmatch(wantResidualExpr)
	if m == nil {
		t.Fatal("no reason!~ matcher in residual expr")
	}
	re := regexp.MustCompile("^(?:" + m[1] + ")$")
	for reason, wantExcluded := range map[string]bool{
		"upstream-killed":    true,
		"budget-exceeded":    true,
		"triager-failure":    true,
		"origin-unavailable": true,
		"at-capacity":        true,
		"skipped-pr-merged":  true,
		"":                   false, // plain handler-error, dispatch-failure: keep paging
		"capacity-unknown":   false,
		"upstream-killed2":   false,
	} {
		if got := re.MatchString(reason); got != wantExcluded {
			t.Errorf("residual excludes reason %q = %v, want %v", reason, got, wantExcluded)
		}
	}
}
