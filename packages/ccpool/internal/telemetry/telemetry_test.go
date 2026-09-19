package telemetry

import (
	"context"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	logglobal "go.opentelemetry.io/otel/log/global"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// TestInit_NoEndpoint_InstallsNoopLoggerProvider verifies that ccpool starts
// cleanly when no OTLP endpoint is configured: a usable (no-op) global
// LoggerProvider and a usable (no-op) MeterProvider, mirroring pg-desk's
// telemetry_test.go.
func TestInit_NoEndpoint_InstallsNoopLoggerProvider(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	shutdown := Init(context.Background())
	if shutdown == nil {
		t.Fatal("Init returned nil shutdown")
	}
	defer func() { _ = shutdown(context.Background()) }()

	lp := logglobal.GetLoggerProvider()
	if lp == nil {
		t.Fatal("nil global logger provider")
	}
	lg := lp.Logger("probe")
	var rec otellog.Record
	rec.SetBody(attribute.StringValue("probe"))
	lg.Emit(context.Background(), rec)

	if mp := MeterProvider(); mp == nil {
		t.Fatal("nil MeterProvider")
	}

	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// TestInit_BadEndpoint_FallsBackToNoop verifies that a misconfigured
// endpoint downgrades gracefully rather than failing/blocking Init outright,
// mirroring pg-desk's telemetry_test.go.
func TestInit_BadEndpoint_FallsBackToNoop(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1") // unreachable
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")

	shutdown := Init(context.Background())
	if shutdown == nil {
		t.Fatal("Init returned nil shutdown")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = shutdown(ctx)

	if mp := MeterProvider(); mp == nil {
		t.Fatal("nil MeterProvider")
	}
}

// TestBuildResource_ServiceNameAlwaysHardcoded confirms the built resource's
// service.name is always "ccpool" even when OTEL_SERVICE_NAME is set in the
// process environment — the unit-testable half of the design's
// inheritance-resistance check (ccpool never honors an inherited
// OTEL_SERVICE_NAME, unlike the pg-router/pg-desk telemetry precedent).
func TestBuildResource_ServiceNameAlwaysHardcoded(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "pg-router")

	res := buildResource(context.Background())
	got, ok := res.Set().Value(semconv.ServiceNameKey)
	if !ok {
		t.Fatal("service.name attribute missing from resource")
	}
	if got.AsString() != serviceNameValue {
		t.Fatalf("service.name = %q, want %q", got.AsString(), serviceNameValue)
	}
}

// TestMeterProvider_NoInit_ReturnsUsableNoop verifies the package-level
// MeterProvider accessor never returns nil, even before Init has run —
// a sibling package calling telemetry.MeterProvider().Meter("...") must
// always get a usable (if inert) provider.
func TestMeterProvider_NoInit_ReturnsUsableNoop(t *testing.T) {
	mp := MeterProvider()
	if mp == nil {
		t.Fatal("MeterProvider() returned nil")
	}
	if _, err := mp.Meter("probe").Int64Counter("probe.count"); err != nil {
		t.Fatalf("Meter/Int64Counter: %v", err)
	}
}

// TestNewSlogHandler_NoProvider_NoError verifies the otelslog bridge handler
// is safe to build and use even when the global LoggerProvider is the no-op
// one, mirroring pg-desk's telemetry_test.go.
func TestNewSlogHandler_NoProvider_NoError(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown := Init(context.Background())
	defer func() { _ = shutdown(context.Background()) }()

	h := NewSlogHandler()
	if h == nil {
		t.Fatal("NewSlogHandler returned nil")
	}
	slog.New(h).Warn("probe", "k", "v")
}
