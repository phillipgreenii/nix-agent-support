package render

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// Dormant was triggering too early (10m). The threshold is now 20m; a session
// idle 15m must NOT read dormant, one idle 25m must. Guards both the render
// display const (session.LongIdleThreshold) and this boundary.
func TestIsDormantBoundaryIs20Min(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	restore := nowFn
	nowFn = func() time.Time { return base }
	defer func() { nowFn = restore }()

	mk := func(ageMin int) *aggregate.SessionView {
		return &aggregate.SessionView{
			Session: &session.Session{Status: session.Idle, TranscriptMTime: base.Add(-time.Duration(ageMin) * time.Minute)},
		}
	}
	if isDormant(mk(15)) {
		t.Errorf("15m-idle session should NOT be dormant at a 20m threshold")
	}
	if !isDormant(mk(25)) {
		t.Errorf("25m-idle session SHOULD be dormant at a 20m threshold")
	}
}
