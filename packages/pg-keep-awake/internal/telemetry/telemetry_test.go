package telemetry

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// memLogs is an in-memory log exporter.
type memLogs struct {
	mu   sync.Mutex
	recs []sdklog.Record
}

func (m *memLogs) Export(_ context.Context, r []sdklog.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range r {
		m.recs = append(m.recs, x.Clone())
	}
	return nil
}
func (m *memLogs) Shutdown(context.Context) error   { return nil }
func (m *memLogs) ForceFlush(context.Context) error { return nil }

func newTest(t *testing.T) (Emitter, *sdkmetric.ManualReader, *memLogs) {
	t.Helper()
	r := sdkmetric.NewManualReader()
	logs := &memLogs{}
	e, err := NewWithReader(r, sdklog.NewSimpleProcessor(logs))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	return e, r, logs
}

func collect(t *testing.T, r *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

// labelSets returns the sorted, "k=v,k=v" rendered attribute sets of a metric,
// and a map of those sets to their int64 values (gauge or counter).
func labelSets(t *testing.T, m metricdata.Metrics) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	add := func(a attribute.Set, v int64) {
		kvs := []string{}
		for _, kv := range a.ToSlice() {
			kvs = append(kvs, string(kv.Key)+"="+kv.Value.String())
		}
		sort.Strings(kvs)
		out[strings.Join(kvs, ",")] = v
	}
	switch d := m.Data.(type) {
	case metricdata.Gauge[int64]:
		for _, p := range d.DataPoints {
			add(p.Attributes, p.Value)
		}
	case metricdata.Sum[int64]:
		for _, p := range d.DataPoints {
			add(p.Attributes, p.Value)
		}
	default:
		t.Fatalf("%s: unexpected data type %T", m.Name, m.Data)
	}
	return out
}

func TestEveryMetricNameKindAndLabels(t *testing.T) {
	e, r, _ := newTest(t)
	e.RecordKeeperUp(true)
	e.RecordTick(250*time.Millisecond, time.Unix(1700000000, 0))
	e.RecordLeasesLive(map[string]int{"alice": 2})
	e.IncLeasesReaped("expired")
	e.IncLeasesRejected("too_large")
	e.RecordAssertion(true, []string{"idle"})
	e.RecordGateState("ok")
	e.RecordBattery(55, true)
	e.RecordReleasePct(20)
	e.IncBatteryRelease("spawn_blocked")
	e.IncAnchorExit("watchdog")

	got := collect(t, r)
	type want struct {
		kind   string // gauge, counter, histogram
		labels []string
	}
	table := map[string]want{
		"pg_keep_awake.keeper.up":                          {"gauge", nil},
		"pg_keep_awake.keeper.last_tick_timestamp_seconds": {"gauge", nil},
		"pg_keep_awake.tick.duration":                      {"histogram", nil},
		"pg_keep_awake.leases.live":                        {"gauge", []string{"owner"}},
		"pg_keep_awake.leases.reaped_total":                {"counter", []string{"reason"}},
		"pg_keep_awake.leases.rejected_total":              {"counter", []string{"reason"}},
		"pg_keep_awake.assertion.held":                     {"gauge", nil},
		"pg_keep_awake.assertion.flags":                    {"gauge", []string{"flag"}},
		"pg_keep_awake.gate.state":                         {"gauge", []string{"state"}},
		"pg_keep_awake.battery.percent":                    {"gauge", nil},
		"pg_keep_awake.battery.on_battery":                 {"gauge", nil},
		"pg_keep_awake.battery.release_pct":                {"gauge", nil},
		"pg_keep_awake.battery.releases_total":             {"counter", []string{"reason"}},
		"pg_keep_awake.anchor.exits_total":                 {"counter", []string{"reason"}},
	}
	if len(got) != len(table) {
		t.Errorf("emitted %d metrics, want %d: %v", len(got), len(table), keys(got))
	}
	for name, w := range table {
		m, ok := got[name]
		if !ok {
			t.Errorf("%s: not emitted", name)
			continue
		}
		var kind string
		var attrSets []attribute.Set
		switch d := m.Data.(type) {
		case metricdata.Gauge[int64]:
			kind = "gauge"
			for _, p := range d.DataPoints {
				attrSets = append(attrSets, p.Attributes)
			}
		case metricdata.Gauge[float64]:
			kind = "gauge"
			for _, p := range d.DataPoints {
				attrSets = append(attrSets, p.Attributes)
			}
		case metricdata.Sum[int64]:
			if !d.IsMonotonic {
				t.Errorf("%s: counter is not monotonic", name)
			}
			kind = "counter"
			for _, p := range d.DataPoints {
				attrSets = append(attrSets, p.Attributes)
			}
		case metricdata.Histogram[float64]:
			kind = "histogram"
			for _, p := range d.DataPoints {
				attrSets = append(attrSets, p.Attributes)
			}
		default:
			t.Errorf("%s: unexpected data type %T", name, m.Data)
			continue
		}
		if kind != w.kind {
			t.Errorf("%s: kind %s, want %s", name, kind, w.kind)
		}
		for _, as := range attrSets {
			var keys []string
			for _, kv := range as.ToSlice() {
				keys = append(keys, string(kv.Key))
			}
			sort.Strings(keys)
			if strings.Join(keys, ",") != strings.Join(w.labels, ",") {
				t.Errorf("%s: label keys %v, want %v", name, keys, w.labels)
			}
		}
	}
}

func keys(m map[string]metricdata.Metrics) []string {
	var k []string
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}

func TestGaugeAndCounterValues(t *testing.T) {
	e, r, _ := newTest(t)
	e.RecordKeeperUp(true)
	e.RecordBattery(55, true)
	e.RecordReleasePct(20)
	e.RecordAssertion(true, nil)
	e.IncLeasesReaped("expired")
	e.IncLeasesReaped("expired")
	e.IncLeasesReaped("bogus-reason")
	e.IncAnchorExit("not-a-reason")
	e.RecordGateState("no_spawn")
	got := collect(t, r)

	for name, want := range map[string]int64{
		"pg_keep_awake.keeper.up":           1,
		"pg_keep_awake.battery.percent":     55,
		"pg_keep_awake.battery.on_battery":  1,
		"pg_keep_awake.battery.release_pct": 20,
		"pg_keep_awake.assertion.held":      1,
	} {
		if v := labelSets(t, got[name])[""]; v != want {
			t.Errorf("%s = %d, want %d", name, v, want)
		}
	}
	reaped := labelSets(t, got["pg_keep_awake.leases.reaped_total"])
	if reaped["reason=expired"] != 2 || reaped["reason=other"] != 1 || reaped["reason=orphan"] != 0 {
		t.Errorf("reaped = %v", reaped)
	}
	if v := labelSets(t, got["pg_keep_awake.anchor.exits_total"])["reason=unknown"]; v != 1 {
		t.Errorf("unlisted anchor reason should map to unknown, got %d", v)
	}
	gate := labelSets(t, got["pg_keep_awake.gate.state"])
	for _, s := range GateStates {
		want := int64(0)
		if s == "no_spawn" {
			want = 1
		}
		if gate["state="+s] != want {
			t.Errorf("gate %s = %d, want %d", s, gate["state="+s], want)
		}
	}
}

func TestTickRecordsDurationAndTimestamp(t *testing.T) {
	e, r, _ := newTest(t)
	e.RecordTick(1500*time.Millisecond, time.Unix(1700000000, 500_000_000))
	got := collect(t, r)
	h := got["pg_keep_awake.tick.duration"].Data.(metricdata.Histogram[float64])
	if h.DataPoints[0].Count != 1 || h.DataPoints[0].Sum != 1.5 {
		t.Errorf("histogram count/sum = %d/%v", h.DataPoints[0].Count, h.DataPoints[0].Sum)
	}
	g := got["pg_keep_awake.keeper.last_tick_timestamp_seconds"].Data.(metricdata.Gauge[float64])
	if g.DataPoints[0].Value != 1700000000.5 {
		t.Errorf("timestamp = %v", g.DataPoints[0].Value)
	}
}

func TestOwnerLabelCappedAtSixteen(t *testing.T) {
	e, r, _ := newTest(t)
	by := map[string]int{}
	for i := 0; i < 17; i++ {
		by[fmt.Sprintf("owner-%02d", i)] = 1
	}
	e.RecordLeasesLive(by)
	live := labelSets(t, collect(t, r)["pg_keep_awake.leases.live"])
	if len(live) != 17 {
		t.Fatalf("want 16 owners + other = 17 series, got %d: %v", len(live), live)
	}
	if live["owner=other"] != 1 {
		t.Errorf("17th owner not folded into other: %v", live)
	}
	if _, ok := live["owner=owner-16"]; ok {
		t.Error("17th distinct owner kept its own label")
	}

	// A later, brand new owner is also other; known owners keep their label;
	// others are summed.
	e.RecordLeasesLive(map[string]int{"owner-00": 3, "zzz-new": 2, "yyy-new": 4})
	live = labelSets(t, collect(t, r)["pg_keep_awake.leases.live"])
	if live["owner=owner-00"] != 3 || live["owner=other"] != 6 {
		t.Errorf("after second call: %v", live)
	}
	// Owners that vanished are reset to 0, not left stale.
	if live["owner=owner-05"] != 0 {
		t.Errorf("vanished owner not zeroed: %v", live["owner=owner-05"])
	}
}

func TestFlagsZeroedWhenDropped(t *testing.T) {
	e, r, _ := newTest(t)
	e.RecordAssertion(true, []string{"idle", "system"})
	e.RecordAssertion(true, []string{"idle"})
	f := labelSets(t, collect(t, r)["pg_keep_awake.assertion.flags"])
	if f["flag=idle"] != 1 || f["flag=system"] != 0 {
		t.Errorf("flags = %v", f)
	}
}

func TestLeaseIDAndPIDNeverMetricLabels(t *testing.T) {
	e, r, _ := newTest(t)
	e.RecordLeasesLive(map[string]int{"alice": 1})
	e.IncLeasesReaped("expired")
	e.IncLeasesRejected("unreadable")
	e.RecordAssertion(true, []string{"idle"})
	e.RecordGateState("ok")
	e.IncBatteryRelease("watchdog")
	e.IncAnchorExit("watchdog")
	// These carry ids and pids, but only into logs.
	e.LogLeaseCreated("lease-id-123", "alice")
	e.LogLeaseReaped("lease-id-123", "alice", "expired")
	e.LogAssertionSpawned(4242)
	e.LogAnchorExit("watchdog", 10, 4243, time.Now())

	allowed := map[string]bool{"owner": true, "reason": true, "flag": true, "state": true}
	for name, m := range collect(t, r) {
		var sets []attribute.Set
		switch d := m.Data.(type) {
		case metricdata.Gauge[int64]:
			for _, p := range d.DataPoints {
				sets = append(sets, p.Attributes)
			}
		case metricdata.Sum[int64]:
			for _, p := range d.DataPoints {
				sets = append(sets, p.Attributes)
			}
		}
		for _, s := range sets {
			for _, kv := range s.ToSlice() {
				if !allowed[string(kv.Key)] {
					t.Errorf("%s: unexpected label %q", name, kv.Key)
				}
				if v := kv.Value.String(); strings.Contains(v, "lease-id-123") || v == "4242" || v == "4243" {
					t.Errorf("%s: label %s carries a lease id or pid: %s", name, kv.Key, v)
				}
			}
		}
	}
}

func TestLogEvents(t *testing.T) {
	e, _, logs := newTest(t)
	now := time.Date(2026, 10, 15, 1, 2, 3, 0, time.UTC)
	e.LogLeaseCreated("l1", "alice")
	e.LogLeaseReaped("l1", "alice", "expired")
	e.LogAssertionSpawned(11)
	e.LogAssertionAdopted(12)
	e.LogAssertionReleased("linger")
	e.LogGateTransition("ok", "released")
	e.LogConfigError(errors.New("bad toml"))
	e.LogAnchorExit("blind_threshold", 9, 13, now)

	logs.mu.Lock()
	defer logs.mu.Unlock()
	var names []string
	byName := map[string]map[string]string{}
	for _, rec := range logs.recs {
		attrs := map[string]string{}
		rec.WalkAttributes(func(kv attribute.KeyValue) bool {
			attrs[string(kv.Key)] = kv.Value.String()
			return true
		})
		n := attrs["event_name"]
		names = append(names, n)
		byName[n] = attrs
		if rec.Body().AsString() != n {
			t.Errorf("body %q != event_name %q", rec.Body().AsString(), n)
		}
	}
	wantNames := []string{
		"lease.created", "lease.reaped", "assertion.spawned", "assertion.adopted",
		"assertion.released", "gate.transition", "config.error", "anchor.exit",
	}
	if strings.Join(names, ",") != strings.Join(wantNames, ",") {
		t.Fatalf("events = %v, want %v", names, wantNames)
	}
	if a := byName["lease.reaped"]; a["lease_id"] != "l1" || a["reason"] != "expired" || a["owner"] != "alice" {
		t.Errorf("lease.reaped attrs: %v", a)
	}
	if a := byName["gate.transition"]; a["from"] != "ok" || a["to"] != "released" {
		t.Errorf("gate.transition attrs: %v", a)
	}
	if a := byName["config.error"]; a["error"] != "bad toml" {
		t.Errorf("config.error attrs: %v", a)
	}
	a := byName["anchor.exit"]
	if a["reason"] != "blind_threshold" || a["percent"] != "9" || a["pid"] != "13" || a["at"] != "2026-10-15T01:02:03Z" {
		t.Errorf("anchor.exit attrs: %v", a)
	}
	if a := byName["assertion.spawned"]; a["pid"] != "11" {
		t.Errorf("assertion.spawned attrs: %v", a)
	}
}

func TestNoEndpointIsNoop(t *testing.T) {
	e, err := New(context.Background(), Options{ServiceName: "pg-keep-awake"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e.(Noop); !ok {
		t.Fatalf("no endpoint returned %T, want Noop", e)
	}
	// Every method is safe and does nothing.
	e.RecordKeeperUp(true)
	e.RecordTick(time.Second, time.Now())
	e.RecordLeasesLive(map[string]int{"a": 1})
	e.IncLeasesReaped("expired")
	e.IncLeasesRejected("over_cap")
	e.RecordAssertion(true, []string{"idle"})
	e.RecordGateState("ok")
	e.RecordBattery(1, true)
	e.RecordReleasePct(1)
	e.IncBatteryRelease("watchdog")
	e.IncAnchorExit("watchdog")
	e.LogLeaseCreated("a", "b")
	e.LogLeaseReaped("a", "b", "c")
	e.LogAssertionSpawned(1)
	e.LogAssertionAdopted(1)
	e.LogAssertionReleased("x")
	e.LogGateTransition("a", "b")
	e.LogConfigError(nil)
	e.LogAnchorExit("watchdog", 1, 1, time.Now())
	if err := e.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestUnreachableCollectorDoesNotBlock points the emitter at a loopback port
// nothing listens on and checks that recording calls stay fast and that Close
// is bounded by its context.
func TestUnreachableCollectorDoesNotBlock(t *testing.T) {
	for _, ep := range []string{"127.0.0.1:1", "http://127.0.0.1:1"} {
		t.Run(ep, func(t *testing.T) {
			e, err := New(context.Background(), Options{Endpoint: ep, ServiceName: "pg-keep-awake", ServiceVersion: "test"})
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := e.(Noop); ok {
				t.Fatal("endpoint set but got Noop")
			}
			start := time.Now()
			for i := 0; i < 500; i++ {
				e.RecordKeeperUp(true)
				e.RecordTick(time.Millisecond, time.Now())
				e.RecordLeasesLive(map[string]int{"alice": 1})
				e.IncLeasesReaped("expired")
				e.LogLeaseCreated("id", "alice")
				e.LogGateTransition("ok", "released")
			}
			if d := time.Since(start); d > 2*time.Second {
				t.Errorf("recording calls took %v against an unreachable collector", d)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			cs := time.Now()
			_ = e.Close(ctx) // may report the export failure; it must return
			if d := time.Since(cs); d > 5*time.Second {
				t.Errorf("Close took %v, want it bounded by its context", d)
			}
		})
	}
}

func TestExportHealthThrottle(t *testing.T) {
	now := time.Unix(0, 0)
	h := newExportHealth(func() time.Time { return now }, time.Minute)
	if _, ok := h.onFailure(errors.New("boom")); !ok {
		t.Error("first failure of a streak must be surfaced")
	}
	if _, ok := h.onFailure(errors.New("boom")); ok {
		t.Error("second failure inside the throttle window must be suppressed")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := h.onFailure(errors.New("boom")); !ok {
		t.Error("failure after the throttle window must be surfaced")
	}
	line, ok := h.onSuccess()
	if !ok || !strings.Contains(line, "RECOVERED") || !strings.Contains(line, "pg-keep-awake") {
		t.Errorf("recovery line = %q, %v", line, ok)
	}
	if _, ok := h.onSuccess(); ok {
		t.Error("steady success must be silent")
	}
}
