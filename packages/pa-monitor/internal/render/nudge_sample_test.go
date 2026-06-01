package render

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// TestNudgeRowSampleOutputs prints sample tree rows for the three scenarios
// the bead asks the PR description to include: pending, recent-not-pending,
// neither. Run with `go test -v -run TestNudgeRowSampleOutputs ./internal/render/`.
func TestNudgeRowSampleOutputs(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	mk := func(e aggregate.SessionEnrichment) *aggregate.SessionView {
		return &aggregate.SessionView{
			Session:           &session.Session{SessionID: "abc123de", Name: "feature-x", Status: session.Idle},
			SessionEnrichment: e,
		}
	}
	opts := TreeOpts{Width: 120, Theme: NewTheme(false)}

	withFrozenNow(t, now, func() {
		pending := mk(aggregate.SessionEnrichment{
			PendingNudge: &aggregate.PendingNudge{Sources: []string{"manual"}},
		})
		recent := mk(aggregate.SessionEnrichment{
			LastNudgedAt:     now.Add(-30 * time.Second),
			LastNudgeSources: []string{"disrupted"},
		})
		neither := mk(aggregate.SessionEnrichment{})

		t.Logf("--- tree row (pending) ---\n%s--- end ---", renderSession(pending, opts, "└─", false))
		t.Logf("--- tree row (recent, not pending) ---\n%s--- end ---", renderSession(recent, opts, "└─", false))
		t.Logf("--- tree row (neither) ---\n%s--- end ---", renderSession(neither, opts, "└─", false))
	})
}
