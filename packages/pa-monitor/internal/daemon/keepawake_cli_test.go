package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/caffeinate"
	"github.com/phillipgreenii/pa-monitor/internal/core/caffeinate/keepertest"
)

// TestMain builds the real pg-keep-awake once for the package's tests (see
// keepertest); the tests that need it skip when its source is not present.
func TestMain(m *testing.M) {
	os.Exit(keepertest.Main(m))
}

// noopKeeper is a caffeinate.Keeper that does nothing, for tests that drive the
// tick loop but do not care about the keeper.
type noopKeeper struct{}

func (noopKeeper) Acquire(int) error               { return nil }
func (noopKeeper) Release() error                  { return nil }
func (noopKeeper) Observe() caffeinate.Observation { return caffeinate.Observation{} }

const (
	activeMetric       = "pa_monitor.caffeinate.active"
	keeperUpMetric     = "pa_monitor.keep_awake.keeper_up"
	batteryReleaseName = "pa_monitor.caffeinate.battery_releases_total"
)

// TestRunWith_KeeperDrivesCaffeinateMetrics drives the real tick loop against
// the REAL pg-keep-awake CLI (a keeper's files are simulated, as the keeper
// process is not under test): a working session with the toggle on writes
// pa-monitor's lease; pa_monitor.caffeinate.active follows the keeper's
// assertion.held from status --json, not pa-monitor's own idea of what it asked
// for; pa_monitor.keep_awake.keeper_up follows status --check (1 for exit 0, 0
// for exit 5); and the retired battery counter is never emitted.
func TestRunWith_KeeperDrivesCaffeinateMetrics(t *testing.T) {
	bin := keepertest.Bin(t)
	stateDir := keepertest.UseStateDir(t)
	mgr := &caffeinate.Manager{
		Grace:  time.Minute,
		Keeper: &caffeinate.CLI{Configured: bin},
		Now:    time.Now,
		PID:    os.Getpid(),
	}
	e, reader := newReaderEmitter(t)
	opts := tickOptions(t, e, &stubPoller{snapshot: func(context.Context) (*aggregate.Tree, bool, error) {
		return workingTree(), false, nil
	}})
	opts.Caffeinate = mgr
	opts.InitialCaffeinateOn = true
	opts.PlanTier = "max_5x"
	_, stop := startDaemon(t, opts)
	defer stop()

	leaseFile := filepath.Join(stateDir, "leases", caffeinate.Owner+".default.json")

	// No keeper yet: the lease is written (acquire never waits on the keeper),
	// nothing is held, and the keeper is reported down (status --check exit 5).
	collectUntil(t, reader, 5*time.Second, func(rm metricdata.ResourceMetrics) bool {
		up, upOK := metricInt64(rm, keeperUpMetric, "", "")
		a, aOK := metricInt64(rm, activeMetric, "", "")
		_, leaseErr := os.Stat(leaseFile)
		return upOK && up == 0 && aOK && a == 0 && leaseErr == nil
	})

	// The keeper comes up and holds the assertion.
	keepertest.SimulateKeeper(t, stateDir, keepertest.KeeperState{Held: true})
	collectUntil(t, reader, 5*time.Second, func(rm metricdata.ResourceMetrics) bool {
		up, upOK := metricInt64(rm, keeperUpMetric, "", "")
		a, aOK := metricInt64(rm, activeMetric, "", "")
		return upOK && up == 1 && aOK && a == 1
	})

	// The keeper withholds the assertion for a low battery: the gauge reads 0
	// although the toggle is on and a working session is wanted.
	keepertest.SimulateKeeper(t, stateDir, keepertest.KeeperState{Held: false, Gate: "released", OnBattery: true, Percent: 15})
	rm := collectUntil(t, reader, 5*time.Second, func(rm metricdata.ResourceMetrics) bool {
		a, aOK := metricInt64(rm, activeMetric, "", "")
		return aOK && a == 0
	})
	if _, found := metricInt64(rm, batteryReleaseName, "", ""); found {
		t.Errorf("%s is still emitted; the keeper owns the battery rule now", batteryReleaseName)
	}
}
