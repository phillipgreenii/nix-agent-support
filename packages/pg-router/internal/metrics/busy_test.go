package metrics

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// The busy gauge (bead pg2-8ij5o, operator ruling 30 of docket pg2-12gr4).

type busyHarness struct {
	emitter  *Emitter
	reader   *sdkmetric.ManualReader
	inFlight map[string]int
	fetching []string
}

func newBusyHarness(t *testing.T, sources map[string]time.Duration, roles ...string) *busyHarness {
	t.Helper()
	h := &busyHarness{reader: sdkmetric.NewManualReader(), inFlight: map[string]int{}}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(h.reader))
	e, err := New(mp, func() map[string]int { return nil },
		WithListenerInFlight(func() map[string]int {
			out := map[string]int{}
			for k, v := range h.inFlight {
				out[k] = v
			}
			return out
		}),
		WithRoleProgress(roles...),
		WithPullSources(sources),
		WithBusySources(func() []string { return append([]string(nil), h.fetching...) }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.emitter = e
	return h
}

// read returns MetricBusy as "kind/name" -> value.
func (h *busyHarness) read(t *testing.T) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := h.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != MetricBusy {
				continue
			}
			for _, dp := range m.Data.(metricdata.Gauge[int64]).DataPoints {
				kind, _ := dp.Attributes.Value(attribute.Key("kind"))
				name, _ := dp.Attributes.Value(attribute.Key("name"))
				out[kind.AsString()+"/"+name.AsString()] = dp.Value
			}
		}
	}
	return out
}

func (h *busyHarness) expect(t *testing.T, want map[string]int64) {
	t.Helper()
	got := h.read(t)
	if len(got) != len(want) {
		t.Errorf("busy series = %v, want %v", got, want)
		return
	}
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Errorf("busy{%s} = %v (present=%v), want %d (all: %v)", k, g, ok, v, got)
		}
	}
}

func TestBusy_EverySeriesStartsAtZero(t *testing.T) {
	h := newBusyHarness(t, map[string]time.Duration{"feedback": time.Minute}, "worker", "reviewer")
	h.expect(t, map[string]int64{"role/worker": 0, "role/reviewer": 0, "source/feedback": 0})
}

func TestBusy_RoleIsBusyWhileAnOfferIsInFlight(t *testing.T) {
	h := newBusyHarness(t, nil, "worker", "reviewer")
	h.inFlight["worker"] = 1
	h.expect(t, map[string]int64{"role/worker": 1, "role/reviewer": 0})
	h.inFlight["worker"] = 0
	h.expect(t, map[string]int64{"role/worker": 0, "role/reviewer": 0})
}

func TestBusy_RoleStaysBusyAfterABusyDeclineUntilProgress(t *testing.T) {
	h := newBusyHarness(t, nil, "worker")
	h.emitter.OnRoleDeclined("worker", "busy")
	h.expect(t, map[string]int64{"role/worker": 1}) // nothing in flight, yet busy
	h.emitter.OnRoleSucceeded("worker")
	h.expect(t, map[string]int64{"role/worker": 0})
}

func TestBusy_DeclineKindsCountOnlyWhenBusy(t *testing.T) {
	for _, tc := range []struct {
		kind string
		busy int64
	}{
		{"busy", 1}, {"", 1}, {"something-new", 1}, {"fault", 0}, {"neutral", 0},
	} {
		t.Run("kind="+tc.kind, func(t *testing.T) {
			h := newBusyHarness(t, nil, "worker")
			h.emitter.OnRoleDeclined("worker", tc.kind)
			h.expect(t, map[string]int64{"role/worker": tc.busy})
		})
	}
}

// A handler error is unsuccessful, not busy.
func TestBusy_HandlerErrorDoesNotMakeARoleBusy(t *testing.T) {
	h := newBusyHarness(t, nil, "worker")
	h.emitter.OnRoleFailed("worker", errors.New("exit 1: boom"))
	h.expect(t, map[string]int64{"role/worker": 0})
}

func TestBusy_SourceIsBusyWhileAFetchIsInFlight(t *testing.T) {
	h := newBusyHarness(t, map[string]time.Duration{"feedback": time.Minute, "urgent": time.Minute})
	h.fetching = []string{"urgent"}
	h.expect(t, map[string]int64{"source/feedback": 0, "source/urgent": 1})
	h.fetching = nil
	h.expect(t, map[string]int64{"source/feedback": 0, "source/urgent": 0})
}

// RoleBusy (the read the status reply's rule shares) matches the gauge.
func TestBusy_RoleBusyReadMatchesGauge(t *testing.T) {
	h := newBusyHarness(t, nil, "a", "b", "c")
	h.inFlight["a"] = 1
	h.emitter.OnRoleDeclined("b", "busy")
	got := h.emitter.RoleBusy()
	want := map[string]bool{"a": true, "b": true, "c": false}
	for r, w := range want {
		if got[r] != w {
			t.Errorf("RoleBusy()[%s] = %v, want %v", r, got[r], w)
		}
	}
	h.expect(t, map[string]int64{"role/a": 1, "role/b": 1, "role/c": 0})
}

func TestRoleBusy_PureRule(t *testing.T) {
	for _, tc := range []struct {
		inFlight bool
		p        RoleProgress
		want     bool
	}{
		{false, RoleProgress{}, false},
		{true, RoleProgress{}, true},
		{false, RoleProgress{Busy: 1}, true},
		{false, RoleProgress{Unsuccessful: 5, AgeSeconds: 9999}, false}, // stalled is not busy
	} {
		if got := RoleBusy(tc.inFlight, tc.p); got != tc.want {
			t.Errorf("RoleBusy(%v, %+v) = %v, want %v", tc.inFlight, tc.p, got, tc.want)
		}
	}
}

func TestBusy_NotRegisteredWithoutAnOption(t *testing.T) {
	h := newHarness(t)
	rm := h.collect(t)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == MetricBusy {
				t.Errorf("%s registered with neither WithRoleProgress nor WithBusySources", m.Name)
			}
		}
	}
	if got := h.emitter.RoleBusy(); got != nil {
		t.Errorf("RoleBusy() = %v, want nil without WithRoleProgress", got)
	}
}
