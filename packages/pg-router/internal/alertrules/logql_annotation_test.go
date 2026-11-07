package alertrules

import (
	"strings"
	"testing"
)

// pg-router-probe runs a rule's "logql" annotation against Loki to put recent log
// lines in the bead it files. pg-router's own WARN/ERROR operational lines reach
// Loki as {service_name="pg-router"} (README, "Logs"); the OTLP stream is the only
// stream of the daemon that is not the dispatch-outcome ledger
// (service_name="pg-router-events").
func TestLogqlAnnotationOnLogDiagnosedRules(t *testing.T) {
	for _, uid := range []string{"pg-router-queue-stalled", "pg-router-pool-full-idle", "pg-router-role-stalled"} {
		r := ruleBlock(t, uid)
		i := strings.Index(r, "annotations:")
		j := strings.Index(r, "data:")
		if i < 0 || j < i {
			t.Fatalf("%s: annotations block not found", uid)
		}
		ann := r[i:j]
		if !strings.Contains(ann, "          logql: '{service_name=\"pg-router\"}'\n") {
			t.Errorf("%s: annotations lack the logql selector for the pg-router log stream: %q", uid, ann)
		}
	}
}
