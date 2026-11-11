package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/levels"
	"github.com/phillipgreenii/pg-router/internal/metrics"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// levelLogBuf is a bytes.Buffer safe for the logger's concurrent writers.
type levelLogBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *levelLogBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *levelLogBuf) lines() []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(l.b.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(ln), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

// flatPoints maps "name{k=v,...}" to the value of every data point of rm.
func flatPoints(rm metricdata.ResourceMetrics) map[string]float64 {
	out := map[string]float64{}
	add := func(name string, kv string, v float64) { out[name+"{"+kv+"}"] = v }
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Gauge[int64]:
				for _, p := range d.DataPoints {
					add(m.Name, attrString(p.Attributes.ToSlice()), float64(p.Value))
				}
			case metricdata.Gauge[float64]:
				for _, p := range d.DataPoints {
					add(m.Name, attrString(p.Attributes.ToSlice()), p.Value)
				}
			case metricdata.Sum[int64]:
				for _, p := range d.DataPoints {
					add(m.Name, attrString(p.Attributes.ToSlice()), float64(p.Value))
				}
			case metricdata.Sum[float64]:
				for _, p := range d.DataPoints {
					add(m.Name, attrString(p.Attributes.ToSlice()), p.Value)
				}
			}
		}
	}
	return out
}

// The queue as run.go builds it reports a level-blocked role through the
// metrics Emitter: the level gauges read the projection, role_paused and
// requirement_blocked come from the queue's LevelObserver, and the transition
// emits one role_paused log line carrying resource_level.
func TestBootCore_levelObserverAndLevelGaugesAreWired(t *testing.T) {
	buf := &levelLogBuf{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	logDir := shortDir(t)
	seedBlockedWork(t, logDir)
	reader := sdkmetric.NewManualReader()
	res := config.ResourceSet{{Name: "disk", Scale: []string{"OK", "LOW"}, Sensor: "host-sensor", GraceFactor: 2}}
	cfg := config.Config{
		LogDir:        logDir,
		Resources:     res,
		MeterProvider: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)),
		Roles:         roles.RoleSet{{Name: "r1", Enabled: true, Binds: []string{"t1"}}},
		Requirements: map[string]config.Compiled{"r1": {Requirement: levels.Requirement{Clauses: []levels.Clause{
			{Index: 0, Text: "disk >= OK", Resource: "disk", Allowed: []string{"OK"}},
		}}}},
	}
	o := &orchestrator.Orchestrator{Cfg: cfg, Reg: cfg.Roles, Handler: &fakeHandlerClient{}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, q, _, storeClose, err := bootCore(ctx, cfg, o, cfg.Roles, runExclusions{}, core.RunModeDrainAndExit)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = storeClose() }()
	q.Dispatch()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	got := flatPoints(rm)
	want := map[string]float64{
		metrics.MetricResourceLevel + "{resource=disk}":                         1,
		metrics.MetricResourceScaleSize + "{resource=disk}":                     2,
		metrics.MetricRolePaused + "{role=r1}":                                  1,
		metrics.MetricRequirementBlocked + "{kind=level,resource=disk,role=r1}": 1,
		metrics.MetricResourceLevelSource + "{resource=disk,source=sensor}":     1,
	}
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Errorf("%s = %v (present %v), want %v", k, g, ok, v)
		}
	}
	paused := 0
	for _, l := range buf.lines() {
		if l["msg"] == "role_paused" {
			paused++
			if l["resource_level"] != "LOW" || l["resource"] != "disk" || l["role"] != "r1" {
				t.Errorf("role_paused line = %v, want role r1, resource disk, resource_level LOW", l)
			}
		}
	}
	if paused != 1 {
		t.Errorf("role_paused lines = %d, want exactly 1 (per transition)", paused)
	}
}

// attrString renders attributes as "k=v,k=v", sorted by key.
func attrString(kvs []attribute.KeyValue) string {
	parts := make([]string, 0, len(kvs))
	for _, kv := range kvs {
		parts = append(parts, string(kv.Key)+"="+kv.Value.Emit())
	}
	slices.Sort(parts)
	return strings.Join(parts, ",")
}
