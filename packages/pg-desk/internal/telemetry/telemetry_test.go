package telemetry

import (
	"context"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	logglobal "go.opentelemetry.io/otel/log/global"
)

// TestInit_NoEndpoint_InstallsNoopLoggerProvider verifies that pg-desk
// serve starts cleanly when no OTLP endpoint is configured: no error, a
// usable (no-op) global LoggerProvider.
func TestInit_NoEndpoint_InstallsNoopLoggerProvider(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	shutdown, err := Init(context.Background(), "pg-desk-serve-test", "v0.0.0")
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

	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// TestInit_BadEndpoint_FallsBackToNoop verifies that a misconfigured
// endpoint downgrades gracefully rather than failing Init outright.
func TestInit_BadEndpoint_FallsBackToNoop(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1") // unreachable
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")

	shutdown, err := Init(context.Background(), "pg-desk-serve-test", "v0.0.0")
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

func TestNewSlogHandler_NoProvider_NoError(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	if _, err := Init(context.Background(), "pg-desk-serve-test", "v0.0.0"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	h := NewSlogHandler()
	if h == nil {
		t.Fatal("NewSlogHandler returned nil")
	}
	slog.New(h).Warn("probe", "k", "v")
}

func TestEnvOr(t *testing.T) {
	t.Setenv("PG_DESK_TEST_VAR", "")
	if got := envOr("PG_DESK_TEST_VAR", "fallback"); got != "fallback" {
		t.Fatalf("envOr empty: got %q want fallback", got)
	}
	t.Setenv("PG_DESK_TEST_VAR", "  set-value  ")
	if got := envOr("PG_DESK_TEST_VAR", "fallback"); got != "set-value" {
		t.Fatalf("envOr set: got %q want set-value", got)
	}
}
