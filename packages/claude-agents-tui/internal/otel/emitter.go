// Package otel owns OpenTelemetry SDK initialisation and emission helpers
// for pa-monitor. The disabled-state contract: when OTEL_EXPORTER_OTLP_ENDPOINT
// is unset, New returns (nil, nil) and every method on a *Emitter accepts a
// nil receiver and does nothing. Callers do not branch on whether OTel is on.
package otel

import (
	"context"
	"os"
)

// Options configures the emitter.
type Options struct {
	ServiceName    string
	ServiceVersion string
}

// Emitter holds the OTel SDK handles. A nil *Emitter is a valid no-op
// emitter; do not de-reference fields without nil checks.
type Emitter struct {
	// Real fields land in the metrics + log tasks. Kept empty here so the
	// type exists and the nil-safe contract is testable.
}

// New constructs an Emitter if OTEL_EXPORTER_OTLP_ENDPOINT is set in the
// environment, otherwise returns (nil, nil).
func New(ctx context.Context, opts Options) (*Emitter, error) {
	_ = opts
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return nil, nil
	}
	// Real SDK initialisation lands in Task 3.
	return &Emitter{}, nil
}

// Shutdown flushes exporters. nil-safe.
func (e *Emitter) Shutdown(ctx context.Context) error {
	_ = ctx
	if e == nil {
		return nil
	}
	return nil
}

// RecordSessionsCount sets per-state session gauges. nil-safe.
func (e *Emitter) RecordSessionsCount(byState map[string]int, baseAttrs map[string]string) {
	if e == nil {
		return
	}
	_ = byState
	_ = baseAttrs
}

// RecordCaffeinateActive sets the caffeinate gauge. nil-safe.
func (e *Emitter) RecordCaffeinateActive(active bool, attrs map[string]string) {
	if e == nil {
		return
	}
	_ = active
	_ = attrs
}

// LogEvent emits one log record at info level. nil-safe.
func (e *Emitter) LogEvent(name string, attrs map[string]string) {
	if e == nil {
		return
	}
	_ = name
	_ = attrs
}
