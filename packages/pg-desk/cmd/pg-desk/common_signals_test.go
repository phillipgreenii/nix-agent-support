package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/otelsetup"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func collect(t *testing.T, reader sdkmetric.Reader) (count uint64, overruns int64) {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Histogram[float64]:
				if m.Name == otelsetup.MetricShutdownDuration {
					for _, dp := range d.DataPoints {
						count += dp.Count
					}
				}
			case metricdata.Sum[int64]:
				if m.Name == otelsetup.MetricShutdownOverruns {
					for _, dp := range d.DataPoints {
						overruns += dp.Value
					}
				}
			}
		}
	}
	return count, overruns
}

// shutdownSignals runs observeShutdown for a timer whose context ended `ago`
// before now against `deadline`.
func shutdownSignals(t *testing.T, ago, deadline time.Duration) (uint64, int64) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	sig := registerCommonSignals(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	if sig == nil {
		t.Fatal("registerCommonSignals returned nil")
	}
	tm := &shutdownTimer{deadline: deadline}
	tm.endedAt.Store(time.Now().Add(-ago).UnixNano())
	observeShutdown(context.Background(), sig, tm)
	return collect(t, reader)
}

func TestObserveShutdownOnTimeRecordsDurationNoOverrun(t *testing.T) {
	count, overruns := shutdownSignals(t, time.Second, time.Minute)
	if count != 1 || overruns != 0 {
		t.Fatalf("on time: duration count=%d overruns=%d, want 1 and 0", count, overruns)
	}
}

func TestObserveShutdownLateCountsOverrun(t *testing.T) {
	count, overruns := shutdownSignals(t, time.Minute, time.Second)
	if count != 1 || overruns != 1 {
		t.Fatalf("late: duration count=%d overruns=%d, want 1 and 1", count, overruns)
	}
}

func TestObserveShutdownBeforeContextEndsRecordsNothing(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	sig := registerCommonSignals(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	observeShutdown(context.Background(), sig, &shutdownTimer{deadline: time.Second})
	if count, overruns := collect(t, reader); count != 0 || overruns != 0 {
		t.Fatalf("recorded before the context ended: count=%d overruns=%d", count, overruns)
	}
}

func TestLogShutdownPhaseLogsOnlyAfterTheContextEnds(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	tm := &shutdownTimer{deadline: time.Second}
	logShutdownPhase(log, tm, "early")
	if buf.Len() != 0 {
		t.Fatalf("logged before the context ended: %s", buf.String())
	}
	tm.endedAt.Store(time.Now().UnixNano())
	logShutdownPhase(log, tm, "http drained")
	if out := buf.String(); !strings.Contains(out, "phase=\"http drained\"") || !strings.Contains(out, "elapsed=") {
		t.Fatalf("phase line = %q", out)
	}
}
