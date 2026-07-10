package daemon

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// TestHeartbeatEveryN_RoundsIntervalToTicks proves the cadence divisor is the
// interval rounded to the nearest whole tick, clamped to a minimum of 1 so a
// zero/oversized tick still heartbeats every tick rather than never.
func TestHeartbeatEveryN_RoundsIntervalToTicks(t *testing.T) {
	cases := []struct {
		name     string
		tick     time.Duration
		interval time.Duration
		want     int
	}{
		{"60s over 5s tick", 5 * time.Second, 60 * time.Second, 12},
		{"rounds down", 7 * time.Second, 60 * time.Second, 9}, // 8.57 -> 9
		{"rounds up", 9 * time.Second, 60 * time.Second, 7},   // 6.67 -> 7
		{"interval below tick clamps to 1", 90 * time.Second, 60 * time.Second, 1},
		{"interval equals tick", 60 * time.Second, 60 * time.Second, 1},
		{"zero tick clamps to 1", 0, 60 * time.Second, 1},
		{"zero interval clamps to 1", 5 * time.Second, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := heartbeatEveryN(tc.tick, tc.interval); got != tc.want {
				t.Errorf("heartbeatEveryN(%v, %v) = %d, want %d", tc.tick, tc.interval, got, tc.want)
			}
		})
	}
}

// TestShouldHeartbeat_FiresOnMultiplesOnly proves the cadence gate fires on
// tick 0, N, 2N, … and stays quiet in between — the modest-cadence contract
// (NOT every tick).
func TestShouldHeartbeat_FiresOnMultiplesOnly(t *testing.T) {
	const everyN = 12
	fires := map[int]bool{0: true, 12: true, 24: true, 36: true}
	for tickCount := 0; tickCount <= 36; tickCount++ {
		got := shouldHeartbeat(tickCount, everyN)
		want := fires[tickCount]
		if got != want {
			t.Errorf("shouldHeartbeat(%d, %d) = %v, want %v", tickCount, everyN, got, want)
		}
	}
}

// TestShouldHeartbeat_EveryNBelowOneHeartbeatsEveryTick proves a degenerate
// everyN (< 1) is treated as 1 so the daemon still produces a baseline stream.
func TestShouldHeartbeat_EveryNBelowOneHeartbeatsEveryTick(t *testing.T) {
	for _, everyN := range []int{0, -3} {
		for tickCount := 0; tickCount < 5; tickCount++ {
			if !shouldHeartbeat(tickCount, everyN) {
				t.Errorf("shouldHeartbeat(%d, %d) = false, want true (everyN<1 => every tick)", tickCount, everyN)
			}
		}
	}
}

// TestHeartbeatAttrs_SummarisesTree proves the payload is a small, all-string
// summary: session counts by status summed across the tree, plan_tier,
// auto_resume, and five_hour_pct when known.
func TestHeartbeatAttrs_SummarisesTree(t *testing.T) {
	pct := 42.5
	tree := &aggregate.Tree{
		Dirs: []*aggregate.Directory{
			{WorkingN: 2, BlockedN: 1, IdleN: 0},
			{WorkingN: 1, BlockedN: 0, IdleN: 3},
		},
		FiveHourPct: &pct,
	}
	attrs := heartbeatAttrs(tree, "max_5x", true)
	want := map[string]string{
		"sessions_working": "3",
		"sessions_blocked": "1",
		"sessions_idle":    "3",
		"plan_tier":        "max_5x",
		"auto_resume":      "true",
		"five_hour_pct":    "42.5",
	}
	for k, v := range want {
		if attrs[k] != v {
			t.Errorf("attrs[%q] = %q, want %q (full=%v)", k, attrs[k], v, attrs)
		}
	}
	if len(attrs) != len(want) {
		t.Errorf("attrs has %d keys, want %d: %v", len(attrs), len(want), attrs)
	}
}

// TestHeartbeatAttrs_OmitsUnknownFiveHour proves an unknown (nil) FiveHourPct
// leaves five_hour_pct absent rather than reporting a false 0%.
func TestHeartbeatAttrs_OmitsUnknownFiveHour(t *testing.T) {
	attrs := heartbeatAttrs(&aggregate.Tree{}, "pro", false)
	if _, ok := attrs["five_hour_pct"]; ok {
		t.Errorf("five_hour_pct should be omitted when unknown, got %q", attrs["five_hour_pct"])
	}
	if attrs["auto_resume"] != "false" {
		t.Errorf("auto_resume = %q, want false", attrs["auto_resume"])
	}
	if attrs["sessions_working"] != "0" || attrs["sessions_blocked"] != "0" || attrs["sessions_idle"] != "0" {
		t.Errorf("zero-tree counts wrong: %v", attrs)
	}
}

// TestHeartbeatAttrs_NilTreeSafe proves a nil tree yields a zero-count summary
// without panicking (defensive: the emit site guards on a successful snapshot
// but the helper must stand alone).
func TestHeartbeatAttrs_NilTreeSafe(t *testing.T) {
	attrs := heartbeatAttrs(nil, "max_5x", false)
	if attrs["sessions_working"] != "0" {
		t.Errorf("nil-tree sessions_working = %q, want 0", attrs["sessions_working"])
	}
}

// TestHeartbeatAttrs_AllValuesAreStrings is a guard on the "keep the payload
// small and all-string" constraint — every value must be a plain string so the
// log record carries only string attributes (matching LogEvent's contract).
func TestHeartbeatAttrs_AllValuesAreStrings(t *testing.T) {
	// The signature already returns map[string]string; this test documents the
	// intent and fails to compile if the shape ever regresses to map[string]any.
	var attrs map[string]string = heartbeatAttrs(&aggregate.Tree{
		Dirs: []*aggregate.Directory{{Sessions: []*aggregate.SessionView{
			{Session: &session.Session{Status: session.Working}},
		}}},
	}, "max_5x", true)
	_ = attrs
}
