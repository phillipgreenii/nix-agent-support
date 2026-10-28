package telemetry

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
)

func i64(v int64) *int64 { return &v }
func bl(v bool) *bool    { return &v }

var originMetrics = []string{
	MetricOriginRemaining, MetricOriginLimit, MetricOriginNearLimit, MetricOriginReset,
	MetricOriginFloor, MetricOriginHeadroom, MetricOriginReadingAgeSecs,
}

func originFixture(t *testing.T, floor int) (*fixture, *origin.Readings) {
	t.Helper()
	rs := origin.NewReadings()
	return newFixture(t, func(o *Options) { o.Readings, o.Floor = rs, floor }), rs
}

func TestOriginGauges_NothingBeforeTheFirstReadingButTheFloor(t *testing.T) {
	f, _ := originFixture(t, 20)
	got := f.collect()
	for _, name := range originMetrics {
		_, present := got[name]
		if name == MetricOriginFloor {
			if !present || points(t, got[name])[""] != 20 {
				t.Errorf("%s = %v, want 20", name, got[name])
			}
			continue
		}
		if present && len(points(t, got[name])) > 0 {
			t.Errorf("%s reported before any reading: %v", name, points(t, got[name]))
		}
	}
}

func TestOriginGauges_FollowTheLatestReading(t *testing.T) {
	f, rs := originFixture(t, 20)
	rs.Record(internal.RateLimit{
		Status: 429, Limit: i64(100), Remaining: i64(12), NearLimit: bl(true),
		RetryAfterSeconds: 11, Reset: "2026-10-12T12:00:11Z", Reason: "jira-burst-based",
	}, t0.Add(-30*time.Second))
	got := f.collect()
	for name, want := range map[string]float64{
		MetricOriginRemaining:      12,
		MetricOriginLimit:          100,
		MetricOriginNearLimit:      1,
		MetricOriginFloor:          20,
		MetricOriginHeadroom:       -8, // 12 remaining minus the floor of 20: the floor is breached
		MetricOriginReset:          float64(time.Date(2026, 10, 12, 12, 0, 11, 0, time.UTC).Unix()),
		MetricOriginReadingAgeSecs: 30,
	} {
		if v := points(t, got[name])[""]; v != want {
			t.Errorf("%s = %v, want %v", name, v, want)
		}
	}
}

func TestOriginGauges_AKeyTheReadingLacksIsNotReportedAsZero(t *testing.T) {
	f, rs := originFixture(t, 20)
	rs.Record(internal.RateLimit{Status: 200, Remaining: i64(80)}, t0)
	got := f.collect()
	if v := points(t, got[MetricOriginRemaining])[""]; v != 80 {
		t.Errorf("remaining = %v", v)
	}
	for _, name := range []string{MetricOriginLimit, MetricOriginNearLimit, MetricOriginReset} {
		if m, ok := got[name]; ok && len(points(t, m)) > 0 {
			t.Errorf("%s must be unreported when the reading lacks it: %v", name, points(t, m))
		}
	}
}

// The gauges, and so the below-floor alert, drop out once the reading is older
// than origin.ReadingMaxAge, so a quiet daemon never latches a stale breach.
func TestOriginGauges_DropOutWhenTheReadingIsTooOld(t *testing.T) {
	f, rs := originFixture(t, 20)
	rs.Record(internal.RateLimit{Status: 200, Limit: i64(100), Remaining: i64(3)}, t0)
	f.clock.Advance(origin.ReadingMaxAge + time.Second)
	got := f.collect()
	for _, name := range originMetrics {
		if name == MetricOriginFloor {
			continue
		}
		if m, ok := got[name]; ok && len(points(t, m)) > 0 {
			t.Errorf("%s still reported for a stale reading: %v", name, points(t, m))
		}
	}
	if v := points(t, got[MetricOriginFloor])[""]; v != 20 {
		t.Errorf("floor = %v, want it reported regardless", v)
	}
}
