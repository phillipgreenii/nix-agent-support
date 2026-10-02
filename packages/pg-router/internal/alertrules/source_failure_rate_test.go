package alertrules

import (
	"strings"
	"testing"
)

const wantSourceFailureExpr = `sum by (source) (rate(pg_router_source_failures_total[10m]))`

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
	for _, need := range []string{"for: 10m", "noDataState: OK", "execErrState: Error", "severity: warning", "{{ $labels.source }}"} {
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
