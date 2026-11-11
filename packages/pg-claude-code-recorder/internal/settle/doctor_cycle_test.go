package settle

import (
	"testing"
	"time"
)

// cycleInterval (24h) MUST exceed 2*minExpectedCycle (2h) so that the due
// threshold (interval minus expected) stays positive for both the floor case
// and a long-previous-cycle case; cycleLong (5h) MUST exceed minExpectedCycle
// so the "previous duration wins over the floor" case is distinct from the
// floor case. cycleStep (1s) is the smallest probe that is far from clock
// resolution and still one unit across a boundary.
const (
	cycleInterval = 24 * time.Hour
	cycleLong     = 5 * time.Hour
	cycleStep     = time.Second
)

func TestCycleDueAt(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	endedAgo := func(ago time.Duration) int64 { return now.Add(-ago).UnixNano() }
	floorThreshold := cycleInterval - minExpectedCycle
	longThreshold := cycleInterval - cycleLong
	tests := []struct {
		name     string
		interval time.Duration
		end      int64
		dur      int64
		want     bool
	}{
		{"disabled interval is never due", 0, endedAgo(1000 * time.Hour), 0, false},
		{"negative interval is never due", -time.Hour, 0, 0, false},
		{"never completed is due", cycleInterval, 0, 0, true},
		{"floor: one under the threshold", cycleInterval, endedAgo(floorThreshold - cycleStep), 0, false},
		{"floor: exactly the threshold", cycleInterval, endedAgo(floorThreshold), 0, true},
		{"floor: one over the threshold", cycleInterval, endedAgo(floorThreshold + cycleStep), 0, true},
		{"a short previous cycle uses the floor", cycleInterval, endedAgo(floorThreshold - cycleStep), int64(minExpectedCycle - cycleStep), false},
		{"previous cycle equal to the floor", cycleInterval, endedAgo(floorThreshold), int64(minExpectedCycle), true},
		{"long previous cycle: one under", cycleInterval, endedAgo(longThreshold - cycleStep), int64(cycleLong), false},
		{"long previous cycle: exactly", cycleInterval, endedAgo(longThreshold), int64(cycleLong), true},
		{"long previous cycle: one over", cycleInterval, endedAgo(longThreshold + cycleStep), int64(cycleLong), true},
		{"a cycle longer than the interval is always due", time.Hour, endedAgo(0), int64(cycleLong), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cycleDueAt(tc.interval, tc.end, tc.dur, now); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
