// Package telemetry initializes OpenTelemetry OTLP LOG export for ccpool's
// operational slog records, plus a MeterProvider a sibling package (the
// metrics instrument catalog) registers instruments against. diaglog and
// eventlog are untouched by this package — the new mechanism is additive,
// not a replacement (pg2-qye99 / pg2-24f89).
//
// Safe-by-default: Init can never fail its caller, and no-ops entirely when
// OTEL_EXPORTER_OTLP_ENDPOINT is unset. On any exporter-construction error it
// logs one line to stderr and falls back to the no-op providers — every
// path returns successfully.
//
// OTLP PUSH (not file-tailing) is used deliberately: ccpool is a
// short-lived, per-invocation CLI with no resident process to host a
// pull-scrape endpoint.
//
// service.name is HARDCODED to "ccpool" and is NEVER read from
// OTEL_SERVICE_NAME, unlike the packages/pg-router and packages/pg-desk
// telemetry packages this one otherwise mirrors for its LOG half: ccpool is
// routinely invoked as a subprocess of pg-router-ccpool-handler's
// CLIRunner (and, once wired, the ccpool-reap LaunchAgent), and pg-router's
// own darwin module sets OTEL_SERVICE_NAME=pg-router into its LaunchAgent
// environment. Honoring that inherited value would silently mislabel every
// dispatch-driven ccpool session as pg-router.
//
// OTEL_EXPORTER_OTLP_ENDPOINT / OTEL_EXPORTER_OTLP_PROTOCOL, by contrast,
// ARE inherited normally from the process environment — only the
// service-name attribute is hardcoded:
//
//	OTEL_EXPORTER_OTLP_ENDPOINT   (default: not set -> no-op)
//	OTEL_EXPORTER_OTLP_PROTOCOL   (LOG signal only; default: http/protobuf;
//	                               "grpc" switches to otlploggrpc)
//
// The metric signal always exports over gRPC (otlpmetricgrpc), mirroring
// packages/pa-monitor/internal/otel/emitter.go's own metric-exporter shape
// (which has no protocol switch either) minus its health-throttling
// decorator — YAGNI for a single, short-lived CLI invocation with no
// repeated-export-spam problem.
//
// MeterProvider exposes the global metric.MeterProvider Init installs
// (real when an endpoint is configured, otherwise the OTel no-op provider)
// so the sibling metrics-catalog package can register its instruments
// against it once this package's Init has run — call telemetry.
// MeterProvider() and then MeterProvider().Meter("<scope>") to obtain a
// metric.Meter.
package telemetry

import (
	"context"
	"fmt"
	"os"
	"strings"

	otlploggrpc "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	otlploghttp "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	otlpmetricgrpc "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	logglobal "go.opentelemetry.io/otel/log/global"
	lognoop "go.opentelemetry.io/otel/log/noop"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// serviceNameValue is hardcoded — see package doc. Never read from
// OTEL_SERVICE_NAME, unlike the pg-router/pg-desk precedent this package
// otherwise mirrors.
const serviceNameValue = "ccpool"

// meterProvider is the MeterProvider Init installs (real or no-op),
// returned by the exported MeterProvider accessor below. Starts as a
// no-op so a caller reading it before Init runs still gets a usable,
// inert provider rather than a nil interface.
var meterProvider metric.MeterProvider = metricnoop.NewMeterProvider()

// MeterProvider returns the metric.MeterProvider Init most recently
// installed: the real OTLP-backed provider once an endpoint is configured
// and construction succeeds, otherwise the OTel no-op provider. Safe to
// call at any time, including before Init runs. The sibling metrics
// instrument catalog package calls this (then .Meter("<scope>")) to
// register its counters/gauges against the same provider ccpool's Init
// configured — it does not construct its own.
func MeterProvider() metric.MeterProvider {
	return meterProvider
}

// Init configures the global OTel LoggerProvider and this package's
// MeterProvider.
//
// Behaviour:
//
//   - If OTEL_EXPORTER_OTLP_ENDPOINT is empty, Init installs no-op
//     Logger/Meter providers and returns a no-op shutdown.
//   - If either exporter fails to construct, Init logs one stderr
//     warning, installs no-op Logger/Meter providers, and continues.
//   - On success, Init installs batching SDK providers for both signals
//     and returns a shutdown that flushes both.
//
// Init never returns an error: every path above succeeds.
func Init(ctx context.Context) (shutdown func(context.Context) error) {
	noopShutdown := func(context.Context) error { return nil }

	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if endpoint == "" {
		logglobal.SetLoggerProvider(lognoop.NewLoggerProvider())
		meterProvider = metricnoop.NewMeterProvider()
		return noopShutdown
	}

	res := buildResource(ctx)

	logExp, err := newOTLPLogExporter(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ccpool: OTel log exporter init failed (%v); telemetry disabled\n", err)
		logglobal.SetLoggerProvider(lognoop.NewLoggerProvider())
		meterProvider = metricnoop.NewMeterProvider()
		return noopShutdown
	}

	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ccpool: OTel metric exporter init failed (%v); telemetry disabled\n", err)
		logglobal.SetLoggerProvider(lognoop.NewLoggerProvider())
		meterProvider = metricnoop.NewMeterProvider()
		return noopShutdown
	}

	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
		sdklog.WithResource(res),
	)
	logglobal.SetLoggerProvider(lp)

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)
	meterProvider = mp

	return func(shutdownCtx context.Context) error {
		logErr := lp.Shutdown(shutdownCtx)
		metricErr := mp.Shutdown(shutdownCtx)
		if logErr != nil {
			return logErr
		}
		return metricErr
	}
}

// buildResource constructs the shared OTel resource (service.name/version +
// runtime attrs), degrading to a schemaless resource if detection fails.
// service.name is always serviceNameValue ("ccpool") — deliberately NOT
// merged with OTEL_RESOURCE_ATTRIBUTES/OTEL_SERVICE_NAME env detection, so
// an inherited OTEL_SERVICE_NAME can never override it (see package doc).
func buildResource(ctx context.Context) *resource.Resource {
	res, err := resource.New(
		ctx,
		resource.WithAttributes(semconv.ServiceName(serviceNameValue)),
		resource.WithProcessRuntimeName(),
		resource.WithProcessRuntimeVersion(),
	)
	if err != nil {
		return resource.NewSchemaless(semconv.ServiceName(serviceNameValue))
	}
	return res
}

// newOTLPLogExporter builds the OTLP log exporter. Protocol is picked from
// OTEL_EXPORTER_OTLP_PROTOCOL (default "http/protobuf"); "grpc" switches to
// the gRPC transport. Endpoint and TLS are honored from env vars by the
// underlying exporter packages — no overrides are set here.
func newOTLPLogExporter(ctx context.Context) (sdklog.Exporter, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"))) {
	case "grpc":
		return otlploggrpc.New(ctx)
	default:
		return otlploghttp.New(ctx)
	}
}
