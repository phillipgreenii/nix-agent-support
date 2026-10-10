package obs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otlploggrpc "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	otlploghttp "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	logglobal "go.opentelemetry.io/otel/log/global"
	lognoop "go.opentelemetry.io/otel/log/noop"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// ShutdownFunc flushes buffered telemetry and releases the exporters. It is
// safe to call when Init installed no-op providers.
type ShutdownFunc func(context.Context) error

// Init configures the global tracer and log providers from the standard
// OTEL_* variables. It is best-effort: with no endpoint, or an endpoint that
// is not on the loopback interface, or an exporter that fails to build, it
// installs no-op providers, says so once on warn, and returns no error, so the
// daemon never refuses to start for want of a collector. The sampler is
// ParentBased(AlwaysOn).
func Init(ctx context.Context, serviceName, version string, warn func(string)) ShutdownFunc {
	none := func(context.Context) error { return nil }
	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if endpoint == "" {
		otel.SetTracerProvider(noop.NewTracerProvider())
		logglobal.SetLoggerProvider(lognoop.NewLoggerProvider())
		return none
	}
	if !Loopback(endpoint) {
		warn(fmt.Sprintf("OTLP endpoint %q is not on the loopback interface; telemetry stays local, so traces and logs are no-op", endpoint))
		otel.SetTracerProvider(noop.NewTracerProvider())
		logglobal.SetLoggerProvider(lognoop.NewLoggerProvider())
		return none
	}
	res := buildResource(ctx, serviceName, version)
	var shutdowns []ShutdownFunc
	grpc := strings.EqualFold(strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")), "grpc")

	var traceExp sdktrace.SpanExporter
	var err error
	if grpc {
		traceExp, err = otlptrace.New(ctx, otlptracegrpc.NewClient())
	} else {
		traceExp, err = otlptrace.New(ctx, otlptracehttp.NewClient())
	}
	if err != nil {
		warn(fmt.Sprintf("OTel trace exporter failed to start (%v); traces are no-op", err))
		otel.SetTracerProvider(noop.NewTracerProvider())
	} else {
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(traceExp), sdktrace.WithResource(res),
			sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
		)
		otel.SetTracerProvider(tp)
		shutdowns = append(shutdowns, tp.Shutdown)
	}

	var logExp sdklog.Exporter
	if grpc {
		logExp, err = otlploggrpc.New(ctx)
	} else {
		logExp, err = otlploghttp.New(ctx)
	}
	if err != nil {
		warn(fmt.Sprintf("OTel log exporter failed to start (%v); OTLP logs are no-op", err))
		logglobal.SetLoggerProvider(lognoop.NewLoggerProvider())
	} else {
		lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)), sdklog.WithResource(res))
		logglobal.SetLoggerProvider(lp)
		shutdowns = append(shutdowns, lp.Shutdown)
	}
	return func(ctx context.Context) error {
		var errs []error
		for _, s := range shutdowns {
			errs = append(errs, s(ctx))
		}
		return errors.Join(errs...)
	}
}

// Loopback reports whether an OTLP endpoint (a URL, or host:port) names the
// loopback interface: localhost, 127.0.0.0/8 or ::1.
func Loopback(endpoint string) bool {
	host := endpoint
	if strings.Contains(endpoint, "://") {
		u, err := url.Parse(endpoint)
		if err != nil {
			return false
		}
		host = u.Host
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func buildResource(ctx context.Context, serviceName, version string) *resource.Resource {
	name := serviceName
	if v := strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME")); v != "" {
		name = v
	}
	res, err := resource.New(
		ctx,
		resource.WithAttributes(semconv.ServiceName(name), semconv.ServiceVersion(version)),
		resource.WithFromEnv(), resource.WithProcessRuntimeName(), resource.WithProcessRuntimeVersion(),
	)
	if err != nil {
		return resource.NewSchemaless(semconv.ServiceName(name), semconv.ServiceVersion(version))
	}
	return res
}

// AllowedSpanAttributes is the explicit list of attributes a span may carry.
// It excludes every free-text field: a note, a key/value value, a skip or
// correction reason, a period label, a task or cycle title.
var AllowedSpanAttributes = map[string]bool{
	"http.request.method": true, "http.route": true, "http.response.status_code": true,
	"url.path": true, "client": true, "request_id": true, "event_type": true, "event_id": true,
	"reason": true, "changed": true, "events": true, "stage": true, "version": true,
}

// Attrs sets the allowed attributes among kvs on span and drops the rest, so
// a free-text field cannot reach a trace by accident.
func Attrs(span trace.Span, kvs ...attribute.KeyValue) {
	var keep []attribute.KeyValue
	for _, kv := range kvs {
		if AllowedSpanAttributes[string(kv.Key)] {
			keep = append(keep, kv)
		}
	}
	span.SetAttributes(keep...)
}

// Propagator is the W3C trace-context propagator used for traceparent.
var Propagator = propagation.TraceContext{}

// TraceResponse is the value of the traceresponse header for a span context.
func TraceResponse(sc trace.SpanContext) string {
	if !sc.IsValid() {
		return ""
	}
	flags := "00"
	if sc.IsSampled() {
		flags = "01"
	}
	return "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-" + flags
}
