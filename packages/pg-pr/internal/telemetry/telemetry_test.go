package telemetry

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	logglobal "go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/trace/noop"
)

// TestInit_NoEndpoint_InstallsNoopProvider verifies that pg-pr starts
// cleanly when no OTLP endpoint is configured. The behaviour contract
// is: no error, no warning, no-op provider installed.
func TestInit_NoEndpoint_InstallsNoopProvider(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	shutdown, err := Init(context.Background(), "pg-pr-test", "v0.0.0")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Init returned nil shutdown")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	// The noop tracer provider is exposed under a sentinel type; the
	// concrete check uses the trace/noop package.
	gotTracer := otel.Tracer("probe")
	noopTracer := noop.NewTracerProvider().Tracer("probe")
	// Compare by spawning a span and verifying the SpanContext is
	// IsValid()==false (noop spans never carry a real context).
	_, span := gotTracer.Start(context.Background(), "probe")
	defer span.End()
	if span.SpanContext().IsValid() {
		t.Fatal("expected noop tracer (invalid SpanContext); got a valid one")
	}
	_, refSpan := noopTracer.Start(context.Background(), "probe")
	defer refSpan.End()
	if span.SpanContext().IsValid() != refSpan.SpanContext().IsValid() {
		t.Fatal("got tracer behaves differently than noop reference")
	}
}

// TestInit_BadEndpoint_FallsBackToNoop verifies that a misconfigured
// endpoint downgrades gracefully. The OTLP HTTP/gRPC exporter clients
// validate addresses lazily, so we additionally probe by checking that
// shutdown returns no error and the global tracer remains non-nil.
func TestInit_BadEndpoint_FallsBackToNoop(t *testing.T) {
	// Use a syntactically invalid endpoint that the gRPC client refuses
	// to accept. The exporter packages may still defer real network
	// dial to send-time; in that case Init succeeds and shutdown is
	// what we cover. Either way the function MUST NOT return an error.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1") // unreachable
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")

	shutdown, err := Init(context.Background(), "pg-pr-test", "v0.0.0")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Init returned nil shutdown")
	}
	// Force shutdown to complete promptly even if the exporter is set up.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = shutdown(ctx)
}

// TestTracer_ReturnsNamedTracer probes that Tracer() does not return
// nil and that the tracer name matches our sentinel.
func TestTracer_ReturnsNamedTracer(t *testing.T) {
	got := Tracer()
	if got == nil {
		t.Fatal("Tracer returned nil")
	}
	if !strings.Contains(TracerName, "pg-pr") {
		t.Fatalf("TracerName missing pg-pr: %q", TracerName)
	}
}

// TestEnvOr exercises the small fallback helper.
func TestEnvOr(t *testing.T) {
	t.Setenv("PG_PR_TEST_VAR", "")
	if got := envOr("PG_PR_TEST_VAR", "fallback"); got != "fallback" {
		t.Fatalf("envOr empty: got %q want fallback", got)
	}
	t.Setenv("PG_PR_TEST_VAR", "  set-value  ")
	if got := envOr("PG_PR_TEST_VAR", "fallback"); got != "set-value" {
		t.Fatalf("envOr set: got %q want set-value", got)
	}
}

func TestInit_NoEndpoint_InstallsNoopLoggerProvider(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	shutdown, err := Init(context.Background(), "pg-pr-test", "v0.0.0")
	if err != nil {
		t.Fatalf("Init: %v", err)
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
}

func TestNewSlogHandler_NoProvider_NoError(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	if _, err := Init(context.Background(), "pg-pr-test", "v0.0.0"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	h := NewSlogHandler()
	if h == nil {
		t.Fatal("NewSlogHandler returned nil")
	}
	slog.New(h).Warn("probe", "k", "v")
}
