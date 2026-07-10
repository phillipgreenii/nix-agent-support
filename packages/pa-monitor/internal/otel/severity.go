package otel

import otellog "go.opentelemetry.io/otel/log"

// severityForEvent maps an OTLP log event_name to its severity. Most daemon
// events are informational; the delivery-failure events are error-like and MUST
// be WARN so they are distinguishable from normal traffic in the log stream
// (they previously all emitted at INFO). Keep this list small and explicit —
// a new failure event is opted in here, not by default.
func severityForEvent(name string) otellog.Severity {
	switch name {
	case "nudge.send_failed", "bridge.deliver_failed":
		return otellog.SeverityWarn
	default:
		return otellog.SeverityInfo
	}
}
