package metrics

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
)

// Tests for the per-listener pending gauges (epic pg2-u1cxx0, DEC-OBS-12):
// MetricListenerPending (a count, unit {event}) and MetricListenerOldestPendingAge
// (seconds), both fed by ONE queue read per collect.

func TestListenerPendingGaugesExportEveryRegisteredListener(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	reads := 0
	if _, err := New(
		t.Context(),
		mp, func() map[string]int { return nil },
		WithListenerPending(func() map[string]eventqueue.ListenerPending {
			reads++
			return map[string]eventqueue.ListenerPending{
				"a": {Count: 3, OldestAge: 90 * time.Second},
				"b": {}, // idle: present as 0, not absent
			}
		}),
	); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Fatalf("queue read %d times in one collect, want 1 (one source for both gauges)", reads)
	}

	count := findMetric(t, rm, MetricListenerPending)
	if count.Unit != "{event}" {
		t.Errorf("%s unit = %q, want {event}", MetricListenerPending, count.Unit)
	}
	got := gaugeValues(t, count, "role")
	if want := map[string]float64{"a": 3, "b": 0}; !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %v, want %v", MetricListenerPending, got, want)
	}

	age := findMetric(t, rm, MetricListenerOldestPendingAge)
	if age.Unit != "s" {
		t.Errorf("%s unit = %q, want s", MetricListenerOldestPendingAge, age.Unit)
	}
	got = gaugeValues(t, age, "role")
	if want := map[string]float64{"a": 90, "b": 0}; !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %v, want %v", MetricListenerOldestPendingAge, got, want)
	}
}

// The role label set is exactly the registered listener ids: bounded by
// configuration (DEC-OBS-7, DEC-OBS-9 "Bounds"), no other label key.
func TestListenerPendingGaugesLabelSetIsExactlyTheRegisteredIDs(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if _, err := New(
		t.Context(),
		mp, func() map[string]int { return nil },
		WithListenerPending(func() map[string]eventqueue.ListenerPending {
			return map[string]eventqueue.ListenerPending{"r1": {Count: 1}, "r2": {}}
		}),
	); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{MetricListenerPending, MetricListenerOldestPendingAge} {
		m := findMetric(t, rm, name)
		var roles []string
		switch d := m.Data.(type) {
		case metricdata.Gauge[int64]:
			for _, dp := range d.DataPoints {
				if dp.Attributes.Len() != 1 {
					t.Errorf("%s has %d attributes, want only role", name, dp.Attributes.Len())
				}
				v, _ := dp.Attributes.Value("role")
				roles = append(roles, v.AsString())
			}
		case metricdata.Gauge[float64]:
			for _, dp := range d.DataPoints {
				if dp.Attributes.Len() != 1 {
					t.Errorf("%s has %d attributes, want only role", name, dp.Attributes.Len())
				}
				v, _ := dp.Attributes.Value("role")
				roles = append(roles, v.AsString())
			}
		}
		sort.Strings(roles)
		if want := []string{"r1", "r2"}; !reflect.DeepEqual(roles, want) {
			t.Errorf("%s roles = %v, want %v", name, roles, want)
		}
	}
}

// Without the option neither gauge is registered.
func TestListenerPendingGaugesAbsentWithoutOption(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if _, err := New(t.Context(), mp, func() map[string]int { return nil }); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	if hasMetric(rm, MetricListenerPending) || hasMetric(rm, MetricListenerOldestPendingAge) {
		t.Fatal("pending gauges registered without WithListenerPending")
	}
}

// Against a real queue: a caught-up listener reads 0 while the retained depth
// still shows the event, and a stuck one reads its count and growing age.
func TestListenerPendingGaugesAgainstRealQueue(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	clk := &mockClock{t: time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)}
	var q *eventqueue.Queue
	if _, err := New(
		t.Context(),
		mp, func() map[string]int { return q.DepthByType() },
		WithClock(clk.now),
		WithListenerPending(func() map[string]eventqueue.ListenerPending { return q.PendingByListener() }),
	); err != nil {
		t.Fatal(err)
	}
	var err error
	q, err = eventqueue.New(eventqueue.NewMemStore(), eventqueue.WithClock(clk.now))
	if err != nil {
		t.Fatal(err)
	}
	q.Register(runningListener{id: "done", typ: "T", clk: clk, run: time.Second})
	q.Register(declineListener{id: "stuck", typ: "T"})
	if _, err := q.Enqueue(eventqueue.Event{ID: "e1", Type: "T", ExpiresAt: clk.t.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	q.Dispatch()
	clk.advance(30 * time.Second)
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	if got := gaugeValues(t, findMetric(t, rm, MetricListenerPending), "role"); got["done"] != 0 || got["stuck"] != 1 {
		t.Errorf("pending = %v, want done=0 stuck=1", got)
	}
	if got := gaugeValues(t, findMetric(t, rm, MetricListenerOldestPendingAge), "role"); got["done"] != 0 || got["stuck"] < 30 {
		t.Errorf("oldest pending age = %v, want done=0 stuck>=30", got)
	}
}

// declineListener matches one type and always declines busy.
type declineListener struct{ id, typ string }

func (l declineListener) ID() string                      { return l.id }
func (l declineListener) Matches(e eventqueue.Event) bool { return e.Type == l.typ }
func (l declineListener) Offer(eventqueue.Offering) eventqueue.OfferResult {
	return eventqueue.OfferResult{Decline: eventqueue.DeclineBusy}
}
