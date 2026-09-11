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

// TestSeverityForEvent_BeadsStaleExportIsError: pg2-zjopv's top-level alert
// must be ERROR, one level above the WARN delivery-failure events — a stray
// issues.jsonl is a data-corruption footgun, not merely a degraded delivery.
func TestSeverityForEvent_BeadsStaleExportIsError(t *testing.T) {
	if got := severityForEvent("beads.stale_export_found"); got != otellog.SeverityError {
		t.Errorf("severityForEvent(%q) = %v, want Error", "beads.stale_export_found", got)
	}
}
