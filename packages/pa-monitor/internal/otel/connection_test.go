package otel

import (
	"context"
	"errors"
	"testing"

	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
)

func TestConnectionEmitterDisabledWhenNoEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	e, err := NewConnectionEmitter(context.Background(), ConnOptions{
		ServiceName: "pa-monitor", Component: "cmux-bridge",
	})
	if err != nil {
		t.Fatal(err)
	}
	if e != nil {
		t.Fatalf("want nil emitter when endpoint unset, got %#v", e)
	}
	e.RecordDaemonConnected(false)
	e.LogEvent("daemon.disconnect", map[string]string{"error": "x"})
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatalf("nil Shutdown: %v", err)
	}
}

func TestConnectionEmitterConstructsAndRecords(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4317")
	e, err := NewConnectionEmitter(context.Background(), ConnOptions{
		ServiceName: "pa-monitor", ServiceVersion: "test", Component: "tui",
	})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if e == nil {
		t.Fatal("want non-nil emitter when endpoint set")
	}
	defer e.Shutdown(context.Background())
	e.RecordDaemonConnected(true)
	e.RecordDaemonConnected(false)
	if got := e.connectedValue(); got != 0 {
		t.Errorf("connectedValue = %d, want 0", got)
	}
}

// TestConnectionEmitterLogEvent exercises the LogEvent emission path on a
// constructed (non-nil) emitter: it must build a record and walk the attrs
// map, skipping empty values, without panicking. The batch log processor
// buffers the record, so no live collector is required.
func TestConnectionEmitterLogEvent(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4317")
	e, err := NewConnectionEmitter(context.Background(), ConnOptions{
		ServiceName: "pa-monitor", ServiceVersion: "test", Component: "cmux-bridge",
	})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if e == nil {
		t.Fatal("want non-nil emitter when endpoint set")
	}
	defer e.Shutdown(context.Background())
	// Non-empty and empty values: the empty value must be skipped by the loop.
	e.LogEvent("daemon.disconnect", map[string]string{"error": "boom", "skipped": ""})
	e.LogEvent("daemon.reconnect", nil)
}

// stubMetricExporter implements sdkmetric.Exporter. Export/ForceFlush succeed
// so that, during a provider Shutdown, only the injected shutdownErr surfaces.
// Temporality/Aggregation delegate to the SDK defaults — a zero-value
// Temporality is invalid and would fail gauge collection before Shutdown.
type stubMetricExporter struct{ shutdownErr error }

func (s stubMetricExporter) Temporality(k sdkmetric.InstrumentKind) metricdata.Temporality {
	return sdkmetric.DefaultTemporalitySelector(k)
}

func (s stubMetricExporter) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(k)
}

func (s stubMetricExporter) Export(context.Context, *metricdata.ResourceMetrics) error { return nil }

func (s stubMetricExporter) ForceFlush(context.Context) error { return nil }

func (s stubMetricExporter) Shutdown(context.Context) error { return s.shutdownErr }

// stubLogExporter implements sdklog.Exporter; same contract as above.
type stubLogExporter struct{ shutdownErr error }

func (s stubLogExporter) Export(context.Context, []sdklog.Record) error { return nil }
func (s stubLogExporter) Shutdown(context.Context) error                { return s.shutdownErr }
func (s stubLogExporter) ForceFlush(context.Context) error              { return nil }

func TestNewConnectionEmitterBuildResourceError(t *testing.T) {
	wantErr := errors.New("resource boom")
	deps := defaultConnDeps()
	deps.buildResource = func(context.Context, string, string) (*resource.Resource, error) {
		return nil, wantErr
	}
	if _, err := newConnectionEmitter(context.Background(), ConnOptions{Component: "tui"}, deps); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestNewConnectionEmitterMetricExporterError(t *testing.T) {
	wantErr := errors.New("metric exporter boom")
	deps := defaultConnDeps()
	deps.newMetricExp = func(context.Context) (sdkmetric.Exporter, error) { return nil, wantErr }
	if _, err := newConnectionEmitter(context.Background(), ConnOptions{Component: "tui"}, deps); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestNewConnectionEmitterLogExporterError(t *testing.T) {
	wantErr := errors.New("log exporter boom")
	deps := defaultConnDeps()
	deps.newMetricExp = func(context.Context) (sdkmetric.Exporter, error) { return stubMetricExporter{}, nil }
	deps.newLogExp = func(context.Context) (sdklog.Exporter, error) { return nil, wantErr }
	// Covers the mp.Shutdown(ctx) cleanup on log-exporter failure.
	if _, err := newConnectionEmitter(context.Background(), ConnOptions{Component: "tui"}, deps); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestNewConnectionEmitterRegisterGaugeError(t *testing.T) {
	wantErr := errors.New("register boom")
	deps := defaultConnDeps()
	deps.newMetricExp = func(context.Context) (sdkmetric.Exporter, error) { return stubMetricExporter{}, nil }
	deps.newLogExp = func(context.Context) (sdklog.Exporter, error) { return stubLogExporter{}, nil }
	deps.registerGauge = func(*ConnEmitter, *sdkmetric.MeterProvider) error { return wantErr }
	// Covers the registerGauge error branch (both providers shut down).
	if _, err := newConnectionEmitter(context.Background(), ConnOptions{Component: "tui"}, deps); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestConnectionEmitterShutdownMetricError(t *testing.T) {
	wantErr := errors.New("metric shutdown boom")
	deps := defaultConnDeps()
	deps.newMetricExp = func(context.Context) (sdkmetric.Exporter, error) {
		return stubMetricExporter{shutdownErr: wantErr}, nil
	}
	deps.newLogExp = func(context.Context) (sdklog.Exporter, error) { return stubLogExporter{}, nil }
	e, err := newConnectionEmitter(context.Background(), ConnOptions{Component: "tui"}, deps)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	// Provider may wrap/join the exporter error — assert with errors.Is, not ==.
	if err := e.Shutdown(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Shutdown err = %v, want %v", err, wantErr)
	}
}

func TestConnectionEmitterShutdownLogError(t *testing.T) {
	wantErr := errors.New("log shutdown boom")
	deps := defaultConnDeps()
	deps.newMetricExp = func(context.Context) (sdkmetric.Exporter, error) { return stubMetricExporter{}, nil }
	deps.newLogExp = func(context.Context) (sdklog.Exporter, error) {
		return stubLogExporter{shutdownErr: wantErr}, nil
	}
	e, err := newConnectionEmitter(context.Background(), ConnOptions{Component: "tui"}, deps)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	// Metric Shutdown succeeds, so this exercises the firstErr==nil log branch.
	if err := e.Shutdown(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Shutdown err = %v, want %v", err, wantErr)
	}
}
