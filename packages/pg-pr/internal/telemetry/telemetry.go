// Package telemetry initializes OpenTelemetry tracing and Prometheus
// metrics for pg-pr.
//
// Initialization is best-effort: when no OTLP endpoint is configured, or
// when the configured endpoint is unreachable, telemetry.Init installs a
// no-op tracer provider and logs a single one-line warning to stderr.
// This way pg-pr never refuses to start because the local OTel
// collector is down.
//
// The exporter honors the standard OTEL_* env vars per the workspace
// convention documented in
// phillipgreenii-nix-support-apps/docs/otel-emitter-onboarding.md:
//
//	OTEL_EXPORTER_OTLP_ENDPOINT   (default: not set -> no-op)
//	OTEL_EXPORTER_OTLP_PROTOCOL   (default: http/protobuf; "grpc" switches)
//	OTEL_SERVICE_NAME             (overrides the serviceName arg)
//	OTEL_RESOURCE_ATTRIBUTES      (merged via the SDK resource detector)
//
// Span boundary policy (per pg-pr design spec
// docs/superpowers/specs/2026-05-19-pg-pr-design.md):
//
//   - One span per sync run per repo.
//   - Nested spans for VCS calls, CI calls, and bd writes are encouraged.
//   - Allowed trace attributes: repo_name, pr_number, run_id,
//     author_email. NEVER comment body.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	otlploggrpc "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	otlploghttp "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	logglobal "go.opentelemetry.io/otel/log/global"
	lognoop "go.opentelemetry.io/otel/log/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// TracerName is the conventional instrumentation library name passed to
// otel.Tracer(...) when emitting spans from pg-pr code.
const TracerName = "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr"

// ShutdownFunc flushes any buffered telemetry and releases exporter
// resources. Safe to call even when Init installed a no-op provider.
type ShutdownFunc func(context.Context) error

// Init configures the global OTel tracer provider and LoggerProvider.
//
// Behaviour:
//
//   - If OTEL_EXPORTER_OTLP_ENDPOINT is empty, Init installs no-op providers
//     and returns a no-op shutdown. No error.
//   - If an exporter fails to initialise, Init logs one stderr warning,
//     installs a no-op provider for that signal, and continues. No error.
//   - On success, Init installs batching SDK providers and returns a combined
//     Shutdown that flushes all of them.
//
// serviceName is used when OTEL_SERVICE_NAME is unset.
func Init(ctx context.Context, serviceName, version string) (ShutdownFunc, error) {
	noopShutdown := func(context.Context) error { return nil }

	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if endpoint == "" {
		// No collector configured — install no-op providers so callers can
		// use otel.Tracer(...) and the global LoggerProvider without nil
		// worries.
		otel.SetTracerProvider(noop.NewTracerProvider())
		logglobal.SetLoggerProvider(lognoop.NewLoggerProvider())
		return noopShutdown, nil
	}

	res := buildResource(ctx, serviceName, version)
	var shutdowns []ShutdownFunc

	// Traces.
	if traceExp, err := newOTLPExporter(ctx); err != nil {
		fmt.Fprintf(os.Stderr,
			"pg-pr: OTel trace exporter init failed (%v); traces will be no-op\n", err)
		otel.SetTracerProvider(noop.NewTracerProvider())
	} else {
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(traceExp),
			sdktrace.WithResource(res),
		)
		otel.SetTracerProvider(tp)
		shutdowns = append(shutdowns, tp.Shutdown)
	}

	// Logs.
	if logExp, err := newOTLPLogExporter(ctx); err != nil {
		fmt.Fprintf(os.Stderr,
			"pg-pr: OTel log exporter init failed (%v); logs will be no-op\n", err)
		logglobal.SetLoggerProvider(lognoop.NewLoggerProvider())
	} else {
		lp := sdklog.NewLoggerProvider(
			sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
			sdklog.WithResource(res),
		)
		logglobal.SetLoggerProvider(lp)
		shutdowns = append(shutdowns, lp.Shutdown)
	}

	return func(ctx context.Context) error {
		var errs []error
		for _, s := range shutdowns {
			if err := s(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}, nil
}

// buildResource constructs the shared OTel resource (service.name/version +
// env + runtime attrs), degrading to a schemaless resource if detection
// fails. service.name comes from OTEL_SERVICE_NAME, else serviceName.
func buildResource(ctx context.Context, serviceName, version string) *resource.Resource {
	res, err := resource.New(ctx,
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

// newOTLPLogExporter builds the OTLP log exporter, mirroring
// newOTLPExporter's protocol switch (grpc vs default http/protobuf). Endpoint
// and TLS come from env vars honored by the exporter packages.
func newOTLPLogExporter(ctx context.Context) (sdklog.Exporter, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"))) {
	case "grpc":
		return otlploggrpc.New(ctx)
	default:
		return otlploghttp.New(ctx)
	}
}

// Tracer returns a tracer scoped to the pg-pr instrumentation library.
// Safe to call before Init: the global provider is a no-op until Init
// installs the SDK provider.
func Tracer() trace.Tracer {
	return otel.Tracer(TracerName)
}

// newOTLPExporter constructs the OTLP trace exporter. Protocol is picked
// from OTEL_EXPORTER_OTLP_PROTOCOL (default "http/protobuf" matches the
// workspace OTel emitter onboarding doc). "grpc" switches to the gRPC
// transport; anything else falls back to HTTP/protobuf.
//
// All endpoint and TLS details are honored from env vars by the
// underlying exporter packages — we set no overrides here.
func newOTLPExporter(ctx context.Context) (sdktrace.SpanExporter, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"))) {
	case "grpc":
		return otlptrace.New(ctx, otlptracegrpc.NewClient())
	default:
		return otlptrace.New(ctx, otlptracehttp.NewClient())
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
