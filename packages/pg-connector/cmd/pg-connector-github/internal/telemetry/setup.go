// Package telemetry is pg-connector-github's OpenTelemetry layer: the meter and
// tracer providers (Setup), the instruments of the daemon's metric table and
// the request and origin-call spans (Telemetry).
//
// # Where the OTel dependency lives
//
// The umbrella pg-connector and the shared library pkg/connectord stay unbound
// to OpenTelemetry (see the pkg/eventlog doc comment); this package, inside the
// binary's own internal/ tree, is the only place that imports the SDK. The
// library exposes plain observers and state snapshots (governor OnSpend and
// State, scheduler State, router Use, the summary service's OnBatchSent) and
// this package subscribes to them.
//
// # Export
//
// Setup exports over OTLP/gRPC to the endpoint named in the daemon's rendered
// config, the same transport as the workspace's other daemons (pa-monitor,
// ccpool): an "http://host:port" endpoint is insecure gRPC and "https://" is
// TLS. With no endpoint, Setup installs no-op providers and returns a no-op
// shutdown: nothing is constructed, nothing dials, and the daemon runs
// normally.
//
// # Names and labels
//
// Metric names are dotted after the pa-monitor convention, prefixed
// "pg_connector_github.". A label value is always drawn from a closed
// vocabulary the code defines (a bucket, a class, a role, a registered op, a
// handler's served_from word, a reduced pause reason); an entity id, a request
// argument or an ad-hoc query key is never a label. A value the code cannot
// bound is folded to a fixed word ("unknown", "none", "other").
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// ServiceName is the service.name resource attribute of every export.
const ServiceName = "pg-connector-github"

// DefaultInterval is how often the periodic reader exports, and so how often
// the polled gauges are read, when Config.Interval is zero.
const DefaultInterval = 30 * time.Second

// Config is what Setup needs from the daemon's typed config.
type Config struct {
	// Endpoint is the OTLP/gRPC endpoint URL (config.Config.OTelEndpoint);
	// empty or blank means no export.
	Endpoint string
	// ServiceVersion is the build's version, stamped as service.version when
	// non-empty.
	ServiceVersion string
	// Interval is the metric export (and gauge poll) interval; zero means
	// DefaultInterval.
	Interval time.Duration
}

// Enabled reports whether an endpoint is configured.
func (c Config) Enabled() bool { return strings.TrimSpace(c.Endpoint) != "" }

// The exporter constructors are variables so a test can prove that no
// exporter is built without an endpoint and can stand in a recording one with.
var (
	newMetricExporter = func(ctx context.Context, endpoint string) (sdkmetric.Exporter, error) {
		return otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithEndpointURL(endpoint))
	}
	newSpanExporter = func(ctx context.Context, endpoint string) (sdktrace.SpanExporter, error) {
		return otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(endpoint))
	}
)

// Setup installs the global meter and tracer providers. With no endpoint they
// are no-op providers and the returned shutdown does nothing (so an instrument
// obtained from them is inert and no network call is ever made). With one they
// batch to the endpoint and shutdown flushes both. An exporter that cannot be
// built is returned as an error with no provider installed; the caller logs it
// and runs without telemetry.
func Setup(cfg Config) (shutdown func(context.Context) error, err error) {
	if !cfg.Enabled() {
		otel.SetMeterProvider(metricnoop.NewMeterProvider())
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
		return func(context.Context) error { return nil }, nil
	}
	endpoint := strings.TrimSpace(cfg.Endpoint)
	ctx := context.Background() // construction dials lazily; only the exports use the network

	res := buildResource(ctx, cfg.ServiceVersion)

	metricExp, err := newMetricExporter(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("telemetry: metric exporter for %q: %w", endpoint, err)
	}
	spanExp, err := newSpanExporter(ctx, endpoint)
	if err != nil {
		_ = metricExp.Shutdown(ctx)
		return nil, fmt.Errorf("telemetry: trace exporter for %q: %w", endpoint, err)
	}

	interval := cfg.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp, sdkmetric.WithInterval(interval))),
		sdkmetric.WithResource(res),
	)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(spanExp),
		sdktrace.WithResource(res),
	)
	otel.SetMeterProvider(mp)
	otel.SetTracerProvider(tp)

	return func(ctx context.Context) error {
		return errors.Join(mp.Shutdown(ctx), tp.Shutdown(ctx))
	}, nil
}

// buildResource is the resource every export carries. service.name is fixed:
// the daemon is often started by a supervisor whose environment names another
// service, and an inherited OTEL_SERVICE_NAME must not relabel its series.
func buildResource(ctx context.Context, version string) *resource.Resource {
	attrs := []attribute.KeyValue{semconv.ServiceName(ServiceName)}
	if version != "" {
		attrs = append(attrs, semconv.ServiceVersion(version))
	}
	res, err := resource.New(
		ctx,
		resource.WithAttributes(attrs...),
		resource.WithProcessRuntimeName(),
		resource.WithProcessRuntimeVersion(),
	)
	if err != nil {
		return resource.NewSchemaless(attrs...)
	}
	return res
}
