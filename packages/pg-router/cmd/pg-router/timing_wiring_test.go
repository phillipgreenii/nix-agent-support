package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/metrics"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// Wiring tests for the dispatch timeline (bead pg2-n7da9): the fan-out observer
// forwards the timing hook, the queue-wait / run histograms and the two new gauges
// reach a real scrape, and bootCore wires the gauges to the live queue.

// timingRecorder is an eventqueue.Observer that also implements TimingObserver.
type timingRecorder struct {
	recordingDispatchFailureObserver
	timings []eventqueue.DispatchTiming
}

func (r *timingRecorder) OnAcceptTiming(t eventqueue.DispatchTiming) {
	r.timings = append(r.timings, t)
}

func TestFanOutObserver_OnAcceptTimingForwardsToImplementingArmsOnly(t *testing.T) {
	a := &timingRecorder{}
	b := &recordingDispatchFailureObserver{} // no TimingObserver: must be skipped, not panic
	f := fanOutObserver{a, b}
	tm := eventqueue.DispatchTiming{EventID: "e1", EventType: "pr.changed", ListenerID: "desk-pr"}
	f.OnAcceptTiming(tm)
	if len(a.timings) != 1 || a.timings[0] != tm {
		t.Fatalf("a.timings = %+v, want the one timing", a.timings)
	}
	// And through a nested fan-out, as bootCore builds it.
	c := &timingRecorder{}
	nested := fanOutObserver{b, fanOutObserver{a, c}}
	nested.OnAcceptTiming(tm)
	if len(a.timings) != 2 || len(c.timings) != 1 {
		t.Fatalf("nested forwarding: a=%d c=%d, want 2 and 1", len(a.timings), len(c.timings))
	}
}

func TestStartMetricsServer_ExposesWaitRunHistogramsAndNewGauges(t *testing.T) {
	var ln net.Listener
	mp, shutdown, err := startMetricsServer("127.0.0.1:0", func(l net.Listener) { ln = l })
	if err != nil {
		t.Fatalf("startMetricsServer: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = shutdown(ctx)
	})
	emitter, err := metrics.New(mp, func() map[string]int { return map[string]int{"pr.changed": 2} },
		metrics.WithQueueAges(func() map[string]time.Duration { return map[string]time.Duration{"pr.changed": 42 * time.Second} }),
		metrics.WithListenerInFlight(func() map[string]int { return map[string]int{"desk-pr": 1} }))
	if err != nil {
		t.Fatalf("metrics.New: %v", err)
	}
	base := time.Now()
	emitter.OnAcceptTiming(eventqueue.DispatchTiming{
		EventType: "pr.reconcile", ListenerID: "desk-pr",
		EnqueuedAt: base, StartedAt: base.Add(10 * time.Second), SettledAt: base.Add(30 * time.Second),
	})
	if err := forceFlush(context.Background(), mp); err != nil {
		t.Fatal(err)
	}
	body := scrape(t, fmt.Sprintf("http://%s/metrics", ln.Addr().String()))
	for _, w := range []string{
		`pg_router_queue_wait_seconds_bucket{`,
		`pg_router_run_seconds_bucket{`,
		`type="pr.reconcile"`,
		`role="desk-pr"`,
		`pg_router_queue_oldest_age_seconds{`,
		`pg_router_listener_in_flight{`,
		// the deprecated histogram is still exposed alongside them
	} {
		if !strings.Contains(body, w) {
			t.Errorf("scrape missing %q:\n%s", w, body)
		}
	}
}

func TestBootCore_WiresOldestAgeAndInFlightGaugesToTheLiveQueue(t *testing.T) {
	cfg := config.Config{
		LogDir: shortDir(t),
		Roles:  roles.RoleSet{{Name: "r1", Enabled: true, Binds: []string{"t1"}}},
	}
	o := &orchestrator.Orchestrator{Cfg: cfg}
	ctx := context.Background()
	svc, q, _, storeClose, err := bootCore(ctx, cfg, o, cfg.Roles, runExclusions{}, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	past := time.Now().Add(-2 * time.Minute)
	if _, err := q.Enqueue(eventqueue.Event{ID: "e1", Type: "t1", At: past, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	rm, err := svc.MetricsReader().Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var age float64 = -1
	busy := int64(-1)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Gauge[float64]:
				if m.Name == metrics.MetricQueueOldestAge {
					for _, dp := range d.DataPoints {
						if v, ok := dp.Attributes.Value(attribute.Key("type")); ok && v.AsString() == "t1" {
							age = dp.Value
						}
					}
				}
			case metricdata.Gauge[int64]:
				if m.Name == metrics.MetricListenerInFlight {
					for _, dp := range d.DataPoints {
						if v, ok := dp.Attributes.Value(attribute.Key("role")); ok && v.AsString() == "r1" {
							busy = dp.Value
						}
					}
				}
			}
		}
	}
	if age < 119 || age > 180 {
		t.Fatalf("%s{type=t1} = %v, want ~120s (event enqueued two minutes ago, still owed to r1)", metrics.MetricQueueOldestAge, age)
	}
	if busy != 0 {
		t.Fatalf("%s{role=r1} = %d, want 0 (registered and idle)", metrics.MetricListenerInFlight, busy)
	}
}
