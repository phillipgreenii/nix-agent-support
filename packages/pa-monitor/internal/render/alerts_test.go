package render

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
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
		ActiveBlock:   &usage.Block{CostUSD: 75},
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
		ActiveBlock:    &usage.Block{CostUSD: 75},
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

func treeWithAuthFailures(n int) *aggregate.Tree {
	var sv []*aggregate.SessionView
	for i := 0; i < n; i++ {
		sv = append(sv, &aggregate.SessionView{
			Session: &session.Session{SessionID: string(rune('a' + i))},
			SessionEnrichment: aggregate.SessionEnrichment{
				LastError: &transcript.ErrorRecord{Kind: transcript.ErrAuthFailed, IsTerminal: true},
			},
		})
	}
	return &aggregate.Tree{Dirs: []*aggregate.Directory{{Sessions: sv}}}
}

func TestAlertsAuthFailureShows(t *testing.T) {
	out := Alerts(treeWithAuthFailures(1), AlertsOpts{Now: time.Now(), Width: 200})
	if !strings.Contains(out, "⊘") || !strings.Contains(out, "AUTHENTICATION FAILURE") {
		t.Errorf("expected auth-failure banner, got: %q", out)
	}
	if !strings.Contains(out, "/login") {
		t.Errorf("expected /login remediation, got: %q", out)
	}
}

func TestAlertsAuthFailureAbsentWhenNone(t *testing.T) {
	out := Alerts(treeWithAuthFailures(0), AlertsOpts{Now: time.Now(), Width: 200})
	if strings.Contains(out, "AUTHENTICATION") {
		t.Errorf("expected no auth banner when none failing, got: %q", out)
	}
}

func TestAlertsAuthFailureTierVariants(t *testing.T) {
	// Narrow tier (80–119 → TierNarrow)
	outNarrow := Alerts(treeWithAuthFailures(1), AlertsOpts{Now: time.Now(), Width: 90})
	if !strings.Contains(outNarrow, "⊘ auth — run /login") {
		t.Errorf("narrow tier: expected '⊘ auth — run /login', got: %q", outNarrow)
	}
	// Tiny tier (< 80 → TierTiny)
	outTiny := Alerts(treeWithAuthFailures(1), AlertsOpts{Now: time.Now(), Width: 40})
	if !strings.Contains(outTiny, "⊘ /login") {
		t.Errorf("tiny tier: expected '⊘ /login', got: %q", outTiny)
	}
}

func TestAlertsAuthFailureSortsFirst(t *testing.T) {
	now := time.Date(2026, 5, 8, 20, 0, 0, 0, time.UTC)
	tree := treeWithAuthFailures(1)
	tree.WindowResetsAt = now.Add(60 * time.Second)
	out := Alerts(tree, AlertsOpts{
		Now: now, Width: 200, AutoResume: true, WindowResetsAt: tree.WindowResetsAt,
	})
	authIdx := strings.Index(out, "⊘")
	resumeIdx := strings.Index(out, "⏸")
	if authIdx == -1 || resumeIdx == -1 || authIdx > resumeIdx {
		t.Errorf("auth banner must sort before resume; got: %q", out)
	}
}

func TestShortVersion(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"26.07.08.12345+abcd1234", "abcd1234"},
		{"dev", "dev"},
		{"", ""},
		{"noplus", "noplus"},
		{"a+b+c", "c"}, // last '+' wins
	}
	for _, tc := range cases {
		if got := shortVersion(tc.in); got != tc.want {
			t.Errorf("shortVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAlertsVersionMismatchWide(t *testing.T) {
	tree := &aggregate.Tree{}
	out := Alerts(tree, AlertsOpts{
		Now:           time.Now(),
		Width:         200,
		ClientVersion: "26.07.08.12345+abcd1234",
		DaemonVersion: "26.07.01.99999+deadbeef",
	})
	if !strings.Contains(out, "abcd1234") {
		t.Errorf("expected short client digest 'abcd1234', got: %q", out)
	}
	if !strings.Contains(out, "deadbeef") {
		t.Errorf("expected short daemon digest 'deadbeef', got: %q", out)
	}
	if !strings.Contains(out, "restart daemon") {
		t.Errorf("expected 'restart daemon' remediation, got: %q", out)
	}
	if !strings.Contains(out, "⚠") {
		t.Errorf("expected warning glyph, got: %q", out)
	}
}

func TestAlertsVersionMismatchAbsentWhenEqual(t *testing.T) {
	out := Alerts(&aggregate.Tree{}, AlertsOpts{
		Now:           time.Now(),
		Width:         200,
		ClientVersion: "26.07.08+aa",
		DaemonVersion: "26.07.08+aa",
	})
	if strings.Contains(out, "daemon") {
		t.Errorf("expected no mismatch segment when versions equal, got: %q", out)
	}
}

func TestAlertsVersionMismatchAbsentWhenDaemonEmpty(t *testing.T) {
	out := Alerts(&aggregate.Tree{}, AlertsOpts{
		Now:           time.Now(),
		Width:         200,
		ClientVersion: "26.07.08+aa",
		DaemonVersion: "",
	})
	if strings.Contains(out, "daemon") {
		t.Errorf("expected no mismatch segment when daemon version empty, got: %q", out)
	}
}

func TestAlertsVersionMismatchNarrow(t *testing.T) {
	out := Alerts(&aggregate.Tree{}, AlertsOpts{
		Now:           time.Now(),
		Width:         90, // NARROW
		ClientVersion: "26.07.08+aa",
		DaemonVersion: "26.07.01+bb",
	})
	if !strings.Contains(out, "⚠ daemon version differs — restart") {
		t.Errorf("narrow tier: expected '⚠ daemon version differs — restart', got: %q", out)
	}
}

func TestAlertsVersionMismatchTiny(t *testing.T) {
	out := Alerts(&aggregate.Tree{}, AlertsOpts{
		Now:           time.Now(),
		Width:         40, // TINY
		ClientVersion: "26.07.08+aa",
		DaemonVersion: "26.07.01+bb",
	})
	if !strings.Contains(out, "⚠ daemon ver") {
		t.Errorf("tiny tier: expected '⚠ daemon ver', got: %q", out)
	}
}

func TestAlertsAuthFailureSortsBeforeVersionMismatch(t *testing.T) {
	tree := treeWithAuthFailures(1)
	out := Alerts(tree, AlertsOpts{
		Now:           time.Now(),
		Width:         200,
		ClientVersion: "26.07.08+abcd1234",
		DaemonVersion: "26.07.01+deadbeef",
	})
	authIdx := strings.Index(out, "/login")
	mismatchIdx := strings.Index(out, "daemon")
	if authIdx == -1 || mismatchIdx == -1 || authIdx > mismatchIdx {
		t.Errorf("auth segment must sort before version mismatch; authIdx=%d mismatchIdx=%d out=%q",
			authIdx, mismatchIdx, out)
	}
}
