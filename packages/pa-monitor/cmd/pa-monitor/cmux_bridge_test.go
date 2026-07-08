package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/tui"
)

// captureLog returns a log function that appends each line to lines.
func captureLog(lines *[]string) func(string) {
	return func(s string) {
		*lines = append(*lines, s)
	}
}

func TestFormatBridgeLine(t *testing.T) {
	ts := time.Date(2026, 6, 23, 15, 4, 5, 0, time.UTC)
	got := formatBridgeLine(ts, "Lost connection to daemon")
	want := "2026-06-23 15:04:05 Lost connection to daemon"
	if got != want {
		t.Errorf("formatBridgeLine = %q, want %q", got, want)
	}
}

func TestCaffeinatePhraseLowercase(t *testing.T) {
	if caffeinatePhrase(true) != "Caffeinated enabled" {
		t.Errorf("got %q", caffeinatePhrase(true))
	}
	if caffeinatePhrase(false) != "Caffeinated disabled" {
		t.Errorf("got %q", caffeinatePhrase(false))
	}
	if autoNudgePhrase(true) != "Auto Nudge enabled" {
		t.Errorf("got %q", autoNudgePhrase(true))
	}
	if autoNudgePhrase(false) != "Auto Nudge disabled" {
		t.Errorf("got %q", autoNudgePhrase(false))
	}
}

func TestConnAnnouncerTransitions(t *testing.T) {
	var term []string
	var details []string
	var gauge []bool
	a := &connAnnouncer{
		term:   func(s string) { term = append(term, s) },
		detail: func(event string, _ map[string]string) { details = append(details, event) },
		gauge:  func(c bool) { gauge = append(gauge, c) },
	}
	a.connected()                                   // clean startup: no "restored", gauge true
	a.disconnected(map[string]string{"error": "x"}) // one "Lost", detail
	a.disconnected(map[string]string{"error": "y"}) // still one "Lost", another detail
	a.connected()                                   // one "restored"
	wantTerm := []string{"Lost connection to daemon", "Connection to daemon restored"}
	if !reflect.DeepEqual(term, wantTerm) {
		t.Errorf("term = %v, want %v", term, wantTerm)
	}
	if len(details) != 2 {
		t.Errorf("details = %v, want 2", details)
	}
	wantGauge := []bool{true, false, true}
	if !reflect.DeepEqual(gauge, wantGauge) {
		t.Errorf("gauge = %v, want %v", gauge, wantGauge)
	}
}

func TestBridgeLoggerRouting(t *testing.T) {
	dir := t.TempDir()
	var pane bytes.Buffer
	bl := &bridgeLogger{
		now:  func() time.Time { return time.Date(2026, 6, 23, 15, 4, 5, 0, time.UTC) },
		file: &tui.ErrorLogger{CacheDir: dir, FileName: "cmux-bridge.log"},
		emit: nil, // nil-safe
		out:  nil, // set below
	}
	// Term writes to out; Detail must not. Use a temp file as out so we can
	// inspect what reached the pane. (os.File required by the struct.)
	f, err := os.CreateTemp(dir, "pane-*")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	bl.out = f

	bl.Detail("daemon.disconnect", map[string]string{"error": "x"})
	bl.Term("Lost connection to daemon")

	// Pane file: only the Term line, no detail.
	paneBytes, _ := os.ReadFile(f.Name())
	pane.Write(paneBytes)
	if !strings.Contains(pane.String(), "2026-06-23 15:04:05 Lost connection to daemon") {
		t.Errorf("Term line missing from pane: %q", pane.String())
	}
	if strings.Contains(pane.String(), "daemon.disconnect") {
		t.Errorf("Detail leaked to pane: %q", pane.String())
	}
	// Detail must land in the log file.
	logBytes, _ := os.ReadFile(filepath.Join(dir, "cmux-bridge.log"))
	if !strings.Contains(string(logBytes), "daemon.disconnect") {
		t.Errorf("Detail missing from log file: %q", string(logBytes))
	}
}

// TestDiffAndLogNoChange asserts diffAndLog stays silent when state did not
// flip between consecutive ticks.
func TestDiffAndLogNoChange(t *testing.T) {
	prev := bridgeState{initialized: true, caffeinateActive: true, autoResumeEnabled: false}
	curr := bridgeState{initialized: true, caffeinateActive: true, autoResumeEnabled: false}
	var lines []string
	got := diffAndLog(prev, curr, "self", captureLog(&lines))
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
	diffAndLog(prev, curr, "self", captureLog(&lines))
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 log line, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "Caffeinated enabled") {
		t.Fatalf("expected caffeinate-on phrase, got %q", lines[0])
	}
}

// TestDiffAndLogAutoResumeFlip asserts a single line is logged when
// auto_resume_enabled flips.
func TestDiffAndLogAutoResumeFlip(t *testing.T) {
	prev := bridgeState{initialized: true, caffeinateActive: false, autoResumeEnabled: true}
	curr := bridgeState{initialized: true, caffeinateActive: false, autoResumeEnabled: false}
	var lines []string
	diffAndLog(prev, curr, "self", captureLog(&lines))
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 log line, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "Auto Nudge disabled") {
		t.Fatalf("expected auto-nudge-off phrase, got %q", lines[0])
	}
}

// TestDiffAndLogInitialState asserts that the first tick (prev.initialized
// == false) emits a single "initial state" line summarizing observed
// toggles, NOT N separate flip lines from the zero value.
func TestDiffAndLogInitialState(t *testing.T) {
	var prev bridgeState // zero value, initialized == false
	curr := bridgeState{initialized: true, caffeinateActive: true, autoResumeEnabled: true}
	var lines []string
	got := diffAndLog(prev, curr, "self", captureLog(&lines))
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 initial-state line, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "initial state") {
		t.Fatalf("expected line to mention 'initial state', got %q", lines[0])
	}
	if !strings.Contains(lines[0], "Caffeinated enabled") {
		t.Fatalf("expected initial-state line to include 'Caffeinated enabled', got %q", lines[0])
	}
	if !strings.Contains(lines[0], "Auto Nudge enabled") {
		t.Fatalf("expected initial-state line to include 'Auto Nudge enabled', got %q", lines[0])
	}
	if !got.initialized {
		t.Fatalf("expected returned state to be marked initialized")
	}
}

// TestDiffAndLogInitialStateVersionMismatch asserts that when the bridge's own
// version and the daemon's reported version are both non-empty and differ, the
// initial-state tick emits a second "⚠ daemon version differs" line after the
// "initial state" summary.
func TestDiffAndLogInitialStateVersionMismatch(t *testing.T) {
	var prev bridgeState // zero value, initialized == false
	curr := bridgeState{initialized: true, caffeinateActive: true, autoResumeEnabled: true, daemonVersion: "26.07.01+daemon"}
	var lines []string
	diffAndLog(prev, curr, "26.07.08+bridge", captureLog(&lines))
	if len(lines) != 2 {
		t.Fatalf("expected exactly 2 lines (initial + mismatch warning), got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "initial state") {
		t.Fatalf("expected first line to be the initial-state summary, got %q", lines[0])
	}
	if !strings.Contains(lines[1], "⚠ daemon version differs from this bridge — restart daemon") {
		t.Fatalf("expected second line to be the mismatch warning, got %q", lines[1])
	}
}

// TestDiffAndLogInitialStateVersionMatch asserts that equal bridge/daemon
// versions emit only the single initial-state line (no warning).
func TestDiffAndLogInitialStateVersionMatch(t *testing.T) {
	var prev bridgeState // zero value, initialized == false
	curr := bridgeState{initialized: true, caffeinateActive: true, autoResumeEnabled: true, daemonVersion: "26.07.08+same"}
	var lines []string
	diffAndLog(prev, curr, "26.07.08+same", captureLog(&lines))
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 line for matching versions, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "initial state") {
		t.Fatalf("expected the single line to be the initial-state summary, got %q", lines[0])
	}
}

// TestDiffAndLogInitialStateEmptyDaemonVersion asserts that an empty daemon
// version never warns (Mismatch with "" is false), even when the bridge's own
// version is non-empty.
func TestDiffAndLogInitialStateEmptyDaemonVersion(t *testing.T) {
	var prev bridgeState                                                                    // zero value, initialized == false
	curr := bridgeState{initialized: true, caffeinateActive: true, autoResumeEnabled: true} // daemonVersion == ""
	var lines []string
	diffAndLog(prev, curr, "26.07.08+bridge", captureLog(&lines))
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 line when daemon version is empty, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "initial state") {
		t.Fatalf("expected the single line to be the initial-state summary, got %q", lines[0])
	}
}

// TestDiffAndLogBothFlip asserts simultaneous flips both surface as separate
// lines (not collapsed).
func TestDiffAndLogBothFlip(t *testing.T) {
	prev := bridgeState{initialized: true, caffeinateActive: false, autoResumeEnabled: false}
	curr := bridgeState{initialized: true, caffeinateActive: true, autoResumeEnabled: true}
	var lines []string
	diffAndLog(prev, curr, "self", captureLog(&lines))
	if len(lines) != 2 {
		t.Fatalf("expected 2 log lines for two simultaneous flips, got %d: %v", len(lines), lines)
	}
}

// TestDiffSessionsInitialEmitsFullRoster: on the first observation we emit
// a "+pid sid/name" line for every session in the workspace so pane
// operators get a roster at bridge startup.
func TestDiffSessionsInitialEmitsFullRoster(t *testing.T) {
	curr := bridgeSessions{initialized: true, byPID: map[int]bridgeSessionInfo{
		1234: {SessionID: "sid-a", Name: "feature-x"},
		5678: {SessionID: "sid-b", Name: "scratch"},
	}}
	var lines []string
	diffSessionsAndLog(bridgeSessions{}, curr, captureLog(&lines))
	if len(lines) != 2 {
		t.Fatalf("expected 2 initial-roster lines, got %d: %v", len(lines), lines)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "+1234 sid-a/feature-x") {
		t.Errorf("missing +1234 sid-a/feature-x in:\n%s", joined)
	}
	if !strings.Contains(joined, "+5678 sid-b/scratch") {
		t.Errorf("missing +5678 sid-b/scratch in:\n%s", joined)
	}
}

// TestDiffSessionsLogsAdditionAndRemoval: + when a new pid appears, - when
// a known pid disappears; identical sets emit nothing.
func TestDiffSessionsLogsAdditionAndRemoval(t *testing.T) {
	prev := bridgeSessions{initialized: true, byPID: map[int]bridgeSessionInfo{1234: {SessionID: "sid-old", Name: "old"}}}
	curr := bridgeSessions{initialized: true, byPID: map[int]bridgeSessionInfo{5678: {SessionID: "sid-new", Name: "new"}}}
	var lines []string
	diffSessionsAndLog(prev, curr, captureLog(&lines))
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "+5678 sid-new/new") {
		t.Errorf("expected +5678 sid-new/new in:\n%s", joined)
	}
	if !strings.Contains(joined, "-1234 sid-old/old") {
		t.Errorf("expected -1234 sid-old/old in:\n%s", joined)
	}
}

// TestDiffSessionsSilentWhenUnchanged: identical session sets across two
// ticks emit no lines.
func TestDiffSessionsSilentWhenUnchanged(t *testing.T) {
	same := bridgeSessions{initialized: true, byPID: map[int]bridgeSessionInfo{1234: {SessionID: "sid-a", Name: "feature-x"}}}
	var lines []string
	diffSessionsAndLog(same, same, captureLog(&lines))
	if len(lines) != 0 {
		t.Fatalf("expected no log lines for unchanged set, got %v", lines)
	}
}

// TestFormatSessionEntryFallbacks: when SessionID or Name is empty,
// formatSessionEntry uses whichever is present rather than emitting an
// empty "/" separator.
func TestFormatSessionEntryFallbacks(t *testing.T) {
	if got := formatSessionEntry(1, bridgeSessionInfo{SessionID: "sid"}); got != "1 sid" {
		t.Errorf("sid-only fallback: got %q want %q", got, "1 sid")
	}
	if got := formatSessionEntry(2, bridgeSessionInfo{Name: "n"}); got != "2 n" {
		t.Errorf("name-only fallback: got %q want %q", got, "2 n")
	}
	if got := formatSessionEntry(3, bridgeSessionInfo{}); got != "3 <unknown>" {
		t.Errorf("empty fallback: got %q want %q", got, "3 <unknown>")
	}
}
