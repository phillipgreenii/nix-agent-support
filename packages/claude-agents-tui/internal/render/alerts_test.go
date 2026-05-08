package render

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
	"github.com/phillipgreenii/claude-agents-tui/internal/ccusage"
)

func TestAlertsEmptyWhenNoneActive(t *testing.T) {
	tree := &aggregate.Tree{}
	out := Alerts(tree, AlertsOpts{Now: time.Now(), Width: 200})
	if out != "" {
		t.Errorf("expected empty alerts, got: %q", out)
	}
}

func TestAlertsAutoResumeCountdown(t *testing.T) {
	now := time.Date(2026, 5, 8, 20, 0, 0, 0, time.UTC)
	tree := &aggregate.Tree{WindowResetsAt: now.Add(75 * time.Second)}
	out := Alerts(tree, AlertsOpts{
		Now:             now,
		Width:           200,
		AutoResume:      true,
		WindowResetsAt:  tree.WindowResetsAt,
		AutoResumeDelay: 0,
	})
	if !strings.Contains(out, "⏸") {
		t.Errorf("expected pause glyph in alerts, got: %q", out)
	}
	if !strings.Contains(out, "1:15") {
		t.Errorf("expected countdown 1:15, got: %q", out)
	}
}

func TestAlertsTopupShows(t *testing.T) {
	tree := &aggregate.Tree{
		CCUsageProbed: true,
		PlanCapUSD:    50,
		ActiveBlock:   &ccusage.Block{CostUSD: 75},
	}
	out := Alerts(tree, AlertsOpts{
		Now:           time.Now(),
		Width:         200,
		TopupPoolUSD:  20,
		TopupConsumed: 5,
	})
	if !strings.Contains(out, "Top-up") {
		t.Errorf("expected Top-up segment, got: %q", out)
	}
	if !strings.Contains(out, "$15") {
		t.Errorf("expected remaining amount $15, got: %q", out)
	}
}

func TestAlertsPipeJoinedWhenMultiple(t *testing.T) {
	now := time.Date(2026, 5, 8, 20, 0, 0, 0, time.UTC)
	tree := &aggregate.Tree{
		CCUsageProbed:  true,
		PlanCapUSD:     50,
		ActiveBlock:    &ccusage.Block{CostUSD: 75},
		WindowResetsAt: now.Add(60 * time.Second),
	}
	out := Alerts(tree, AlertsOpts{
		Now:            now,
		Width:          200,
		AutoResume:     true,
		WindowResetsAt: tree.WindowResetsAt,
		TopupPoolUSD:   20,
		TopupConsumed:  5,
	})
	if !strings.Contains(out, " | ") {
		t.Errorf("expected pipe separator between alerts, got: %q", out)
	}
	if !strings.Contains(out, "⏸") || !strings.Contains(out, "Top-up") {
		t.Errorf("expected both auto-resume and top-up segments, got: %q", out)
	}
}

func TestAlertsSingleLineNoTrailingNewline(t *testing.T) {
	now := time.Date(2026, 5, 8, 20, 0, 0, 0, time.UTC)
	tree := &aggregate.Tree{WindowResetsAt: now.Add(30 * time.Second)}
	out := Alerts(tree, AlertsOpts{Now: now, Width: 200, AutoResume: true, WindowResetsAt: tree.WindowResetsAt})
	if strings.Contains(out, "\n") {
		t.Errorf("Alerts must be single line, got: %q", out)
	}
}
