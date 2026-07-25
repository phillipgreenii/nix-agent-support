package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/otel"
)

// Metric instrument names recorded by the daemon tick (see internal/otel
// emitter.go registerMetrics and daemon/lifecycle.go runTick).
const (
	tickDurationMetric  = "pa_monitor.poll.tick.duration"
	phaseDurationMetric = "pa_monitor.poll.phase.duration"
)

// erroringPoller is a PollerInterface whose Snapshot always fails, driving the
// tick's snapshot-error early-exit (lifecycle.go: the `if err != nil { return }`
// immediately after opts.Poller.Snapshot). It records nothing else, so the tick
// returns before any daemon phase (db_write_block/nudge) can fire.
type erroringPoller struct{}

func (erroringPoller) Snapshot(context.Context) (*aggregate.Tree, bool, error) {
	return nil, false, errors.New("snapshot boom")
}

// newReaderEmitter builds a ManualReader-backed metrics Emitter so a test can
// observe the daemon-tick tick/phase-duration recordings in-process, without a
// live OTLP collector (mirrors internal/otel's newTestEmitter pattern, reused
// here across the package boundary via the exported otel.NewWithReader).
func newReaderEmitter(t *testing.T) (*otel.Emitter, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	e, err := otel.NewWithReader(reader)
	if err != nil {
		t.Fatalf("otel.NewWithReader: %v", err)
	}
	return e, reader
}

// startDaemon runs RunWith in the background with a fast tick and returns its
// done channel plus a stop() that cancels and waits for it to return. RunWith
// shuts the Emitter's MeterProvider down on return, so ALL metric collection
// MUST happen before stop() is called.
func startDaemon(t *testing.T, opts RunOptions) (<-chan error, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunWith(ctx, opts) }()
	stop := func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("RunWith returned err: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("RunWith did not return after cancel")
		}
	}
	return done, stop
}

// tickOptions returns a minimal RunOptions for a fast-ticking daemon wired to
// the given (possibly nil) poller and the reader-backed Emitter.
func tickOptions(t *testing.T, e *otel.Emitter, poller PollerInterface) RunOptions {
	t.Helper()
	dir := shortTempDir(t)
	return RunOptions{
		Paths:   Paths{Dir: dir, PIDFile: filepath.Join(dir, "daemon.pid"), Socket: filepath.Join(dir, "daemon.sock")},
		Emitter: e,
		Tick:    5 * time.Millisecond,
		Poller:  poller,
	}
}

// collectUntil polls the ManualReader until pred is satisfied or timeout, then
// returns the last collected metrics. A startup failure surfaces as a timeout
// here; the deferred stop() then reports RunWith's real error.
func collectUntil(t *testing.T, reader *sdkmetric.ManualReader, timeout time.Duration, pred func(metricdata.ResourceMetrics) bool) metricdata.ResourceMetrics {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var rm metricdata.ResourceMetrics
	for time.Now().Before(deadline) {
		rm = metricdata.ResourceMetrics{}
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatalf("reader.Collect: %v", err)
		}
		if pred(rm) {
			return rm
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v; last metrics=%+v", timeout, rm.ScopeMetrics)
	return rm
}

// histTotalCount sums the data-point counts of the named float histogram across
// all its data points (attribute sets), or 0 if the instrument recorded nothing.
func histTotalCount(rm metricdata.ResourceMetrics, name string) uint64 {
	var total uint64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			if h, ok := m.Data.(metricdata.Histogram[float64]); ok {
				for _, dp := range h.DataPoints {
					total += dp.Count
				}
			}
		}
	}
	return total
}

// phaseCount sums the counts of the phase-duration histogram data points whose
// `phase` attribute equals the given phase label.
func phaseCount(rm metricdata.ResourceMetrics, phase string) uint64 {
	var total uint64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != phaseDurationMetric {
				continue
			}
			if h, ok := m.Data.(metricdata.Histogram[float64]); ok {
				for _, dp := range h.DataPoints {
					if v, present := dp.Attributes.Value("phase"); present && v.AsString() == phase {
						total += dp.Count
					}
				}
			}
		}
	}
	return total
}

// TestTickDuration_RecordsOnNoPollerEarlyExit pins that RecordTickDuration fires
// on the no-poller early-exit tick (lifecycle.go runTick: `if opts.Poller == nil
// { ...; return }`). The tick returns before any phase, so the phase-duration
// instrument must record nothing.
func TestTickDuration_RecordsOnNoPollerEarlyExit(t *testing.T) {
	e, reader := newReaderEmitter(t)
	_, stop := startDaemon(t, tickOptions(t, e, nil))
	defer stop()

	// Wait for >= 2 completed ticks so the first tick's deferred duration record
	// is definitely visible (the defer fires at the end of runTick).
	rm := collectUntil(t, reader, 3*time.Second, func(rm metricdata.ResourceMetrics) bool {
		return histTotalCount(rm, tickDurationMetric) >= 2
	})

	if got := histTotalCount(rm, tickDurationMetric); got < 1 {
		t.Errorf("tick.duration count = %d, want >= 1 on the no-poller early-exit path", got)
	}
	if got := histTotalCount(rm, phaseDurationMetric); got != 0 {
		t.Errorf("phase.duration count = %d, want 0 (no phases on the no-poller early-exit path)", got)
	}
}

// TestTickDuration_RecordsOnSnapshotErrorEarlyExit pins that RecordTickDuration
// fires on the snapshot-error early-exit tick (lifecycle.go runTick: the
// `tree, _, err := opts.Poller.Snapshot(ctx); if err != nil { return }`). The
// tick returns before db_write_block/nudge, so no phase is recorded.
func TestTickDuration_RecordsOnSnapshotErrorEarlyExit(t *testing.T) {
	e, reader := newReaderEmitter(t)
	_, stop := startDaemon(t, tickOptions(t, e, erroringPoller{}))
	defer stop()

	rm := collectUntil(t, reader, 3*time.Second, func(rm metricdata.ResourceMetrics) bool {
		return histTotalCount(rm, tickDurationMetric) >= 2
	})

	if got := histTotalCount(rm, tickDurationMetric); got < 1 {
		t.Errorf("tick.duration count = %d, want >= 1 on the snapshot-error early-exit path", got)
	}
	if got := histTotalCount(rm, phaseDurationMetric); got != 0 {
		t.Errorf("phase.duration count = %d, want 0 (tick returns before any phase on snapshot error)", got)
	}
}

// TestTick_RecordsDaemonPhases pins the daemon-OWNED lifecycle phases on a
// full (non-erroring) tick: db_write_block (lifecycle.go ~:608) and nudge
// (~:858) both record, alongside the tick duration. Note the `limits`/`weekly`
// phases are PRODUCER-owned (internal/core/poller/producer.go fires them
// off-tick) — not daemon-tick phases — so they are intentionally NOT asserted
// here; the poller side owns their coverage.
func TestTick_RecordsDaemonPhases(t *testing.T) {
	e, reader := newReaderEmitter(t)
	// fakeMonitorPoller (lifecycle_monitor_tick_test.go) returns a valid tree
	// without spawning a producer, so only the daemon-tick phases fire.
	p := &fakeMonitorPoller{}
	_, stop := startDaemon(t, tickOptions(t, e, p))
	defer stop()

	rm := collectUntil(t, reader, 3*time.Second, func(rm metricdata.ResourceMetrics) bool {
		return phaseCount(rm, "db_write_block") >= 1 &&
			phaseCount(rm, "nudge") >= 1 &&
			histTotalCount(rm, tickDurationMetric) >= 1
	})

	if got := phaseCount(rm, "db_write_block"); got < 1 {
		t.Errorf("phase=db_write_block count = %d, want >= 1", got)
	}
	if got := phaseCount(rm, "nudge"); got < 1 {
		t.Errorf("phase=nudge count = %d, want >= 1", got)
	}
	if got := histTotalCount(rm, tickDurationMetric); got < 1 {
		t.Errorf("tick.duration count = %d, want >= 1 on a full tick", got)
	}
}
