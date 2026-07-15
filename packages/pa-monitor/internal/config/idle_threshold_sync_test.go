package config

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// TestIdleThresholdDefaultMatchesLongIdleConst guards the sync invariant that is
// documented on BOTH sides but was otherwise untested: config.go's IdleThreshold
// default (the daemon's live per-poll idle→long-idle cutoff) and
// session.LongIdleThreshold (the TUI's display-side "dormant" cutoff) both carry
// a "Kept in sync" comment and MUST hold the same value so the display and the
// live path agree at the default. Bumping one but not the other would silently
// drift the two surfaces; this catches that drift.
func TestIdleThresholdDefaultMatchesLongIdleConst(t *testing.T) {
	if got := defaults().IdleThreshold; got != session.LongIdleThreshold {
		t.Errorf("config default IdleThreshold = %v, but session.LongIdleThreshold = %v; "+
			"they MUST stay in sync (see the 'Kept in sync' comments in config.go and session.go)",
			got, session.LongIdleThreshold)
	}
}
