package otel

import (
	"testing"

	otellog "go.opentelemetry.io/otel/log"
)

// Error-like log events (a failed nudge delivery, a failed cmux injection) were
// emitted at INFO, so they were indistinguishable from normal traffic in the
// log stream. They MUST be WARN so operators can filter them.
func TestSeverityForEvent(t *testing.T) {
	warn := []string{"nudge.send_failed", "bridge.deliver_failed"}
	for _, n := range warn {
		if got := severityForEvent(n); got != otellog.SeverityWarn {
			t.Errorf("severityForEvent(%q) = %v, want Warn", n, got)
		}
	}
	info := []string{"nudge.sent", "daemon.heartbeat", "daemon.started", "block.limit_hit", ""}
	for _, n := range info {
		if got := severityForEvent(n); got != otellog.SeverityInfo {
			t.Errorf("severityForEvent(%q) = %v, want Info", n, got)
		}
	}
}
