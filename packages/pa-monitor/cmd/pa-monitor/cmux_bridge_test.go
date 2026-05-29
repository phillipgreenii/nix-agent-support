package main

import (
	"strings"
	"testing"
)

// captureLog returns a log function that appends each line to lines.
func captureLog(lines *[]string) func(string) {
	return func(s string) {
		*lines = append(*lines, s)
	}
}

// TestDiffAndLogNoChange asserts diffAndLog stays silent when state did not
// flip between consecutive ticks.
func TestDiffAndLogNoChange(t *testing.T) {
	prev := bridgeState{initialized: true, caffeinateActive: true, autoResumeEnabled: false}
	curr := bridgeState{initialized: true, caffeinateActive: true, autoResumeEnabled: false}
	var lines []string
	got := diffAndLog(prev, curr, captureLog(&lines))
	if len(lines) != 0 {
		t.Fatalf("expected no log lines, got %v", lines)
	}
	if got != curr {
		t.Fatalf("expected returned state == curr, got %+v vs %+v", got, curr)
	}
}

// TestDiffAndLogCaffeinateFlip asserts a single line is logged when
// caffeinate_active flips.
func TestDiffAndLogCaffeinateFlip(t *testing.T) {
	prev := bridgeState{initialized: true, caffeinateActive: false, autoResumeEnabled: false}
	curr := bridgeState{initialized: true, caffeinateActive: true, autoResumeEnabled: false}
	var lines []string
	diffAndLog(prev, curr, captureLog(&lines))
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 log line, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "caffeinate") || !strings.Contains(lines[0], "true") {
		t.Fatalf("expected caffeinate flip line referencing true, got %q", lines[0])
	}
}

// TestDiffAndLogAutoResumeFlip asserts a single line is logged when
// auto_resume_enabled flips.
func TestDiffAndLogAutoResumeFlip(t *testing.T) {
	prev := bridgeState{initialized: true, caffeinateActive: false, autoResumeEnabled: true}
	curr := bridgeState{initialized: true, caffeinateActive: false, autoResumeEnabled: false}
	var lines []string
	diffAndLog(prev, curr, captureLog(&lines))
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 log line, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "auto_resume") || !strings.Contains(lines[0], "false") {
		t.Fatalf("expected auto_resume flip line referencing false, got %q", lines[0])
	}
}

// TestDiffAndLogInitialState asserts that the first tick (prev.initialized
// == false) emits a single "initial state" line summarizing observed
// toggles, NOT N separate flip lines from the zero value.
func TestDiffAndLogInitialState(t *testing.T) {
	var prev bridgeState // zero value, initialized == false
	curr := bridgeState{initialized: true, caffeinateActive: true, autoResumeEnabled: true}
	var lines []string
	got := diffAndLog(prev, curr, captureLog(&lines))
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 initial-state line, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "initial state") {
		t.Fatalf("expected line to mention 'initial state', got %q", lines[0])
	}
	if !strings.Contains(lines[0], "caffeinate=true") {
		t.Fatalf("expected initial-state line to include caffeinate=true, got %q", lines[0])
	}
	if !strings.Contains(lines[0], "auto_resume=true") {
		t.Fatalf("expected initial-state line to include auto_resume=true, got %q", lines[0])
	}
	if !got.initialized {
		t.Fatalf("expected returned state to be marked initialized")
	}
}

// TestDiffAndLogBothFlip asserts simultaneous flips both surface as separate
// lines (not collapsed).
func TestDiffAndLogBothFlip(t *testing.T) {
	prev := bridgeState{initialized: true, caffeinateActive: false, autoResumeEnabled: false}
	curr := bridgeState{initialized: true, caffeinateActive: true, autoResumeEnabled: true}
	var lines []string
	diffAndLog(prev, curr, captureLog(&lines))
	if len(lines) != 2 {
		t.Fatalf("expected 2 log lines for two simultaneous flips, got %d: %v", len(lines), lines)
	}
}
