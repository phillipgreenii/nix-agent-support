// gauge_metrics.go — the generic gauge emitter behind `ccpool emit-gauge`
// (bead pg2-fx5x0l.8). The metric name, labels and value are all caller
// supplied, so ccpool stays ignorant of the concepts (e.g. a handler's
// janitor) whose heartbeat gauges flow through it.
package telemetry

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	metricNameRE = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	labelNameRE  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// RecordGaugeTo records one float64 gauge point named name, with labels as
// attributes, on meter. name and label names must be valid Prometheus
// identifiers (the gauge ends up in Prometheus); anything else is rejected
// with an error rather than silently mangled downstream.
func RecordGaugeTo(ctx context.Context, meter metric.Meter, name string, value float64, labels map[string]string) error {
	if !metricNameRE.MatchString(name) {
		return fmt.Errorf("invalid metric name %q", name)
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		if !labelNameRE.MatchString(k) {
			return fmt.Errorf("invalid label name %q", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	attrs := make([]attribute.KeyValue, 0, len(keys))
	for _, k := range keys {
		attrs = append(attrs, attribute.String(k, labels[k]))
	}
	g, err := meter.Float64Gauge(name, metric.WithDescription("ccpool generic gauge emitted via `ccpool emit-gauge`."))
	if err != nil {
		return err
	}
	g.Record(ctx, value, metric.WithAttributes(attrs...))
	return nil
}

// RecordGauge records against the provider telemetry.Init installed.
func RecordGauge(name string, value float64, labels map[string]string) error {
	return RecordGaugeTo(context.Background(), MeterProvider().Meter(scopeName), name, value, labels)
}
