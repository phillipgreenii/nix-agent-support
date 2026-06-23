package otel

import (
	"context"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
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

// ConnEmitter is a minimal OTel emitter for the cmux-bridge and TUI. It owns
// exactly one observable gauge, pa_monitor.daemon.connected, plus a logger for
// low-level detail. A nil *ConnEmitter is a valid no-op (mirrors *Emitter).
type ConnEmitter struct {
	metricsProvider *sdkmetric.MeterProvider
	logProvider     *sdklog.LoggerProvider
	logger          otellog.Logger
	component       string

	mu             sync.Mutex
	connectedVal   int64
	connectedKnown bool
}

// NewConnectionEmitter returns (nil, nil) when OTEL_EXPORTER_OTLP_ENDPOINT is
// unset, matching otel.New's disabled-state contract.
func NewConnectionEmitter(ctx context.Context, opts ConnOptions) (*ConnEmitter, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return nil, nil
	}
	res, err := buildResource(ctx, opts.ServiceName, opts.ServiceVersion)
	if err != nil {
		return nil, err
	}
	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(connExportInterval))),
		sdkmetric.WithResource(res),
	)
	logExp, err := otlploggrpc.New(ctx)
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
	if err := e.registerGauge(mp); err != nil {
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
	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		e.mu.Lock()
		val, known := e.connectedVal, e.connectedKnown
		e.mu.Unlock()
		if known {
			o.ObserveInt64(gauge, val,
				metric.WithAttributes(attribute.String("component", e.component)))
		}
		return nil
	}, gauge)
	return err
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

// LogEvent emits one info-level log record with event_name + component +
// attrs. nil-safe.
func (e *ConnEmitter) LogEvent(name string, attrs map[string]string) {
	if e == nil || e.logger == nil {
		return
	}
	var rec otellog.Record
	rec.SetTimestamp(time.Now())
	rec.SetSeverity(otellog.SeverityInfo)
	rec.SetBody(otellog.StringValue(name))
	rec.AddAttributes(otellog.String("event_name", name))
	rec.AddAttributes(otellog.String("component", e.component))
	for k, v := range attrs {
		if v == "" {
			continue
		}
		rec.AddAttributes(otellog.String(k, v))
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
