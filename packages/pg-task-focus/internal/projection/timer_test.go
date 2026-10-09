package projection

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// cycleFrom replays a log of cycle A's events, given as minutes and payloads.
func cycleFrom(t *testing.T, steps ...any) Cycle {
	t.Helper()
	b := newLog(t)
	for i := 0; i < len(steps); i += 2 {
		b.add(steps[i].(int), steps[i+1].(event.Payload))
	}
	return mustCycle(t, mustReplay(t, b.events), cycleA)
}

func TestElapsedIsSumOfSegments(t *testing.T) {
	c := cycleFrom(t,
		0, startOf(cycleA, 50), 10, pauseOf(cycleA), 20, resumeOf(cycleA),
		35, pauseOf(cycleA), 50, resumeOf(cycleA), 60, stopOf(cycleA))
	assertSegments(t, c, seg(0, 10), seg(20, 35), seg(50, 60))
	for _, tt := range []struct {
		min  int
		want time.Duration
	}{{100, 35 * time.Minute}, {60, 35 * time.Minute}, {30, 20 * time.Minute}, {15, 10 * time.Minute}} {
		if got := c.Elapsed(at(tt.min)); got != tt.want {
			t.Errorf("Elapsed(%d) = %v, want %v", tt.min, got, tt.want)
		}
	}
	if got := c.Remaining(at(100)); got != 15*time.Minute {
		t.Errorf("Remaining = %v, want 50m planned less 35m elapsed", got)
	}
}

func TestOpenSegmentEndsAtReadTime(t *testing.T) {
	c := cycleFrom(t, 0, startOf(cycleA, 50), 10, pauseOf(cycleA), 20, resumeOf(cycleA))
	for _, tt := range []struct {
		min  int
		want time.Duration
	}{{20, 10 * time.Minute}, {27, 17 * time.Minute}, {80, 70 * time.Minute}, {0, 0}, {-5, 0}} {
		if got := c.Elapsed(at(tt.min)); got != tt.want {
			t.Errorf("Elapsed(%d) = %v, want %v", tt.min, got, tt.want)
		}
	}
	if got := c.Elapsed(at(27).Add(1500 * time.Millisecond)); got != 17*time.Minute+1500*time.Millisecond {
		t.Errorf("Elapsed keeps no sub-minute precision: %v", got)
	}
}

func TestBoostExtendsRemaining(t *testing.T) {
	c := cycleFrom(t, 0, startOf(cycleA, 25), 5, boostOf(cycleA, 10), 7, boostOf(cycleA, 5))
	if len(c.Boosts) != 2 || c.Boosts[0] != (Boost{At: at(5), Minutes: 10}) {
		t.Errorf("Boosts = %v, want the two boosts with their instants", c.Boosts)
	}
	for _, tt := range []struct {
		min  int
		want time.Duration
	}{{4, 21 * time.Minute}, {5, 30 * time.Minute}, {10, 30 * time.Minute}, {50, -10 * time.Minute}} {
		if got := c.Remaining(at(tt.min)); got != tt.want {
			t.Errorf("Remaining(%d) = %v, want %v", tt.min, got, tt.want)
		}
	}
}

func TestBoostOutOfOvertimeEndsOvertime(t *testing.T) {
	c := cycleFrom(t, 0, startOf(cycleA, 10), 15, boostOf(cycleA, 10))
	if !c.Overtime(at(14)) || c.Remaining(at(14)) != -4*time.Minute {
		t.Errorf("at 14: Overtime %v, Remaining %v, want overtime by 4m", c.Overtime(at(14)), c.Remaining(at(14)))
	}
	if c.Overtime(at(15)) || c.Remaining(at(15)) != 5*time.Minute {
		t.Errorf("at the boost: Overtime %v, Remaining %v, want 5m left", c.Overtime(at(15)), c.Remaining(at(15)))
	}
	if !c.TimeUp(at(20)) || c.Overtime(at(20)) {
		t.Errorf("at 20: TimeUp %v, Overtime %v, want time up with exactly zero left and no overtime yet", c.TimeUp(at(20)), c.Overtime(at(20)))
	}
	if !c.Overtime(at(21)) {
		t.Error("at 21: not in overtime, want overtime again")
	}
}

func TestPausedCycleIsNeverInOvertime(t *testing.T) {
	c := cycleFrom(t, 0, startOf(cycleA, 10), 20, pauseOf(cycleA))
	if c.Overtime(at(30)) {
		t.Error("a paused cycle is in overtime")
	}
	if !c.TimeUp(at(30)) || c.Remaining(at(30)) != -10*time.Minute {
		t.Errorf("TimeUp %v, Remaining %v, want time up 10m over", c.TimeUp(at(30)), c.Remaining(at(30)))
	}
	if _, ok := c.OvertimeSince(at(30)); ok {
		t.Error("OvertimeSince reports overtime for a paused cycle")
	}
	stopped := cycleFrom(t, 0, startOf(cycleA, 10), 20, stopOf(cycleA))
	if stopped.Overtime(at(30)) || stopped.TimeUp(at(30)) {
		t.Error("a stopped cycle is in overtime or time up")
	}
	if c.TimeUp(at(-1)) {
		t.Error("a cycle is time up before it starts")
	}
}

func TestOvertimeSinceAcrossBoostsAndResume(t *testing.T) {
	// Planned 10: time is up at 10. A pause from 15 to 25 keeps the overtime;
	// a boost of 5 at 30 leaves it in overtime (since 10 still); a boost of
	// 20 at 32 ends it with 13 minutes left, and after a pause from 40 to 50
	// it runs out again at 55.
	c := cycleFrom(t,
		0, startOf(cycleA, 10), 15, pauseOf(cycleA), 25, resumeOf(cycleA),
		30, boostOf(cycleA, 5), 32, boostOf(cycleA, 20), 40, pauseOf(cycleA), 50, resumeOf(cycleA))
	tests := []struct {
		min   int
		since int
		ok    bool
	}{
		{5, 0, false},
		{10, 0, false},
		{12, 10, true},
		{20, 0, false},
		{28, 10, true},
		{31, 10, true},
		{33, 0, false},
		{55, 0, false},
		{56, 55, true},
		{90, 55, true},
	}
	for _, tt := range tests {
		got, ok := c.OvertimeSince(at(tt.min))
		if ok != tt.ok || (ok && !got.Equal(at(tt.since))) {
			t.Errorf("OvertimeSince(%d) = %v %v, want %v %v", tt.min, got, ok, at(tt.since), tt.ok)
		}
	}
	if got := c.Remaining(at(32)); got != 13*time.Minute {
		t.Errorf("Remaining after the second boost = %v, want 13m", got)
	}

	t.Run("time up mid-segment with second precision", func(t *testing.T) {
		c := cycleFrom(t, 0, startOf(cycleA, 1), 0, boostOf(cycleA, 1))
		got, ok := c.OvertimeSince(at(5))
		if !ok || !got.Equal(at(2)) {
			t.Errorf("OvertimeSince = %v %v, want %v", got, ok, at(2))
		}
	})
}
