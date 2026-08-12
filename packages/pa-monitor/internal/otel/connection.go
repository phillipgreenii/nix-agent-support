package otel

import (
	"context"
	"os"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

// connExportInterval is how often the PeriodicReader scrapes the connection
// gauge. Shorter than the daemon's default 60s so a connected=0 reading
// reaches Prometheus quickly and the `for: 1m` alert is responsive.
const connExportInterval = 15 * time.Second

// ConnOptions configures a ConnEmitter. Component is "cmux-bridge" or "tui".
type ConnOptions struct {
	ServiceName    string
	ServiceVersion string
	Component      string
}

// Outcome values for the pa_monitor.client.reexec counter's "outcome"
// attribute. "exhausted"/"exec_failed" are the give-up states — a persistent
// condition worth alerting on, unlike the one-shot "attempt".
const (
	ReexecOutcomeAttempt    = "attempt"
	ReexecOutcomeExhausted  = "exhausted"
	ReexecOutcomeExecFailed = "exec_failed"
)

// ConnEmitter is a minimal OTel emitter for the cmux-bridge and TUI. It owns
// exactly one observable gauge, pa_monitor.daemon.connected, plus a logger for
// low-level detail. A nil *ConnEmitter is a valid no-op (mirrors *Emitter).
type ConnEmitter struct {
	metricsProvider *sdkmetric.MeterProvider
	logProvider     *sdklog.LoggerProvider
	logger          otellog.Logger
	component       string
	reexecs         metric.Int64Counter

	mu             sync.Mutex
	connectedVal   int64
	connectedKnown bool
}

// connDeps holds the constructors NewConnectionEmitter depends on. Splitting
// them out lets tests inject failures into the exporter-construction and
// gauge-registration error branches, which are otherwise unreachable: the gRPC
// exporter New() funcs swallow bad-endpoint errors (they fall back to the
// default target and report parse errors via the global handler, not the
// return value).
type connDeps struct {
	buildResource func(ctx context.Context, name, version string) (*resource.Resource, error)
	newMetricExp  func(ctx context.Context) (sdkmetric.Exporter, error)
	newLogExp     func(ctx context.Context) (sdklog.Exporter, error)
	registerGauge func(e *ConnEmitter, mp *sdkmetric.MeterProvider) error
}

func defaultConnDeps() connDeps {
	return connDeps{
		buildResource: buildResource,
		newMetricExp:  func(ctx context.Context) (sdkmetric.Exporter, error) { return otlpmetricgrpc.New(ctx) },
		newLogExp:     func(ctx context.Context) (sdklog.Exporter, error) { return otlploggrpc.New(ctx) },
		registerGauge: (*ConnEmitter).registerGauge,
	}
}

// NewConnectionEmitter returns (nil, nil) when OTEL_EXPORTER_OTLP_ENDPOINT is
// unset, matching otel.New's disabled-state contract.
func NewConnectionEmitter(ctx context.Context, opts ConnOptions) (*ConnEmitter, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return nil, nil
	}
	return newConnectionEmitter(ctx, opts, defaultConnDeps())
}

// newConnectionEmitter is the dependency-injected core of NewConnectionEmitter.
// It assumes the endpoint gate has already been checked; tests call it directly
// with a fault-injecting connDeps to exercise the error branches.
func newConnectionEmitter(ctx context.Context, opts ConnOptions, deps connDeps) (*ConnEmitter, error) {
	res, err := deps.buildResource(ctx, opts.ServiceName, opts.ServiceVersion)
	if err != nil {
		return nil, err
	}
	metricExp, err := deps.newMetricExp(ctx)
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(connExportInterval))),
		sdkmetric.WithResource(res),
	)
	logExp, err := deps.newLogExp(ctx)
	if err != nil {
		_ = mp.Shutdown(ctx)
		return nil, err
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
		sdklog.WithResource(res),
	)
	e := &ConnEmitter{
		metricsProvider: mp,
		logProvider:     lp,
		logger:          lp.Logger("pa-monitor"),
		component:       opts.Component,
	}
	if err := deps.registerGauge(e, mp); err != nil {
		_ = mp.Shutdown(ctx)
		_ = lp.Shutdown(ctx)
		return nil, err
	}
	return e, nil
}

func (e *ConnEmitter) registerGauge(mp *sdkmetric.MeterProvider) error {
	meter := mp.Meter("pa-monitor")
	gauge, err := meter.Int64ObservableGauge("pa_monitor.daemon.connected")
	if err != nil {
		return err
	}
	if _, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		e.mu.Lock()
		val, known := e.connectedVal, e.connectedKnown
		e.mu.Unlock()
		if known {
			o.ObserveInt64(gauge, val,
				metric.WithAttributes(attribute.String("component", e.component)))
		}
		return nil
	}, gauge); err != nil {
		return err
	}
	if e.reexecs, err = meter.Int64Counter("pa_monitor.client.reexec"); err != nil {
		return err
	}
	return nil
}

// RecordDaemonConnected buffers the latest connection state for the gauge
// callback. nil-safe.
func (e *ConnEmitter) RecordDaemonConnected(connected bool) {
	if e == nil {
		return
	}
	v := int64(0)
	if connected {
		v = 1
	}
	e.mu.Lock()
	e.connectedVal = v
	e.connectedKnown = true
	e.mu.Unlock()
}

// RecordReexec increments pa_monitor.client.reexec once per self-restart
// decision, tagged with the attempt number and outcome (ReexecOutcome*). It
// also emits a companion "client.reexec" log event so the metric increment has
// trace context. nil-safe.
func (e *ConnEmitter) RecordReexec(attempt int, outcome string) {
	if e == nil {
		return
	}
	if e.reexecs != nil {
		e.reexecs.Add(context.Background(), 1, metric.WithAttributes(
			attribute.String("component", e.component),
			attribute.Int("attempt", attempt),
			attribute.String("outcome", outcome),
		))
	}
	e.LogEvent("client.reexec", map[string]string{
		"attempt": strconv.Itoa(attempt),
		"outcome": outcome,
	})
}

// LogEvent emits one info-level log record with event_name + component +
// attrs. nil-safe.
func (e *ConnEmitter) LogEvent(name string, attrs map[string]string) {
	if e == nil || e.logger == nil {
		return
	}
	var rec otellog.Record
	rec.SetTimestamp(time.Now())
	rec.SetSeverity(severityForEvent(name))
	rec.SetBody(attribute.StringValue(name))
	rec.AddAttributes(attribute.String("event_name", name))
	rec.AddAttributes(attribute.String("component", e.component))
	for k, v := range attrs {
		if v == "" {
			continue
		}
		rec.AddAttributes(attribute.String(k, v))
	}
	e.logger.Emit(context.Background(), rec)
}

// Shutdown flushes both providers. nil-safe.
func (e *ConnEmitter) Shutdown(ctx context.Context) error {
	if e == nil {
		return nil
	}
	var firstErr error
	if e.metricsProvider != nil {
		if err := e.metricsProvider.Shutdown(ctx); err != nil {
			firstErr = err
		}
	}
	if e.logProvider != nil {
		if err := e.logProvider.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// connectedValue is a test-only accessor for the buffered gauge value.
func (e *ConnEmitter) connectedValue() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.connectedVal
}
