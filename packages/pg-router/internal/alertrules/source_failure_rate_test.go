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

func TestLivenessRuleCoversDeadDaemon(t *testing.T) {
	r := ruleBlock(t, "pg-router-liveness-down")
	if !strings.Contains(r, "noDataState: Alerting") {
		t.Error("pg-router-liveness-down must keep noDataState: Alerting; it is the liveness backstop that lets pg-router-source-failure-rate keep noDataState: OK")
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
