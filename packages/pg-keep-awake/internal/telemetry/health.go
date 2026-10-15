package telemetry

// Origin: adapted from packages/pa-monitor/internal/otel/health.go (a Go
// internal package cannot be imported across modules); pg-keep-awake design
// section 15. Changes: the self-instrumentation instruments are dropped and
// the summary lines are prefixed pg-keep-awake.

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// defaultHealthThrottle bounds how often a still-UNHEALTHY summary is repeated
// while an outage persists. The first failure of a streak and the recovery are
// always surfaced; intermediate failures collapse into at most one line per
// window.
const defaultHealthThrottle = 5 * time.Minute

// exportHealth tracks OTLP export success/failure and renders a throttled,
// human-readable summary in place of the SDK's per-attempt stderr spam. It is
// pure state plus string rendering, so tests drive it with a fake clock. All
// methods are safe for concurrent use.
type exportHealth struct {
	now      func() time.Time
	throttle time.Duration

	mu               sync.Mutex
	consecutiveFails int
	streakStart      time.Time
	lastErr          string
	lastSummaryAt    time.Time
}

func newExportHealth(now func() time.Time, throttle time.Duration) *exportHealth {
	if now == nil {
		now = time.Now
	}
	if throttle <= 0 {
		throttle = defaultHealthThrottle
	}
	return &exportHealth{now: now, throttle: throttle}
}

// onFailure records one failed export attempt and returns a summary line and
// true when the throttle policy says it should be surfaced.
func (h *exportHealth) onFailure(err error) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.consecutiveFails++
	if h.consecutiveFails == 1 {
		h.streakStart = now
	}
	if err != nil {
		h.lastErr = err.Error()
	}
	firstOfStreak := h.consecutiveFails == 1
	throttleElapsed := now.Sub(h.lastSummaryAt) >= h.throttle
	if !firstOfStreak && !throttleElapsed {
		return "", false
	}
	h.lastSummaryAt = now
	return fmt.Sprintf(
		"pg-keep-awake: OTel export UNHEALTHY: %d consecutive failure(s) since %s (%s ago); last error: %s",
		h.consecutiveFails, h.streakStart.Format(time.RFC3339), now.Sub(h.streakStart).Round(time.Second), h.lastErr,
	), true
}

// onSuccess records one successful export attempt. When it ends an unhealthy
// streak it returns a recovery line and true.
func (h *exportHealth) onSuccess() (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if h.consecutiveFails == 0 {
		return "", false
	}
	n, start, lastErr := h.consecutiveFails, h.streakStart, h.lastErr
	h.consecutiveFails = 0
	h.streakStart = time.Time{}
	h.lastSummaryAt = time.Time{}
	return fmt.Sprintf(
		"pg-keep-awake: OTel export RECOVERED after %d consecutive failure(s); was unhealthy for %s (since %s); last error: %s",
		n, now.Sub(start).Round(time.Second), start.Format(time.RFC3339), lastErr,
	), true
}

func (h *exportHealth) record(err error, out io.Writer) {
	var (
		line string
		emit bool
	)
	if err != nil {
		line, emit = h.onFailure(err)
	} else {
		line, emit = h.onSuccess()
	}
	if emit && out != nil {
		fmt.Fprintln(out, line)
	}
}

// healthMetricExporter decorates a metric Exporter (Decorator pattern), feeding
// each export result to the tracker and SWALLOWING the underlying error: the
// data is already lost when the collector is down, and returning nil keeps the
// SDK from routing every attempt to its default stderr handler.
type healthMetricExporter struct {
	sdkmetric.Exporter
	health *exportHealth
	out    io.Writer
}

func (e *healthMetricExporter) Export(ctx context.Context, rm *metricdata.ResourceMetrics) error {
	e.health.record(e.Exporter.Export(ctx, rm), e.out)
	return nil
}

// healthLogExporter is the log-exporter counterpart of healthMetricExporter.
type healthLogExporter struct {
	sdklog.Exporter
	health *exportHealth
	out    io.Writer
}

func (e *healthLogExporter) Export(ctx context.Context, records []sdklog.Record) error {
	e.health.record(e.Exporter.Export(ctx, records), e.out)
	return nil
}
