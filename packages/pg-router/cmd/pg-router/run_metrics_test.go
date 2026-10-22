package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/metrics"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// Tests for the metric callbacks bootCore registers before it has a queue to read
// (bead pg2-nzkjl, DEC-EVENT-5's residual). The daemon starts serving /metrics BEFORE
// bootCore runs (runRun's startMetricsServer), and bootCore registers the gauge callbacks
// (metrics.New) before it constructs the queue they read (eventqueue.New, whose startup
// compaction alone held the gap open for 6.03 s on 2026-10-12), so a scrape can land
// between the two.

// queueBoundMetrics are the members that read the queue on each collect: the per-type
// depth and its scalar sum, the oldest-owed age, the per-role in-flight gauge, the
// event-log size and limit gauges, and the Gate Registry's active-gate gauge.
var queueBoundMetrics = []string{
	metrics.MetricQueueDepth,
	metrics.MetricBacklog,
	metrics.MetricQueueOldestAge,
	metrics.MetricListenerInFlight,
	metrics.MetricQueueLogBytes,
	metrics.MetricQueueLogLimitBytes,
	metrics.MetricQueueLogPercent,
	metrics.MetricEmittersHalted,
	metrics.MetricLogRejecting,
	metrics.MetricActiveGates,
}

// startupCompactedHandler is a slog.Handler that runs onWindow, once and on the calling
// goroutine, when the queue reports the startup compaction of the log that was exactly
// logBytes long. eventqueue.New logs that record from inside its own body: after bootCore
// registered the metric callbacks and before bootCore assigned the queue. onWindow therefore
// runs INSIDE the window, as a scrape landing in the real compaction does, with no sleep, no
// polling loop and no second goroutine. Every other record is dropped.
type startupCompactedHandler struct {
	logBytes int64
	onWindow func()
	once     *sync.Once
}

func (h startupCompactedHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h startupCompactedHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "eventqueue: queue log compacted" {
		return nil
	}
	trigger, before := "", ""
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "trigger":
			trigger = a.Value.String()
		case "bytesBefore":
			before = a.Value.String()
		}
		return true
	})
	if trigger == "startup" && before == strconv.FormatInt(h.logBytes, 10) {
		h.once.Do(h.onWindow)
	}
	return nil
}

func (h startupCompactedHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h startupCompactedHandler) WithGroup(string) slog.Handler      { return h }

// gaugePointCount is how many data points the named gauge (int64 or float64) has in rm.
func gaugePointCount(rm metricdata.ResourceMetrics, name string) int {
	n := 0
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			switch g := m.Data.(type) {
			case metricdata.Gauge[int64]:
				n += len(g.DataPoints)
			case metricdata.Gauge[float64]:
				n += len(g.DataPoints)
			}
		}
	}
	return n
}

// int64GaugePoint returns the named int64 gauge's data point carrying key=value, and
// whether there is one.
func int64GaugePoint(rm metricdata.ResourceMetrics, name, key, value string) (int64, bool) {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok || m.Name != name {
				continue
			}
			for _, dp := range g.DataPoints {
				if v, present := dp.Attributes.Value(attribute.Key(key)); present && v.AsString() == value {
					return dp.Value, true
				}
			}
		}
	}
	return 0, false
}

// A collect that lands after bootCore registered the metric callbacks and before it built
// the queue MUST NOT panic, and MUST read nothing for the queue-bound gauges: no series at
// all, not a zero, because no queue means no reading (DEC-EVENT-5). Before the fix the
// callbacks closed over bootCore's still-nil queue and dereferenced it. Once bootCore has
// returned, the first collect carries every one of those series, so a role's in-flight
// gauge reads 0 (present) rather than missing from then on.
func TestBootCore_ACollectBeforeTheQueueExistsReadsNothing(t *testing.T) {
	dir := shortDir(t)
	at := time.Now().UTC()
	var seed bytes.Buffer
	enc := json.NewEncoder(&seed)
	for _, rec := range []map[string]any{
		{"op": "enqueue", "eventId": "live-1", "type": "t1", "at": at, "expiresAt": at.Add(time.Hour), "enqueuedAt": at},
		{"op": "gate_set", "eventId": "", "gateType": "SYSTEM_PAUSE", "at": at, "owner": "operator"},
	} {
		if err := enc.Encode(rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "queue.jsonl"), seed.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	reader := sdkmetric.NewManualReader()
	cfg := config.Config{
		LogDir:        dir,
		MeterProvider: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)),
		Roles:         roles.RoleSet{{Name: "r1", Enabled: true, Binds: []string{"t1"}}},
	}

	var inWindow metricdata.ResourceMetrics
	attempted := false
	collectInWindow := func() {
		attempted = true
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("a collect that landed before bootCore built the queue panicked: %v\n%s", r, debug.Stack())
			}
		}()
		if err := reader.Collect(context.Background(), &inWindow); err != nil {
			t.Errorf("a collect that landed before bootCore built the queue failed: %v", err)
		}
	}
	prev := slog.Default()
	slog.SetDefault(slog.New(startupCompactedHandler{logBytes: int64(seed.Len()), onWindow: collectInWindow, once: &sync.Once{}}))
	t.Cleanup(func() { slog.SetDefault(prev) })

	o := &orchestrator.Orchestrator{Cfg: cfg}
	svc, _, _, storeClose, err := bootCore(context.Background(), cfg, o, cfg.Roles, runExclusions{}, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	if !attempted {
		t.Fatal("the startup compaction record never ran the in-window collect, so this test proved nothing")
	}
	for _, name := range queueBoundMetrics {
		if n := gaugePointCount(inWindow, name); n != 0 {
			t.Errorf("%s has %d data point(s) in a collect before the queue exists; want none (not a reading, and not a zero)", name, n)
		}
	}

	var built metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &built); err != nil {
		t.Fatalf("collect after bootCore: %v", err)
	}
	for _, name := range queueBoundMetrics {
		if gaugePointCount(built, name) == 0 {
			t.Errorf("%s has no data point in the first collect after bootCore returned; want its series", name)
		}
	}
	if got, ok := int64GaugePoint(built, metrics.MetricQueueDepth, "type", "t1"); !ok || got != 1 {
		t.Errorf("%s{type=t1} = %d (present %v), want 1: the restored event", metrics.MetricQueueDepth, got, ok)
	}
	if got, ok := int64GaugePoint(built, metrics.MetricListenerInFlight, "role", "r1"); !ok || got != 0 {
		t.Errorf("%s{role=r1} = %d (present %v), want 0: registered, idle, and present", metrics.MetricListenerInFlight, got, ok)
	}
	if got, ok := int64GaugePoint(built, metrics.MetricActiveGates, "type", "SYSTEM_PAUSE"); !ok || got != 1 {
		t.Errorf("%s{type=SYSTEM_PAUSE} = %d (present %v), want 1: the restored gate", metrics.MetricActiveGates, got, ok)
	}
}

// The per-role progress gauges (docket pg2-12gr4.8) reach a real scrape of a running core:
// a role whose handler exits 9 with a decline body of kind fault is counted by Offer into
// pg_router_role_unsuccessful_since_progress{role}, and its age gauge exists too, with the
// busy count still 0. The handler is a real subprocess behind the real wire client, so the
// exit-9 body decoding (reason, kind) is what is under test, not a fake of it. The scrape
// also proves the gauges start at daemon start for an idle role.
func TestBootCore_RoleProgressGaugesReachAScrapeAfterAFaultDecline(t *testing.T) {
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

	cfg := config.Config{
		LogDir:        shortDir(t),
		MeterProvider: mp,
		Roles: roles.RoleSet{
			{Name: "r1", Enabled: true, Binds: []string{"t1"}},
			{Name: "idle", Enabled: true, Binds: []string{"t2"}},
		},
	}
	stub := []string{"/bin/sh", "-c", `printf '%s' '{"schemaVersion":"1","reason":"x","kind":"fault"}'; exit 9`}
	o := &orchestrator.Orchestrator{Cfg: cfg, Handler: wireclient.New(func(roles.Role, string) ([]string, error) {
		return slices.Clone(stub), nil
	})}
	ctx := context.Background()
	svc, q, _, storeClose, err := bootCore(ctx, cfg, o, cfg.Roles, runExclusions{}, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	if _, err := q.Enqueue(eventqueue.Event{ID: "e1", Type: "t1", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	q.Dispatch() // one pass: one offer to r1, declined fault

	if err := forceFlush(ctx, mp); err != nil {
		t.Fatal(err)
	}
	body := scrape(t, fmt.Sprintf("http://%s/metrics", ln.Addr().String()))
	for _, tc := range []struct {
		name string
		role string
		want float64
	}{
		{"pg_router_role_unsuccessful_since_progress", "r1", 1},
		{"pg_router_role_busy_since_progress", "r1", 0},
		{"pg_router_role_unsuccessful_since_progress", "idle", 0},
		{"pg_router_role_busy_since_progress", "idle", 0},
	} {
		got := seriesValue(body, tc.name, map[string]string{"role": tc.role})
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s{role=%q} = %v, want %v:\n%s", tc.name, tc.role, got, tc.want, body)
		}
	}
	for _, role := range []string{"r1", "idle"} {
		if got := seriesValue(body, "pg_router_role_last_progress_age_seconds", map[string]string{"role": role}); len(got) != 1 {
			t.Errorf("pg_router_role_last_progress_age_seconds{role=%q} = %v, want one series:\n%s", role, got, body)
		}
	}
}

// The daemon's live collector is the OTel Prometheus exporter behind promhttp, and a panic
// in a gauge callback there fails the scrape that ran it (HTTP 500) and nothing else: the
// process, the listener and the SDK's state survive and the next scrape reads normally.
// This is the severity of the nil-queue window above, pinned so a dependency upgrade that
// stops recovering the panic fails here instead of crashing the daemon on a restart.
//
// Why it is a failed scrape: sdk/metric v1.46.0's pipeline.produce runs the callbacks with
// no recover (its deferred unlock still runs), as does exporters/prometheus v0.56.0's
// Collect, but client_golang v1.24.1's Registry.Gather runs each collector through
// safeCollect, which turns a panic into an error, and promhttp.HandlerFor with the zero
// HandlerOpts (HTTPErrorOnError) answers an error with a 500.
func TestStartMetricsServer_APanickingGaugeCallbackFailsOneScrapeNotTheProcess(t *testing.T) {
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

	var queue *eventqueue.Queue // the defect's own shape: a callback reading a queue that is not there
	var broken atomic.Bool
	broken.Store(true)
	if _, err := mp.Meter("test").Int64ObservableGauge("pg_router_test_callback_probe",
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			if broken.Load() {
				o.Observe(int64(len(queue.DepthByType())))
				return nil
			}
			o.Observe(1)
			return nil
		})); err != nil {
		t.Fatalf("Int64ObservableGauge: %v", err)
	}

	get := func() (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+ln.Addr().String()+"/metrics", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET /metrics: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return resp.StatusCode, string(b)
	}

	status, body := get()
	if status != http.StatusInternalServerError || !strings.Contains(body, "panic recovered") || !strings.Contains(body, "nil pointer dereference") {
		t.Fatalf("scrape with a panicking callback = %d, want 500 naming the recovered nil dereference:\n%s", status, body)
	}

	broken.Store(false)
	status, body = get()
	if status != http.StatusOK || !strings.Contains(body, `pg_router_test_callback_probe{otel_scope_name="test",otel_scope_version=""} 1`) {
		t.Fatalf("scrape after the callback stopped panicking = %d, want 200 with the gauge (the process must have survived):\n%s", status, body)
	}
}
