// Package telemetry initializes OpenTelemetry OTLP log export for
// pg-router's own operational slog records.
//
// Initialization is best-effort: when no OTLP endpoint is configured, or
// when the configured endpoint is unreachable, Init installs a no-op
// LoggerProvider and logs a single one-line warning to stderr. This way
// pg-router never refuses to start because a local OTel collector is down.
//
// This package is a deliberate small duplication of
// packages/pg-pr/internal/telemetry's Init/NewSlogHandler/Fanout shape:
// `internal/` packages cannot be imported across the pg-pr/pg-router module
// boundary, so the proven pattern is copied rather than shared. Only the LOG
// half is copied here — pg-router emits no spans, so there is no trace
// provider to install (traces were not requested for pg-router). Metrics use
// a separate, direct-scrape OTel Prometheus exporter wired in cmd/pg-router
// (internal/metrics), never this package's OTLP push path.
//
// The exporter honors the standard OTEL_* env vars per the workspace
// convention documented in
// phillipgreenii-nix-support-apps/docs/otel-emitter-onboarding.md:
//
//	OTEL_EXPORTER_OTLP_ENDPOINT   (default: not set -> no-op)
//	OTEL_EXPORTER_OTLP_PROTOCOL   (default: http/protobuf; "grpc" switches)
//	OTEL_SERVICE_NAME             (overrides the serviceName arg)
//	OTEL_RESOURCE_ATTRIBUTES      (merged via the SDK resource detector)
package telemetry

import (
	"context"
	"fmt"
	"os"
	"strings"

	otlploggrpc "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	otlploghttp "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	logglobal "go.opentelemetry.io/otel/log/global"
	lognoop "go.opentelemetry.io/otel/log/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// scopeName is the OTel instrumentation-scope name NewSlogHandler binds its
// otelslog bridge to. Mirrors pg-pr's own TracerName constant in spirit
// (same value shape — the instrumentation library's Go import path) but
// renamed: pg-router installs no tracer, so calling it "TracerName" here
// would misdescribe what it is used for.
const scopeName = "github.com/phillipgreenii/pg-router"

// ShutdownFunc flushes any buffered telemetry and releases exporter
// resources. Safe to call even when Init installed a no-op provider.
type ShutdownFunc func(context.Context) error

// Init configures the global OTel LoggerProvider.
//
// Behaviour:
//
//   - If OTEL_EXPORTER_OTLP_ENDPOINT is empty, Init installs a no-op
//     LoggerProvider and returns a no-op shutdown. No error.
//   - If the log exporter fails to initialise, Init logs one stderr
//     warning, installs a no-op LoggerProvider, and continues. No error.
//   - On success, Init installs a batching SDK LoggerProvider and returns
//     its Shutdown.
//
// serviceName is used when OTEL_SERVICE_NAME is unset.
func Init(ctx context.Context, serviceName, version string) (ShutdownFunc, error) {
	noopShutdown := func(context.Context) error { return nil }

	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if endpoint == "" {
		// No collector configured — install a no-op provider so callers can
		// use the global LoggerProvider without nil worries.
		logglobal.SetLoggerProvider(lognoop.NewLoggerProvider())
		return noopShutdown, nil
	}

	logExp, err := newOTLPLogExporter(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"pg-router: OTel log exporter init failed (%v); logs will be no-op\n", err)
		logglobal.SetLoggerProvider(lognoop.NewLoggerProvider())
		return noopShutdown, nil
	}

	res := buildResource(ctx, serviceName, version)
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
		sdklog.WithResource(res),
	)
	logglobal.SetLoggerProvider(lp)
	return lp.Shutdown, nil
}

// buildResource constructs the shared OTel resource (service.name/version +
// env + runtime attrs), degrading to a schemaless resource if detection
// fails. service.name comes from OTEL_SERVICE_NAME, else serviceName.
func buildResource(ctx context.Context, serviceName, version string) *resource.Resource {
	res, err := resource.New(
		ctx,
		resource.WithAttributes(
			semconv.ServiceName(envOr("OTEL_SERVICE_NAME", serviceName)),
			semconv.ServiceVersion(version),
		),
		resource.WithFromEnv(), // OTEL_RESOURCE_ATTRIBUTES
		resource.WithProcessRuntimeName(),
		resource.WithProcessRuntimeVersion(),
	)
	if err != nil {
		return resource.NewSchemaless(
			semconv.ServiceName(envOr("OTEL_SERVICE_NAME", serviceName)),
			semconv.ServiceVersion(version),
		)
	}
	return res
}

// newOTLPLogExporter builds the OTLP log exporter. Protocol is picked from
// OTEL_EXPORTER_OTLP_PROTOCOL (default "http/protobuf" matches the workspace
// OTel emitter onboarding doc); "grpc" switches to the gRPC transport.
// Endpoint and TLS details are honored from env vars by the underlying
// exporter packages — no overrides are set here.
func newOTLPLogExporter(ctx context.Context) (sdklog.Exporter, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"))) {
	case "grpc":
		return otlploggrpc.New(ctx)
	default:
		return otlploghttp.New(ctx)
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
