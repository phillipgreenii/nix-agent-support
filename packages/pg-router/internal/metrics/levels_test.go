package metrics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	otelprometheus "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/levels"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// levelHarness is an Emitter over the real Prometheus exporter, so a test sees
// the exposed names (unit and _total suffixes included).
type levelHarness struct {
	e    *Emitter
	reg  *prometheus.Registry
	logs *bytes.Buffer
	now  time.Time
}

func newLevelHarness(t *testing.T, samples *[]LevelSample, opts ...Option) *levelHarness {
	t.Helper()
	reg := prometheus.NewRegistry()
	exp, err := otelprometheus.New(otelprometheus.WithRegisterer(reg))
	if err != nil {
		t.Fatal(err)
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exp))
	h := &levelHarness{reg: reg, logs: &bytes.Buffer{}, now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	all := append([]Option{
		WithClock(func() time.Time { return h.now }),
		WithLogger(slog.New(slog.NewJSONHandler(h.logs, nil))),
		WithLevels(func() []LevelSample { return *samples }),
		WithPausedRoles("seeded"),
	}, opts...)
	h.e, err = New(t.Context(), mp, func() map[string]int { return nil }, all...)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// series returns "name{k=v,...}" -> value for every exposed series.
func (h *levelHarness) series(t *testing.T) map[string]float64 {
	t.Helper()
	fams, err := h.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, f := range fams {
		for _, m := range f.Metric {
			var kv []string
			for _, l := range m.Label {
				if l.GetName() == "otel_scope_name" || l.GetName() == "otel_scope_version" || l.GetName() == "otel_scope_schema_url" {
					continue
				}
				kv = append(kv, l.GetName()+"="+l.GetValue())
			}
			slices.Sort(kv)
			var v float64
			switch f.GetType() {
			case dto.MetricType_COUNTER:
				v = m.Counter.GetValue()
			case dto.MetricType_GAUGE:
				v = m.Gauge.GetValue()
			default:
				continue
			}
			out[f.GetName()+"{"+strings.Join(kv, ",")+"}"] = v
		}
	}
	return out
}

func (h *levelHarness) logLines(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(h.logs.String()), "\n") {
		if ln == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("log line %q is not JSON: %v", ln, err)
		}
		out = append(out, m)
	}
	return out
}

func wantSeries(t *testing.T, got map[string]float64, want map[string]float64) {
	t.Helper()
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Errorf("%s = %v (present %v), want %v", k, g, ok, v)
		}
	}
}

// Each level series is exposed under its catalog name with the pinned label set,
// 0 = best, the unit suffix on the gauges that have one and _total only on the
// counters.
func TestLevelMetrics_gaugesExposeCatalogNamesAndLabels(t *testing.T) {
	samples := []LevelSample{
		{
			Resource: "disk", Level: "LOW", Index: 2, ScaleSize: 4, HasLevel: true, Source: "sensor", Owner: "host-sensor",
			ObservedAt: time.Date(2026, 11, 1, 11, 59, 30, 0, time.UTC), Age: 30 * time.Second, LeaseRemaining: 90 * time.Second, HasLease: true,
			Reading: &LevelReading{Value: 12.5, Unit: "GiB"},
		},
		{
			Resource: "router", Level: "PAUSED", Index: 1, ScaleSize: 2, HasLevel: true, Source: "operator", Owner: "operator",
			OverrideActive: true,
		}, // until cleared: no lease series
		{Resource: "cpu", Level: "PLENTY", Index: 0, ScaleSize: 4}, // no level: reads the unknown value
	}
	h := newLevelHarness(t, &samples)
	got := h.series(t)
	wantSeries(t, got, map[string]float64{
		"pg_router_resource_level{resource=disk}":                          2,
		"pg_router_resource_level{resource=router}":                        1,
		"pg_router_resource_level{resource=cpu}":                           0,
		"pg_router_resource_scale_size{resource=disk}":                     4,
		"pg_router_resource_scale_size{resource=router}":                   2,
		"pg_router_resource_level_stale{resource=disk}":                    0,
		"pg_router_resource_level_override_active{resource=router}":        1,
		"pg_router_resource_level_override_active{resource=disk}":          0,
		"pg_router_resource_level_source{resource=disk,source=sensor}":     1,
		"pg_router_resource_level_source{resource=router,source=operator}": 1,
		"pg_router_resource_level_age_seconds{resource=disk}":              30,
		"pg_router_resource_level_lease_remaining_seconds{resource=disk}":  90,
		"pg_router_resource_reading{resource=disk,unit=GiB}":               12.5,
	})
	for _, absent := range []string{
		"pg_router_resource_level_lease_remaining_seconds{resource=router}",
		"pg_router_resource_level_source{resource=cpu,source=sensor}",
		"pg_router_resource_level_age_seconds{resource=cpu}",
	} {
		if _, ok := got[absent]; ok {
			t.Errorf("%s is present, want absent (no expiry / no level)", absent)
		}
	}
	wantSeries(t, got, map[string]float64{"pg_router_sensor_last_observed_age_seconds{sensor=host-sensor}": 30})
	if _, ok := got["pg_router_sensor_last_observed_age_seconds{sensor=operator}"]; ok {
		t.Error("an operator override is not a sensor answer")
	}
	for k := range got {
		if strings.HasPrefix(k, "pg_router_gate") {
			t.Errorf("legacy gate series %s still exposed", k)
		}
	}
}

// The counters carry _total, the sensor pull gauge does not, and the label sets
// are the pinned ones.
func TestLevelMetrics_countersAndSensorSeries(t *testing.T) {
	var samples []LevelSample
	h := newLevelHarness(t, &samples)
	e := h.e
	e.OnLevelChanged("disk", "OK", "LOW")
	e.OnLevelEventRejected("disk", "NOPE", "unknown_level")
	e.OnLevelPull("host-sensor", 1500*time.Millisecond)
	e.OnLevelPull("host-sensor", 500*time.Millisecond)
	e.OnLevelPullFailure("host-sensor", "deadline")
	e.OnRequirementBlocked("r1", "dispatch", "disk", "stale")
	e.OnRequirementDrop("t1", "r1", "disk")
	e.OnRolePaused("r1", "disk", "LOW")
	wantSeries(t, h.series(t), map[string]float64{
		"pg_router_resource_changes_total{resource=disk,to=LOW}":                   1,
		"pg_router_level_events_rejected_total{reason=unknown_level}":              1,
		"pg_router_sensor_pull_total{sensor=host-sensor}":                          2,
		"pg_router_sensor_pull_seconds{sensor=host-sensor}":                        0.5,
		"pg_router_sensor_pull_failures_total{reason=deadline,sensor=host-sensor}": 1,
		"pg_router_requirement_blocked_total{kind=stale,resource=disk,role=r1}":    1,
		"pg_router_requirement_drops_total{resource=disk,role=r1,type=t1}":         1,
		"pg_router_role_paused{role=r1}":                                           1,
		"pg_router_role_paused{role=seeded}":                                       0,
	})
	e.OnRoleResumed("r1", "disk", "OK")
	wantSeries(t, h.series(t), map[string]float64{"pg_router_role_paused{role=r1}": 0})
}

// resource_level_seconds accumulates the time spent at the level in force.
func TestLevelMetrics_levelSecondsAccumulatePerLevel(t *testing.T) {
	samples := []LevelSample{{Resource: "disk", Level: "OK", ScaleSize: 2, HasLevel: true}}
	h := newLevelHarness(t, &samples)
	h.series(t) // first collect: no time has passed
	h.now = h.now.Add(10 * time.Second)
	wantSeries(t, h.series(t), map[string]float64{"pg_router_resource_level_seconds_total{level=OK,resource=disk}": 10})
	samples[0].Level = "LOW"
	h.now = h.now.Add(5 * time.Second)
	got := h.series(t)
	wantSeries(t, got, map[string]float64{
		"pg_router_resource_level_seconds_total{level=LOW,resource=disk}": 5,
		"pg_router_resource_level_seconds_total{level=OK,resource=disk}":  10,
	})
}

// The 64-value fold: the first 64 distinct values of a label are kept and a later
// one is recorded as OTHER, for resource, role and type alike.
func TestLevelMetrics_foldsLabelsAt64(t *testing.T) {
	var samples []LevelSample
	h := newLevelHarness(t, &samples)
	for i := 0; i < 70; i++ {
		h.e.OnRequirementBlocked(fmt.Sprintf("role%d", i), "dispatch", fmt.Sprintf("res%d", i), "level")
		h.e.OnRequirementDrop(fmt.Sprintf("type%d", i), "role0", "res0")
	}
	got := h.series(t)
	if _, ok := got["pg_router_requirement_blocked_total{kind=level,resource=res63,role=role63}"]; !ok {
		t.Error("the 64th distinct value must be kept")
	}
	if got["pg_router_requirement_blocked_total{kind=level,resource=OTHER,role=OTHER}"] != 6 {
		t.Errorf("OTHER blocked = %v, want 6 (values 65 to 70)", got["pg_router_requirement_blocked_total{kind=level,resource=OTHER,role=OTHER}"])
	}
	if got["pg_router_requirement_drops_total{resource=res0,role=role0,type=OTHER}"] != 6 {
		t.Errorf("OTHER drops = %v, want 6", got["pg_router_requirement_drops_total{resource=res0,role=role0,type=OTHER}"])
	}
}

// Every level log line is one JSON line carrying resource_level (never level).
func TestLevelLogLines_eachCarriesResourceLevel(t *testing.T) {
	var samples []LevelSample
	h := newLevelHarness(t, &samples)
	e := h.e
	e.OnLevelChanged("disk", "OK", "LOW")
	e.OnLevelGraceExpired("disk", "LOW")
	e.OnLevelOverrideSet("router", "PAUSED", "operator", time.Time{})
	e.OnLevelOverrideSet("router", "PAUSED", "operator", time.Date(2026, 11, 1, 13, 0, 0, 0, time.UTC))
	e.OnLevelOverrideExpired("router", "PAUSED", "operator")
	e.OnLevelEventRejected("disk", "NOPE", "unknown_level")
	e.OnRolePaused("r1", "disk", "LOW")
	e.OnRoleResumed("r1", "disk", "OK")
	e.OnLegacyGateDropped("EXAMPLE_GATE", "operator", time.Time{})
	wantMsgs := []string{"level_changed", "level_grace_expired", "level_override_set", "level_override_set", "level_override_expired", "level_event_rejected", "role_paused", "role_resumed", "legacy_gate_dropped"}
	lines := h.logLines(t)
	if len(lines) != len(wantMsgs) {
		t.Fatalf("%d log lines, want %d: %s", len(lines), len(wantMsgs), h.logs.String())
	}
	for i, l := range lines {
		if l["msg"] != wantMsgs[i] {
			t.Errorf("line %d msg = %v, want %s", i, l["msg"], wantMsgs[i])
		}
		if _, ok := l["resource_level"]; !ok {
			t.Errorf("line %d (%v) has no resource_level key", i, l["msg"])
		}
		if _, ok := l["level"].(string); ok && l["msg"] != nil {
			// slog's own level key is "level"; the line's level is resource_level.
			if l["level"] == "LOW" || l["level"] == "OK" || l["level"] == "PAUSED" {
				t.Errorf("line %d carries the resource level under level: %v", i, l)
			}
		}
	}
	if lines[0]["resource_level"] != "LOW" || lines[0]["from"] != "OK" {
		t.Errorf("level_changed = %v, want resource_level LOW from OK", lines[0])
	}
	if lines[2]["until_cleared"] != true || lines[3]["expires_at"] != "2026-11-01T13:00:00Z" {
		t.Errorf("override lines = %v / %v", lines[2], lines[3])
	}
	last := lines[len(lines)-1]
	if last["resource_level"] != "EXAMPLE_GATE" || last["owner"] != "operator" || last["expires_at"] != "none" {
		t.Errorf("legacy_gate_dropped = %v", last)
	}
}

// requirement_blocked_seconds accrues wall-clock time per unmet resource, with
// sole and steps_below labels, never double counting and capped on a long gap.
func TestLevelMetrics_requirementBlockedSeconds(t *testing.T) {
	var samples []LevelSample
	h := newLevelHarness(t, &samples)
	bp := func(p string, sf ...levels.Shortfall) eventqueue.BlockedParticipant {
		return eventqueue.BlockedParticipant{Participant: p, Shortfalls: sf}
	}
	const name = "pg_router_requirement_blocked_seconds_total"
	h.now = h.now.Add(5 * time.Second)
	h.e.AccrueRequirementBlocked([]eventqueue.BlockedParticipant{
		bp("a", levels.Shortfall{Resource: "disk", Steps: 1, Sole: true}),
		bp("b", levels.Shortfall{Resource: "disk", Steps: 2, Sole: false}, levels.Shortfall{Resource: "net", Steps: levels.StepsUnknown, Sole: false}),
		bp("c", levels.Shortfall{Resource: "router", Steps: 1, Sole: true}),
	})
	wantSeries(t, h.series(t), map[string]float64{
		name + "{resource=disk,role=a,sole=true,steps_below=1}":       5,
		name + "{resource=disk,role=b,sole=false,steps_below=2}":      5,
		name + "{resource=net,role=b,sole=false,steps_below=unknown}": 5,
		name + "{resource=router,role=c,sole=true,steps_below=1}":     5,
	})
	// Next evaluation: only a is still blocked; it books the time since the LAST
	// evaluation, so b and c keep 5 and a goes to 5 + 3.
	h.now = h.now.Add(3 * time.Second)
	h.e.AccrueRequirementBlocked([]eventqueue.BlockedParticipant{bp("a", levels.Shortfall{Resource: "disk", Steps: 1, Sole: true})})
	// An evaluation with nobody blocked still advances the clock: the blocked
	// minute that follows is not stretched back over the idle gap.
	h.now = h.now.Add(4 * time.Second)
	h.e.AccrueRequirementBlocked(nil)
	h.now = h.now.Add(2 * time.Second)
	h.e.AccrueRequirementBlocked([]eventqueue.BlockedParticipant{bp("a", levels.Shortfall{Resource: "disk", Steps: 1, Sole: true})})
	got := h.series(t)
	wantSeries(t, got, map[string]float64{
		name + "{resource=disk,role=a,sole=true,steps_below=1}":   10,
		name + "{resource=disk,role=b,sole=false,steps_below=2}":  5,
		name + "{resource=router,role=c,sole=true,steps_below=1}": 5,
	})
	// A gap longer than the cap (a suspended host) books only the cap.
	h.now = h.now.Add(time.Hour)
	h.e.AccrueRequirementBlocked([]eventqueue.BlockedParticipant{bp("a", levels.Shortfall{Resource: "disk", Steps: 1, Sole: true})})
	wantSeries(t, h.series(t), map[string]float64{
		name + "{resource=disk,role=a,sole=true,steps_below=1}": 10 + maxAccrualGap.Seconds(),
	})
	// A call with no time elapsed adds nothing (no double count).
	h.e.AccrueRequirementBlocked([]eventqueue.BlockedParticipant{bp("a", levels.Shortfall{Resource: "disk", Steps: 1, Sole: true})})
	wantSeries(t, h.series(t), map[string]float64{
		name + "{resource=disk,role=a,sole=true,steps_below=1}": 10 + maxAccrualGap.Seconds(),
	})
}

// DispatchLevels keeps only graded-scale resources with a level in force, and
// OnDispatchAtLevel books count and seconds per resource for success and failure.
func TestLevelMetrics_dispatchAtLevel(t *testing.T) {
	samples := []LevelSample{
		{Resource: "disk", Level: "TIGHT", ScaleSize: 4, HasLevel: true},
		{Resource: "router", Level: "RUNNING", ScaleSize: 2, HasLevel: true}, // two-level: skipped
		{Resource: "cpu", Level: "PLENTY", ScaleSize: 4},                     // no level: skipped
		{Resource: "net", Level: "OK", ScaleSize: 3, HasLevel: true, Stale: true},
	}
	h := newLevelHarness(t, &samples)
	at := h.e.DispatchLevels()
	if want := [][2]string{{"disk", "TIGHT"}, {"net", "OK"}}; !slices.Equal(at, want) {
		t.Fatalf("DispatchLevels = %v, want %v", at, want)
	}
	h.e.OnDispatchAtLevel("r1", at, 90*time.Second, false)
	h.e.OnDispatchAtLevel("r1", at[:1], 30*time.Second, true)
	h.e.OnDispatchAtLevel("r1", [][2]string{{"disk", "OK"}}, 10*time.Second, false)
	got := h.series(t)
	wantSeries(t, got, map[string]float64{
		"pg_router_dispatch_at_level_total{level=TIGHT,outcome=success,resource=disk,role=r1}": 1,
		"pg_router_dispatch_at_level_total{level=TIGHT,outcome=failure,resource=disk,role=r1}": 1,
		"pg_router_dispatch_at_level_total{level=OK,outcome=success,resource=net,role=r1}":     1,
		"pg_router_dispatch_at_level_total{level=OK,outcome=success,resource=disk,role=r1}":    1,
		"pg_router_dispatch_at_level_seconds_total{level=TIGHT,resource=disk,role=r1}":         120,
		"pg_router_dispatch_at_level_seconds_total{level=OK,resource=net,role=r1}":             90,
		"pg_router_dispatch_at_level_seconds_total{level=OK,resource=disk,role=r1}":            10,
	})
	for k := range got {
		if strings.HasPrefix(k, "pg_router_dispatch_at_level") && (strings.Contains(k, "resource=router") || strings.Contains(k, "resource=cpu")) {
			t.Errorf("%s: two-level or unknown-level resource must not be booked", k)
		}
	}
}
