package serve_test

import (
	"context"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/telemetry"
)

// The expiry sweep runs the summary sweep, the job table's horizon prune and the
// eviction metric together, on the timer.
func TestSweep_PrunesJobRowsPastTheHorizonAndCountsTheEvictions(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seed3a})
	reader := sdkmetric.NewManualReader()
	p := runInProcess(t, d, func(o *serve.Options) {
		o.MeterProvider = sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		o.Hooks.SweepInterval = 200 * time.Millisecond
	})
	defer p.stop(t)
	settle(t, d)
	if got := countRows(t, d, "job"); got != 1 {
		t.Fatalf("fixture: want the failed run's job row, got %d", got)
	}

	// A job row of a run nobody has looked at since 1970 is past any horizon.
	db := d.WritableStore(t)
	if _, err := db.Exec(`INSERT INTO job (run_id, attempt, jobs, fetched_at) VALUES ('9999', 1, '[]', 1000)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	until(t, 20*time.Second, "the sweep to prune the old job row", func() bool {
		var n int
		if err := d.OpenStore(t).QueryRow(`SELECT COUNT(*) FROM job WHERE run_id = '9999'`).Scan(&n); err != nil {
			return false
		}
		return n == 0
	})
	if got := countRows(t, d, "job"); got != 1 {
		t.Errorf("job rows = %d after the sweep, want only the failed run's current row", got)
	}
	// The eviction was reported to the metric.
	var evictions int64
	until(t, 10*time.Second, "the store.evictions metric", func() bool {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatal(err)
		}
		evictions = 0
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if sum, ok := m.Data.(metricdata.Sum[int64]); ok && m.Name == telemetry.MetricStoreEvictions {
					for _, dp := range sum.DataPoints {
						evictions += dp.Value
					}
				}
			}
		}
		return evictions >= 1
	})
}
