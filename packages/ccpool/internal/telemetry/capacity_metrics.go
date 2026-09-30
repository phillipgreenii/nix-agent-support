// capacity_metrics.go — the ccpool_pool_capacity gauge (bead pg2-om899.6).
// Kept separate from metrics.go on purpose (that file's catalog is edited by
// sibling packets). Emission is opt-in: only `ccpool capacity --emit-metrics`
// calls RecordPoolCapacity, so the dispatch/admission-gate path that also runs
// `ccpool capacity --json` never emits.
package telemetry

import (
	"context"
	"path/filepath"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// PoolCapacityMetric is the exact OTLP instrument name of the pool-capacity
// gauge. Attributes: pool (pool dir BASENAME, never a path) and dim
// (max_sessions|live|preserved|counted|free).
const PoolCapacityMetric = "ccpool_pool_capacity"

// DefaultPoolName is the pool attribute value in default mode (empty pool root).
const DefaultPoolName = "default"

// PoolCapacity mirrors session.Capacity without importing it.
type PoolCapacity struct {
	MaxSessions, Live, Preserved, Counted, Free int64
}

// PoolName maps a canonical pool root to the bounded pool attribute value:
// its basename, or DefaultPoolName when root is empty.
func PoolName(root string) string {
	if root == "" {
		return DefaultPoolName
	}
	return filepath.Base(root)
}

// RecordPoolCapacityTo records one gauge point per dimension on meter.
func RecordPoolCapacityTo(ctx context.Context, meter metric.Meter, poolRoot string, c PoolCapacity) error {
	g, err := meter.Int64Gauge(PoolCapacityMetric,
		metric.WithDescription("ccpool pool occupancy by dimension (max_sessions|live|preserved|counted|free), snapshotted per opt-in capacity emission."))
	if err != nil {
		return err
	}
	pool := PoolName(poolRoot)
	for _, d := range []struct {
		dim string
		v   int64
	}{
		{"max_sessions", c.MaxSessions},
		{"live", c.Live},
		{"preserved", c.Preserved},
		{"counted", c.Counted},
		{"free", c.Free},
	} {
		g.Record(ctx, d.v, metric.WithAttributes(attribute.String("pool", pool), attribute.String("dim", d.dim)))
	}
	return nil
}

// RecordPoolCapacity records against the provider telemetry.Init installed.
func RecordPoolCapacity(poolRoot string, c PoolCapacity) error {
	return RecordPoolCapacityTo(context.Background(), MeterProvider().Meter(scopeName), poolRoot, c)
}
