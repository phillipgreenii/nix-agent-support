package alertrules

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/metrics"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
)

// The failure-rate alert is split by cause (pg2-irowq): budget stops get their
// own rule, at-capacity stays excluded from both, and the original rule
// (pg-router-failure-rate) becomes the RESIDUAL that keeps every failure not
// matched by the first two.
const (
	wantBudgetExpr   = `sum by (role) (increase(pg_router_failures_total{class="handler-error",reason="budget-exceeded"}[1h])) >= 2`
	wantResidualExpr = `sum by (class, role) (rate(pg_router_failures_total{reason!~"at-capacity|budget-exceeded|dispatch-retry|origin-unavailable|triager-failure|upstream-killed|upstream-unavailable|usage-limit|launch-failed|session-in-flight|skipped-.+"}[10m]))`
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
	// 30s scriptout timeout; its own sustained-only rule, pg2-fy2pm), usage-limit
	// (a session that died on an account usage or spend limit or an API outage,
	// ruled not to page, no rule of its own, pg2-e0imq). And the
	// handler's review-precheck
	// declines (skipped-*, pg2-5x29j), which are healthy and moot by construction, and
	// the handler's session-in-flight decline (pg2-qaeb8), routine
	// backpressure like at-capacity. Nothing else.
	if !strings.Contains(got, `reason!~"at-capacity|budget-exceeded|dispatch-retry|origin-unavailable|triager-failure|upstream-killed|upstream-unavailable|usage-limit|launch-failed|session-in-flight|skipped-.+"`) {
		t.Errorf("residual must exclude exactly at-capacity, budget-exceeded, origin-unavailable, triager-failure, upstream-killed, upstream-unavailable, dispatch-retry, usage-limit, launch-failed, session-in-flight and skipped-*: %q", got)
	}
	// docket pg2-12gr4.8: launch-failed (a ccpool session that failed before it was
	// ready, a fault decline) is excluded in the rule's expr AND its description, and
	// the progress rule that pages a role that keeps failing this way is named.
	if !strings.Contains(r, `reason=launch-failed`) || !strings.Contains(r, "pg-router-role-stalled") {
		t.Error("residual annotation must name the launch-failed exclusion and the rule that covers it")
	}
	if n := strings.Count(r, "usage-limit|launch-failed|session-in-flight"); n != 2 {
		t.Errorf("the exclusion regex (with launch-failed) must appear in both the description and the expr, found %d", n)
	}
	// pg2-e0imq: the annotation names the new carve-out, and the rule's own
	// comment documents it, so the exclusion is never an unexplained matcher.
	if !strings.Contains(r, "reason=usage-limit") {
		t.Error("residual annotation must name the usage-limit exclusion")
	}
	for _, need := range []string{"for: 10m", "noDataState: OK", "execErrState: Error", "{{ $labels.role }}", "{{ $labels.class }}"} {
		if !strings.Contains(r, need) {
			t.Errorf("residual rule lost %q", need)
		}
	}
	// pg2-s3pjt: the handler declines an unreadable ccpool pool with its own
	// reason, pool-unreadable, precisely so it is NOT hidden by the
	// session-in-flight exclusion. Neither the matcher nor the rule text may
	// ever exclude it: a persistently unreadable pool must page.
	if strings.Contains(got, "pool-unreadable") {
		t.Errorf("residual must keep counting pool-unreadable, not exclude it: %q", got)
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

// pg2-e0imq: usage-limit is excluded from the residual and, unlike upstream-killed,
// has NO alert of its own (the operator ruled an external limit does not page; the
// series stays visible as pg_router_failures_total{reason="usage-limit"}). The
// only expr that mentions the reason is the residual's exclusion matcher.
func TestUsageLimitHasNoAlertOfItsOwn(t *testing.T) {
	b, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`(?m)^\s*expr: (.+)$`).FindAllStringSubmatch(string(b), -1) {
		if strings.Contains(m[1], "usage-limit") && m[1] != wantResidualExpr {
			t.Errorf("only the residual's exclusion may mention usage-limit, got expr %q", m[1])
		}
	}
	if strings.Contains(string(b), "uid: pg-router-usage-limit") {
		t.Error("usage-limit must not get a rule of its own")
	}
}

// pg2-6t91v7: the handler's moot precheck outcomes (skipped-bead-closed,
// skipped-pr-merged, skipped-bead-not-escalated) are accepted and completed as
// skipped, so they are no longer declines and never reach pg_router_failures_total.
// The residual still carries `skipped-.+` because the non-moot outcomes are still
// declines, and the comment block above the rule says which is which and where the
// moot ones are counted. A new skipped-* reason is excluded by the matcher either way.
func TestResidualSkippedExclusionCoversTheNonMootDeclines(t *testing.T) {
	m := regexp.MustCompile(`reason!~"([^"]+)"`).FindStringSubmatch(wantResidualExpr)
	if m == nil {
		t.Fatal("no reason!~ matcher in residual expr")
	}
	re := regexp.MustCompile("^(?:" + m[1] + ")$")
	for _, reason := range []string{
		// still declines (the work still exists; a completion would drop it)
		"skipped-pending-review", "skipped-bead-human", "skipped-bead-not-groomed",
		"skipped-bead-blocked", "skipped-bead-deferred", "skipped-bead-claimed", "skipped-bead-not-open",
		// moot: completed as skipped, never a failure series; still matched in case of a stale series
		"skipped-bead-closed", "skipped-pr-merged", "skipped-bead-not-escalated",
	} {
		if !re.MatchString(reason) {
			t.Errorf("residual must exclude %q", reason)
		}
	}
	// A connector outage is NOT moot and NOT excluded: it must keep paging.
	if re.MatchString("connector-unavailable") {
		t.Error("connector-unavailable must keep paging")
	}
	b, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "reason=skipped-* is EXCLUDED too")
	j := strings.Index(src, "- uid: pg-router-failure-rate\n")
	if i < 0 || j < i {
		t.Fatal("the failure-rate comment block lost its reason=skipped-* paragraph")
	}
	para := src[i:j]
	for _, need := range []string{
		"pg2-6t91v7", "pg_router_skipped_total", "skipped-bead-closed", "skipped-pr-merged",
		"skipped-bead-not-escalated", "skipped-pending-review", "skipped-bead-not-open", "pg_router_throughput_total",
	} {
		if !strings.Contains(para, need) {
			t.Errorf("the reason=skipped-* comment block must mention %q", need)
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
		"upstream-killed":            true,
		"budget-exceeded":            true,
		"triager-failure":            true,
		"origin-unavailable":         true,
		"at-capacity":                true,
		"skipped-pr-merged":          true,
		"usage-limit":                true, // pg2-e0imq: an external usage/spend limit or API outage
		"upstream-unavailable":       true, // pg2-8o8x9d: a router-retried class-unavailable handler error
		"dispatch-retry":             true, // pg2-f1d4mk: the class=declined hand-back of that re-run
		"dispatch-retry2":            false,
		"dispatch":                   false,
		"session-in-flight":          true, // pg2-qaeb8: routine backpressure
		"launch-failed":              true, // docket pg2-12gr4.8: covered by the role-progress rule
		"launch-failed2":             false,
		"launch":                     false,
		"low-disk":                   false,
		"command-busy":               false,
		"":                           false, // plain handler-error, dispatch-failure: keep paging
		"capacity-unknown":           false,
		"pool-unreadable":            false, // pg2-s3pjt: an unreadable pool is not backpressure; it must keep paging
		"upstream-killed2":           false,
		"usage-limit2":               false,
		"upstream-unavailable2":      false,
		"upstream-unavailable-extra": false,
		"unavailable":                false,
		"connector-unavailable":      false, // a busy decline the handler emits: a sustained outage pages
		"usage":                      false,
	} {
		if got := re.MatchString(reason); got != wantExcluded {
			t.Errorf("residual excludes reason %q = %v, want %v", reason, got, wantExcluded)
		}
	}
}

// pg2-8o8x9d (pg2-hk1sn option c): the reason the core attaches to a handler error
// classified class unavailable is excluded from the residual, the exclusion matches
// the metrics constant (not a retyped literal), and the reason has no alert of its
// own: a sustained PR-lane failure pages through pg-desk-pr-lane-failure-rate. The
// promtool fire / no-fire cases live in rule-tests/pg-router-failure-rate.test.yaml.
func TestResidualExcludesUpstreamUnavailable(t *testing.T) {
	if metrics.ReasonUpstreamUnavailable != "upstream-unavailable" {
		t.Fatalf("ReasonUpstreamUnavailable = %q, the rule pins upstream-unavailable", metrics.ReasonUpstreamUnavailable)
	}
	m := regexp.MustCompile(`reason!~"([^"]+)"`).FindStringSubmatch(ruleExpr(t, ruleBlock(t, "pg-router-failure-rate")))
	if m == nil {
		t.Fatal("no reason!~ matcher in the live residual expr")
	}
	re := regexp.MustCompile("^(?:" + m[1] + ")$")
	// No-fire: the reason is excluded from the residual.
	if !re.MatchString(metrics.ReasonUpstreamUnavailable) {
		t.Errorf("the residual must exclude reason %q", metrics.ReasonUpstreamUnavailable)
	}
	// Fire: every other reason the Emitter can attach keeps its disposition, and a
	// handler error with no reason (not class unavailable) keeps paging.
	for reason, wantExcluded := range map[string]bool{
		metrics.ReasonTriagerFailure: true,
		metrics.ReasonBudgetExceeded: true,
		metrics.ReasonUpstreamKilled: true,
		metrics.ReasonUsageLimit:     true,
		"":                           false,
		"unavailable":                false,
		"connector-unavailable":      false,
		"upstream-unavailable-retry": false,
		"pre-upstream-unavailable":   false,
	} {
		if got := re.MatchString(reason); got != wantExcluded {
			t.Errorf("residual excludes reason %q = %v, want %v", reason, got, wantExcluded)
		}
	}
	// No alert of its own: the only expr naming the reason is the residual's matcher.
	b, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range regexp.MustCompile(`(?m)^\s*expr: (.+)$`).FindAllStringSubmatch(string(b), -1) {
		if strings.Contains(e[1], "upstream-unavailable") && e[1] != wantResidualExpr {
			t.Errorf("only the residual's exclusion may mention upstream-unavailable, got expr %q", e[1])
		}
	}
	if strings.Contains(string(b), "uid: pg-router-upstream-unavailable") {
		t.Error("upstream-unavailable must not get a rule of its own")
	}
	// The rule's description names the exclusion and the rule that still pages.
	r := ruleBlock(t, "pg-router-failure-rate")
	if !strings.Contains(r, "reason=upstream-unavailable") || !strings.Contains(r, "pg-desk-pr-lane-failure-rate") {
		t.Error("residual annotation must name the upstream-unavailable exclusion and pg-desk-pr-lane-failure-rate")
	}
}

// pg2-f1d4mk (completes pg2-8o8x9d): the router hands a re-run attempt back to the
// queue as class=declined reason=dispatch-retry (DEC-RETRY-2). The failed attempt is
// already excluded as upstream-unavailable, so the hand-back is excluded with it, or a
// retried-and-recovered failure still pages. Only that exact reason is excluded: every
// other declined reason (command-busy, capacity-unknown) keeps paging. The exclusion
// matches the orchestrator's constant, not a retyped literal. The promtool fire /
// no-fire cases live in rule-tests/pg-router-failure-rate.test.yaml.
func TestResidualExcludesDispatchRetry(t *testing.T) {
	if orchestrator.DeclineDetailDispatchRetry != "dispatch-retry" {
		t.Fatalf("DeclineDetailDispatchRetry = %q, the rule pins dispatch-retry", orchestrator.DeclineDetailDispatchRetry)
	}
	m := regexp.MustCompile(`reason!~"([^"]+)"`).FindStringSubmatch(ruleExpr(t, ruleBlock(t, "pg-router-failure-rate")))
	if m == nil {
		t.Fatal("no reason!~ matcher in the live residual expr")
	}
	re := regexp.MustCompile("^(?:" + m[1] + ")$")
	if !re.MatchString(orchestrator.DeclineDetailDispatchRetry) {
		t.Errorf("the residual must exclude reason %q", orchestrator.DeclineDetailDispatchRetry)
	}
	for reason, wantExcluded := range map[string]bool{
		"command-busy":       false,
		"capacity-unknown":   false,
		"dispatch-retry2":    false,
		"pre-dispatch-retry": false,
		"dispatch":           false,
		"retry":              false,
		"":                   false,
	} {
		if got := re.MatchString(reason); got != wantExcluded {
			t.Errorf("residual excludes reason %q = %v, want %v", reason, got, wantExcluded)
		}
	}
	// No alert of its own: only the residual's matcher may name the reason.
	b, err := os.ReadFile("../../grafana/alerting/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range regexp.MustCompile(`(?m)^\s*expr: (.+)$`).FindAllStringSubmatch(string(b), -1) {
		if strings.Contains(e[1], "dispatch-retry") && e[1] != wantResidualExpr {
			t.Errorf("only the residual's exclusion may mention dispatch-retry, got expr %q", e[1])
		}
	}
	r := ruleBlock(t, "pg-router-failure-rate")
	if !strings.Contains(r, "reason=dispatch-retry") || !strings.Contains(r, "pg-desk-pr-lane-failure-rate") {
		t.Error("residual annotation must name the dispatch-retry exclusion and pg-desk-pr-lane-failure-rate")
	}
}
