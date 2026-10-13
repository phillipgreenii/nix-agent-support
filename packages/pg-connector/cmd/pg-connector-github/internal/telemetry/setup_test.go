package telemetry

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

type fakeMetricExporter struct {
	sdkmetric.Exporter
	endpoint string
	shutdown bool
}

func (*fakeMetricExporter) Temporality(sdkmetric.InstrumentKind) metricdata.Temporality {
	return metricdata.CumulativeTemporality
}

func (*fakeMetricExporter) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(k)
}
func (*fakeMetricExporter) Export(context.Context, *metricdata.ResourceMetrics) error { return nil }
func (*fakeMetricExporter) ForceFlush(context.Context) error                          { return nil }
func (e *fakeMetricExporter) Shutdown(context.Context) error                          { e.shutdown = true; return nil }

type fakeSpanExporter struct {
	endpoint string
	shutdown bool
}

func (*fakeSpanExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (e *fakeSpanExporter) Shutdown(context.Context) error                           { e.shutdown = true; return nil }

// stubExporters replaces the exporter constructors for one test and restores
// the global providers afterwards.
func stubExporters(t *testing.T, m func(string) (sdkmetric.Exporter, error), s func(string) (sdktrace.SpanExporter, error)) {
	t.Helper()
	om, os := newMetricExporter, newSpanExporter
	newMetricExporter = func(_ context.Context, ep string) (sdkmetric.Exporter, error) { return m(ep) }
	newSpanExporter = func(_ context.Context, ep string) (sdktrace.SpanExporter, error) { return s(ep) }
	t.Cleanup(func() {
		newMetricExporter, newSpanExporter = om, os
		otel.SetMeterProvider(metricnoop.NewMeterProvider())
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
	})
}

func TestSetupWithNoEndpointIsAWorkingNoOpAndBuildsNoExporter(t *testing.T) {
	for _, endpoint := range []string{"", "   "} {
		stubExporters(t,
			func(string) (sdkmetric.Exporter, error) {
				t.Fatal("a metric exporter was built with no endpoint")
				return nil, nil
			},
			func(string) (sdktrace.SpanExporter, error) {
				t.Fatal("a span exporter was built with no endpoint")
				return nil, nil
			})
		shutdown, err := Setup(Config{Endpoint: endpoint})
		if err != nil {
			t.Fatalf("endpoint %q: %v", endpoint, err)
		}
		if err := shutdown(context.Background()); err != nil {
			t.Errorf("no-op shutdown: %v", err)
		}
		if _, ok := otel.GetMeterProvider().(metricnoop.MeterProvider); !ok {
			t.Errorf("meter provider is %T, want the no-op", otel.GetMeterProvider())
		}
		if _, ok := otel.GetTracerProvider().(tracenoop.TracerProvider); !ok {
			t.Errorf("tracer provider is %T, want the no-op", otel.GetTracerProvider())
		}

		// The daemon path on the no-op providers works and records nothing.
		tel, err := New(Options{})
		if err != nil {
			t.Fatal(err)
		}
		tel.OnBatchSent([]string{"x"}, "due")
		if err := tel.Close(); err != nil {
			t.Error(err)
		}
	}
}

func TestSetupWithAnEndpointBuildsBothExportersAndShutdownFlushesThem(t *testing.T) {
	me, se := &fakeMetricExporter{}, &fakeSpanExporter{}
	stubExporters(t,
		func(ep string) (sdkmetric.Exporter, error) { me.endpoint = ep; return me, nil },
		func(ep string) (sdktrace.SpanExporter, error) { se.endpoint = ep; return se, nil })
	shutdown, err := Setup(Config{Endpoint: " http://127.0.0.1:4317 ", ServiceVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if me.endpoint != "http://127.0.0.1:4317" || se.endpoint != "http://127.0.0.1:4317" {
		t.Errorf("endpoints %q %q", me.endpoint, se.endpoint)
	}
	if _, ok := otel.GetMeterProvider().(*sdkmetric.MeterProvider); !ok {
		t.Errorf("meter provider is %T, want the SDK's", otel.GetMeterProvider())
	}
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); !ok {
		t.Errorf("tracer provider is %T, want the SDK's", otel.GetTracerProvider())
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !me.shutdown || !se.shutdown {
		t.Errorf("shutdown must reach both exporters: metric=%v trace=%v", me.shutdown, se.shutdown)
	}
}

func TestSetupExporterFailuresAreErrorsAndLeaveNothingInstalled(t *testing.T) {
	boom := errors.New("boom")
	stubExporters(t,
		func(string) (sdkmetric.Exporter, error) { return nil, boom },
		func(string) (sdktrace.SpanExporter, error) {
			t.Fatal("trace exporter built after the metric one failed")
			return nil, nil
		})
	if _, err := Setup(Config{Endpoint: "http://127.0.0.1:4317"}); !errors.Is(err, boom) {
		t.Errorf("metric exporter failure: %v", err)
	}

	me := &fakeMetricExporter{}
	stubExporters(t,
		func(string) (sdkmetric.Exporter, error) { return me, nil },
		func(string) (sdktrace.SpanExporter, error) { return nil, boom })
	if _, err := Setup(Config{Endpoint: "http://127.0.0.1:4317"}); !errors.Is(err, boom) {
		t.Errorf("trace exporter failure: %v", err)
	}
	if !me.shutdown {
		t.Error("the metric exporter built before the failure must be shut down")
	}
	if _, ok := otel.GetMeterProvider().(*sdkmetric.MeterProvider); ok {
		t.Error("a failed Setup must not install the SDK providers")
	}
}

// The real constructors dial lazily: building and shutting down the exporters
// of a configured endpoint makes no connection, so a daemon whose collector is
// down still starts.
func TestRealExportersConstructWithoutDialing(t *testing.T) {
	ctx := context.Background()
	me, err := newMetricExporter(ctx, "http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	se, err := newSpanExporter(ctx, "http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	if err := me.Shutdown(ctx); err != nil {
		t.Errorf("metric exporter shutdown: %v", err)
	}
	if err := se.Shutdown(ctx); err != nil {
		t.Errorf("trace exporter shutdown: %v", err)
	}
}
