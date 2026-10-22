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

// The per-role progress gauges (docket pg2-12gr4.8, design sections 5 and 10).

type roleProgressHarness struct {
	emitter  *Emitter
	reader   *sdkmetric.ManualReader
	clk      *mockClock
	inFlight map[string]int
}

func newRoleProgressHarness(t *testing.T, roles ...string) *roleProgressHarness {
	t.Helper()
	h := &roleProgressHarness{
		reader:   sdkmetric.NewManualReader(),
		clk:      &mockClock{t: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)},
		inFlight: map[string]int{},
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(h.reader))
	e, err := New(mp, func() map[string]int { return nil }, WithClock(h.clk.now),
		WithListenerInFlight(func() map[string]int {
			out := map[string]int{}
			for k, v := range h.inFlight {
				out[k] = v
			}
			return out
		}),
		WithRoleProgress(roles...))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.emitter = e
	return h
}

// read returns the three gauges as role -> value.
func (h *roleProgressHarness) read(t *testing.T) (age map[string]float64, unsuccessful, busy map[string]int64) {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := h.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	age, unsuccessful, busy = map[string]float64{}, map[string]int64{}, map[string]int64{}
	role := func(attrs attribute.Set) string {
		v, _ := attrs.Value(attribute.Key("role"))
		return v.AsString()
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case MetricRoleLastProgressAge:
				g := m.Data.(metricdata.Gauge[float64])
				if m.Unit != "s" {
					t.Errorf("%s unit = %q, want s (so the exposition suffix is _seconds)", m.Name, m.Unit)
				}
				for _, dp := range g.DataPoints {
					age[role(dp.Attributes)] = dp.Value
				}
			case MetricRoleUnsuccessfulSinceProgress:
				for _, dp := range m.Data.(metricdata.Gauge[int64]).DataPoints {
					unsuccessful[role(dp.Attributes)] = dp.Value
				}
			case MetricRoleBusySinceProgress:
				for _, dp := range m.Data.(metricdata.Gauge[int64]).DataPoints {
					busy[role(dp.Attributes)] = dp.Value
				}
			}
		}
	}
	return age, unsuccessful, busy
}

// expect fails unless role reads exactly (age seconds, unsuccessful, busy).
func (h *roleProgressHarness) expect(t *testing.T, role string, age time.Duration, unsuccessful, busy int64) {
	t.Helper()
	a, u, b := h.read(t)
	if got, ok := a[role]; !ok || got != age.Seconds() {
		t.Errorf("age{%s} = %v (present=%v), want %v", role, got, ok, age.Seconds())
	}
	if got, ok := u[role]; !ok || got != unsuccessful {
		t.Errorf("unsuccessful{%s} = %v (present=%v), want %d", role, got, ok, unsuccessful)
	}
	if got, ok := b[role]; !ok || got != busy {
		t.Errorf("busy{%s} = %v (present=%v), want %d", role, got, ok, busy)
	}
}

func TestRoleProgress_StartsAtDaemonStartWithZeroCounts(t *testing.T) {
	h := newRoleProgressHarness(t, "worker", "reviewer")
	h.expect(t, "worker", 0, 0, 0)
	h.expect(t, "reviewer", 0, 0, 0)
	h.clk.advance(10 * time.Minute)
	// A role with no offers: its age grows from daemon start, and nothing counted.
	h.expect(t, "worker", 10*time.Minute, 0, 0)
}

func TestRoleProgress_AgeReadsZeroWhileOfferInFlight(t *testing.T) {
	h := newRoleProgressHarness(t, "worker")
	h.clk.advance(45 * time.Minute)
	h.inFlight["worker"] = 1
	h.expect(t, "worker", 0, 0, 0)
	h.inFlight["worker"] = 0
	h.expect(t, "worker", 45*time.Minute, 0, 0)
}

func TestRoleProgress_CleanFinishResetsAgeAndBothCounters(t *testing.T) {
	h := newRoleProgressHarness(t, "worker")
	h.emitter.OnRoleFailed("worker", errors.New("exit 1: boom"))
	h.emitter.OnRoleDeclined("worker", "busy")
	h.clk.advance(time.Hour)
	h.expect(t, "worker", time.Hour, 1, 1)
	h.emitter.OnRoleSucceeded("worker")
	// The series stays, at zero, with the age restarted now.
	h.expect(t, "worker", 0, 0, 0)
	h.clk.advance(time.Minute)
	h.expect(t, "worker", time.Minute, 0, 0)
}

func TestRoleProgress_RoutineSentinelResets(t *testing.T) {
	for _, tc := range []struct {
		name string
		role string
		err  error
	}{
		{"budget stop", "worker", errors.New("exit 1: session budget exceeded")},
		{"usage limit", "worker", errors.New("exit 1: " + UsageLimitSentinel)},
		{"triager failure", "bead-triage", errors.New("exit 1: anything at all")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newRoleProgressHarness(t, tc.role)
			h.emitter.OnRoleFailed(tc.role, errors.New("exit 1: boom"))
			if tc.role == "bead-triage" {
				// A triager's every error is routine, so the first call reset too.
				h.expect(t, tc.role, 0, 0, 0)
				h.emitter.OnRoleDeclined(tc.role, "busy")
			} else {
				h.emitter.OnRoleDeclined(tc.role, "busy")
			}
			h.clk.advance(time.Hour)
			h.emitter.OnRoleFailed(tc.role, tc.err)
			h.expect(t, tc.role, 0, 0, 0)
		})
	}
}

func TestRoleProgress_OtherHandlerErrorIsUnsuccessfulAndDoesNotResetAge(t *testing.T) {
	h := newRoleProgressHarness(t, "worker")
	h.clk.advance(20 * time.Minute)
	h.emitter.OnRoleFailed("worker", errors.New("wireclient: role worker exited 1: boom"))
	h.emitter.OnRoleFailed("worker", errors.New("exit 1 again"))
	h.expect(t, "worker", 20*time.Minute, 2, 0)
}

func TestRoleProgress_DeclineKinds(t *testing.T) {
	for _, tc := range []struct {
		kind         string
		unsuccessful int64
		busy         int64
	}{
		{"busy", 0, 1},
		{"", 0, 1},              // absent means busy
		{"something-new", 0, 1}, // an unknown kind counts as busy
		{"fault", 1, 0},
		{"neutral", 0, 0},
	} {
		t.Run("kind="+tc.kind, func(t *testing.T) {
			h := newRoleProgressHarness(t, "worker")
			h.emitter.OnRoleDeclined("worker", tc.kind)
			h.expect(t, "worker", 0, tc.unsuccessful, tc.busy)
		})
	}
}

func TestRoleProgress_ResetsAtRestart(t *testing.T) {
	h := newRoleProgressHarness(t, "worker")
	h.emitter.OnRoleFailed("worker", errors.New("boom"))
	h.emitter.OnRoleDeclined("worker", "busy")
	h.clk.advance(time.Hour)
	h.expect(t, "worker", time.Hour, 1, 1)
	// A restart is a new emitter: all three start over.
	h2 := newRoleProgressHarness(t, "worker")
	h2.expect(t, "worker", 0, 0, 0)
}

func TestRoleProgress_UnseededRoleIsSeededByItsFirstOutcome(t *testing.T) {
	h := newRoleProgressHarness(t)
	h.clk.advance(time.Hour)
	h.emitter.OnRoleDeclined("late", "fault")
	h.expect(t, "late", 0, 1, 0)
}

func TestRoleProgress_NotRegisteredWithoutOption(t *testing.T) {
	h := newHarness(t)
	// The hooks must be safe, and record nothing, when no gauge exists.
	h.emitter.OnRoleSucceeded("x")
	h.emitter.OnRoleFailed("x", errors.New("boom"))
	h.emitter.OnRoleDeclined("x", "fault")
	rm := h.collect(t)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case MetricRoleLastProgressAge, MetricRoleUnsuccessfulSinceProgress, MetricRoleBusySinceProgress:
				t.Errorf("%s registered without WithRoleProgress", m.Name)
			}
		}
	}
}

// IsRoutineHandlerError is the one classification OnHandlerFailure's reasons and
// the progress gauges share.
func TestIsRoutineHandlerError(t *testing.T) {
	for _, tc := range []struct {
		role string
		err  error
		want bool
	}{
		{"worker", errors.New("session budget exceeded"), true},
		{"worker", errors.New("x: " + UsageLimitSentinel), true},
		{"escalation-triage", errors.New("anything"), true},
		{"worker", errors.New("exit 1"), false},
		{"worker", nil, false},
	} {
		if got := IsRoutineHandlerError(tc.role, tc.err); got != tc.want {
			t.Errorf("IsRoutineHandlerError(%q, %v) = %v, want %v", tc.role, tc.err, got, tc.want)
		}
	}
}
