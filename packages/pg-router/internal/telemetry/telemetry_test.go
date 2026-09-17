package telemetry

import (
	"context"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	logglobal "go.opentelemetry.io/otel/log/global"
)

// TestInit_NoEndpoint_InstallsNoopLoggerProvider verifies that pg-router
// starts cleanly when no OTLP endpoint is configured. The behaviour
// contract is: no error, no-op LoggerProvider installed.
func TestInit_NoEndpoint_InstallsNoopLoggerProvider(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	shutdown, err := Init(context.Background(), "pg-router-test", "v0.0.0")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
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
}

// TestInit_BadEndpoint_FallsBackToNoop verifies that a misconfigured
// endpoint downgrades gracefully rather than returning an error. The OTLP
// gRPC exporter client validates addresses lazily, so real network dial may
// be deferred to send-time; either way Init MUST NOT return an error and
// shutdown MUST complete promptly when its context is already canceled.
func TestInit_BadEndpoint_FallsBackToNoop(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1") // unreachable
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")

	shutdown, err := Init(context.Background(), "pg-router-test", "v0.0.0")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Init returned nil shutdown")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = shutdown(ctx)
}

// TestEnvOr exercises the small fallback helper.
func TestEnvOr(t *testing.T) {
	t.Setenv("PG_ROUTER_TEST_VAR", "")
	if got := envOr("PG_ROUTER_TEST_VAR", "fallback"); got != "fallback" {
		t.Fatalf("envOr empty: got %q want fallback", got)
	}
	t.Setenv("PG_ROUTER_TEST_VAR", "  set-value  ")
	if got := envOr("PG_ROUTER_TEST_VAR", "fallback"); got != "set-value" {
		t.Fatalf("envOr set: got %q want set-value", got)
	}
}

func TestNewSlogHandler_NoProvider_NoError(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown, err := Init(context.Background(), "pg-router-test", "v0.0.0")
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = shutdown(context.Background()) }()

	h := NewSlogHandler()
	if h == nil {
		t.Fatal("NewSlogHandler returned nil")
	}
	slog.New(h).Warn("probe", "k", "v")
}
