package otel

import otellog "go.opentelemetry.io/otel/log"

// severityForEvent maps an OTLP log event_name to its severity. Most daemon
// events are informational; the delivery-failure events are error-like and MUST
// be WARN so they are distinguishable from normal traffic in the log stream
// (they previously all emitted at INFO). Keep this list small and explicit —
// a new failure event is opted in here, not by default.
//
// beads.stale_export_found (pg2-zjopv) is deliberately ERROR, one level above
// the WARN delivery-failure events: a stray issues.jsonl sitting in a .beads
// dir is a live data-corruption footgun (a stray `bd import` or a future
// auto-import path could silently restore whole prior rows over current
// state), not merely a degraded-delivery condition — it needs to stand out in
// a severity-filtered alert view as more urgent than a send failure.
func severityForEvent(name string) otellog.Severity {
	switch name {
	case "nudge.send_failed", "bridge.deliver_failed":
		return otellog.SeverityWarn
	case "beads.stale_export_found":
		return otellog.SeverityError
	default:
		return otellog.SeverityInfo
	}
}
