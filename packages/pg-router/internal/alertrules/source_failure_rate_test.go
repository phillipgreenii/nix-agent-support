package alertrules

import (
	"strings"
	"testing"
)

// wantSourceFailureExpr is the failure RATIO over 1h (failures / attempts),
// guarded by a minimum of 3 failures. The denominator,
// pg_router_source_duration_seconds_count, is recorded for every attempt that
// ran to its own end, success or failure, so the quotient is a true ratio. The
// rule fires above 0.5 (threshold node C), sustained for 15m. It replaced the
// earlier `rate(...[10m]) > 0` for 10m, which flapped on every transient
// upstream 502/504 (operator ruling, Phillip, 2026-10-08).
const wantSourceFailureExpr = `sum by (source) (increase(pg_router_source_failures_total[1h])) / sum by (source) (increase(pg_router_source_duration_seconds_count[1h])) and on(source) sum by (source) (increase(pg_router_source_failures_total[1h])) >= 3`

// pg-router-source-failure-rate keeps noDataState: OK on purpose (pg2-vicjc):
// the source-failures counter is created lazily, so an absent series is the
// normal healthy state and NoData/Alerting would fire permanently. Daemon
// death is pg-router-liveness-down's job. This pins the decision (and the
// exported _total series name, pg2-jgbnp) so a change has to be deliberate.
func TestSourceFailureRateRule(t *testing.T) {
	r := ruleBlock(t, "pg-router-source-failure-rate")
	if got := ruleExpr(t, r); got != wantSourceFailureExpr {
		t.Errorf("source-failure-rate expr:\n got %q\nwant %q", got, wantSourceFailureExpr)
	}
	for _, need := range []string{
		"title: pg-router event source failure ratio is high (by source)",
		"for: 15m", "noDataState: OK", "execErrState: Error", "severity: warning", "{{ $labels.source }}",
		// the 1h window needs a 1h query range, and the ratio threshold is 0.5
		"relativeTimeRange: { from: 3600, to: 0 }", "params: [0.5]",
	} {
		if !strings.Contains(r, need) {
			t.Errorf("source-failure-rate rule lost %q", need)
		}
	}
}

const wantSourceBackoffExpr = `sum by (source) (increase(pg_router_source_backoff_total[1h])) >= 3`

// pg-router-source-backoff (bead pg2-bbz1v; operator ruling, Phillip, 2026-10-29: "(b),
// except a warning level alert should happen"): a backend's deliberate reserve-guard
// back-off is counted on its own counter, warns when sustained (3 back-offs in 1h, for
// 30m), and is NOT a producer failure, so neither failure alert reads it and the rule
// itself reads only the back-off counter.
func TestSourceBackoffRule(t *testing.T) {
	r := ruleBlock(t, "pg-router-source-backoff")
	expr := ruleExpr(t, r)
	if expr != wantSourceBackoffExpr {
		t.Errorf("source-backoff expr:\n got %q\nwant %q", expr, wantSourceBackoffExpr)
	}
	if strings.Contains(expr, "source_failures") {
		t.Errorf("source-backoff must not read the failure counter: %q", expr)
	}
	for _, need := range []string{
		"for: 30m", "noDataState: OK", "execErrState: Error", "severity: warning", "{{ $labels.source }}",
		"relativeTimeRange: { from: 3600, to: 0 }", "params: [0]",
	} {
		if !strings.Contains(r, need) {
			t.Errorf("source-backoff rule lost %q", need)
		}
	}
	// Not a failure/page rule: warning only, never critical.
	if strings.Contains(r, "severity: critical") {
		t.Error("source-backoff must be warning severity, not critical")
	}
	// Neither failure alert references the back-off counter, and the failure-rate
	// rule still reads only the failure counter, which a back-off never increments.
	for _, uid := range []string{"pg-router-source-failure-rate", "pg-router-source-persistent-failure"} {
		if strings.Contains(ruleExpr(t, ruleBlock(t, uid)), "backoff") {
			t.Errorf("%s must not read the back-off counter", uid)
		}
	}
}

func TestLivenessRuleCoversDeadDaemon(t *testing.T) {
	r := ruleBlock(t, "pg-router-liveness-down")
	// pg2-787q92 (operator ruling 2026-10-29): a sleeping host is not a down service, so the
	// rule is noDataState: OK and value-based. A dead daemon still alerts through the
	// `liveness < 1 or up == 0` clause while the host is awake; the PromQL cases live in
	// grafana/alerting/rule-tests/pg-router-liveness-down.test.yaml. pg-router-source-failure-rate
	// keeps noDataState: OK on the strength of that value-based coverage plus launchd-health.
	if !strings.Contains(r, "noDataState: OK") || strings.Contains(r, "noDataState: Alerting") {
		t.Error("pg-router-liveness-down must be noDataState: OK (a sleeping host must not page)")
	}
	for _, need := range []string{`pg_router_liveness < 1`, `up{job="pg-router"} == 0`, `host_power_last_wake_timestamp_seconds{kind="full"}`, `host_power_last_sleep_timestamp_seconds`} {
		if !strings.Contains(r, need) {
			t.Errorf("pg-router-liveness-down lost %q from its value-based, host-awake-gated query", need)
		}
	}
}

const wantLogLimitExpr = `max(pg_router_emitters_halted) > 0 or sum(increase(pg_router_enqueue_rejected_total[5m])) > 0`

// pg-router-log-limit (pg2-5d3ui) fires when the emitters are halted by the
// event-log size limit OR any event is rejected, only after 10 minutes so a
// compaction blip does not page, and carries the remedy. noDataState: OK because
// the rejected counter is lazy (an absent series is healthy); a dead daemon is
// pg-router-liveness-down's job. This pins the decisions, the exported _total
// series name, and the remedy text so a change has to be deliberate.
func TestLogLimitRule(t *testing.T) {
	r := ruleBlock(t, "pg-router-log-limit")
	if got := ruleExpr(t, r); got != wantLogLimitExpr {
		t.Errorf("log-limit expr:\n got %q\nwant %q", got, wantLogLimitExpr)
	}
	for _, need := range []string{
		"for: 10m", "noDataState: OK", "execErrState: Error", "severity: warning", "params: [0]",
		// the remedies, in the annotation
		"wait for queued events to expire", "pg-router log compact", "restart pg-router) to compact now", "PG_ROUTER_MAX_LOG_BYTES", "move queue.jsonl aside",
	} {
		if !strings.Contains(r, need) {
			t.Errorf("log-limit rule lost %q", need)
		}
	}
}
