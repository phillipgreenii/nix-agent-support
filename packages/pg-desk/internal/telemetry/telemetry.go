// Package telemetry initializes OpenTelemetry OTLP log export for
// pg-desk's serve subcommand (design section 9, replacing D24's earlier
// "nothing over OpenTelemetry" declaration for serve).
//
// Scoped to logs only: `run`/`run issue` already emit structured JSON to
// stderr and are untouched by this package — only serve's WARN/ERROR
// operational lines are promoted to OTLP, and only serve calls Init.
// pg-desk's real Prometheus metrics catalog lives in the sibling
// internal/metrics package, built the same OTel-metrics-API way
// pg-router's own catalog is. Traces are out of scope for pg-desk
// entirely, so — unlike pg-pr's own internal/telemetry, which this
// package otherwise mirrors in shape (Init / NewSlogHandler / Fanout) —
// there is no tracer provider setup and no exported Tracer here.
//
// Initialization is best-effort: when no OTLP endpoint is configured, or
// the configured endpoint is unreachable, Init installs a no-op
// LoggerProvider and returns no error. This way pg-desk serve never
// refuses to start because the local OTel collector is down.
//
// The exporter honors the standard OTEL_* env vars, matching pg-pr's own
// convention:
//
//	OTEL_EXPORTER_OTLP_ENDPOINT   (default: not set -> no-op)
//	OTEL_EXPORTER_OTLP_PROTOCOL   (default: http/protobuf; "grpc" switches)
//	OTEL_SERVICE_NAME             (overrides the serviceName arg; serve's
//	                               darwin module sets this to
//	                               "pg-desk-serve")
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

// scopeName is the instrumentation scope name passed to
// otelslog.NewHandler (see slog.go). It does not set service_name — that
// comes from the resource (OTEL_SERVICE_NAME) Init configures below.
const scopeName = "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk"

// ShutdownFunc flushes any buffered telemetry and releases exporter
// resources. Safe to call even when Init installed a no-op provider.
type ShutdownFunc func(context.Context) error

// Init configures the global OTel LoggerProvider.
//
// Behaviour:
//
//   - If OTEL_EXPORTER_OTLP_ENDPOINT is empty, Init installs a no-op
//     provider and returns a no-op shutdown. No error.
//   - If the log exporter fails to initialise, Init logs one stderr
//     warning, installs a no-op provider, and continues. No error.
//   - On success, Init installs a batching SDK provider and returns its
//     Shutdown.
//
// serviceName is used when OTEL_SERVICE_NAME is unset.
func Init(ctx context.Context, serviceName, version string) (ShutdownFunc, error) {
	noopShutdown := func(context.Context) error { return nil }

	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if endpoint == "" {
		// No collector configured — install a no-op provider so callers
		// can use the global LoggerProvider without nil worries.
		logglobal.SetLoggerProvider(lognoop.NewLoggerProvider())
		return noopShutdown, nil
	}

	res := buildResource(ctx, serviceName, version)

	logExp, err := newOTLPLogExporter(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"pg-desk: OTel log exporter init failed (%v); logs will be no-op\n", err)
		logglobal.SetLoggerProvider(lognoop.NewLoggerProvider())
		return noopShutdown, nil
	}

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
// OTEL_EXPORTER_OTLP_PROTOCOL (default "http/protobuf"); "grpc" switches to
// the gRPC transport. Endpoint and TLS are honored from env vars by the
// underlying exporter packages — we set no overrides here.
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
