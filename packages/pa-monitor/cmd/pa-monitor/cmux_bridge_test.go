package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/config"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/tui"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestCmuxBridgeReporterOptionsHonorsKillSwitch pins pg2-4x2g3: the bridge's
// sidebar reporter must honor the cmux_sidebar_enable config kill switch
// (previously the bridge hardcoded Enable:true, so cmux_sidebar_enable=false
// never disabled the sidebar). Async is always set — the sidebar paint shells
// out to `cmux` and must never run on the gRPC receive loop.
func TestCmuxBridgeReporterOptionsHonorsKillSwitch(t *testing.T) {
	if got := cmuxBridgeReporterOptions(config.Config{CmuxSidebarEnable: false}, nil); got.Enable {
		t.Errorf("Enable = true; want false when cmux_sidebar_enable=false")
	}
	got := cmuxBridgeReporterOptions(config.Config{CmuxSidebarEnable: true}, nil)
	if !got.Enable {
		t.Errorf("Enable = false; want true when cmux_sidebar_enable=true")
	}
	if !got.Async {
		t.Errorf("Async = false; want true (sidebar paint must not block the gRPC loop)")
	}
}

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

// TestConnAnnouncerDebouncesTransientOutage: a drop that recovers within
// announceAfter never reaches the pane (no "Lost"/"restored" term lines), but
// the gauge and detail log still record every transition.
func TestConnAnnouncerDebouncesTransientOutage(t *testing.T) {
	var term []string
	var details int
	var gauge []bool
	now := time.Unix(0, 0)
	a := &connAnnouncer{
		term:          func(s string) { term = append(term, s) },
		detail:        func(string, map[string]string) { details++ },
		gauge:         func(c bool) { gauge = append(gauge, c) },
		now:           func() time.Time { return now },
		announceAfter: 20 * time.Second,
	}
	a.connected() // clean startup: gauge true, no term
	now = now.Add(1 * time.Second)
	a.disconnected(map[string]string{"error": "x"}) // drop begins
	now = now.Add(2 * time.Second)                  // only 2s down (< 20s)
	a.disconnected(map[string]string{"error": "y"})
	a.connected() // recovered inside the debounce window

	if len(term) != 0 {
		t.Errorf("transient outage must stay off the pane, got term %v", term)
	}
	if details != 2 {
		t.Errorf("detail log should record both drops, got %d", details)
	}
	wantGauge := []bool{true, false, true}
	if !reflect.DeepEqual(gauge, wantGauge) {
		t.Errorf("gauge = %v, want %v", gauge, wantGauge)
	}
}

// TestConnAnnouncerAnnouncesSustainedOutage: once the daemon has been
// unreachable for announceAfter, the pane shows "Lost connection", and a later
// recovery shows "Connection to daemon restored".
func TestConnAnnouncerAnnouncesSustainedOutage(t *testing.T) {
	var term []string
	now := time.Unix(0, 0)
	a := &connAnnouncer{
		term:          func(s string) { term = append(term, s) },
		detail:        func(string, map[string]string) {},
		gauge:         func(bool) {},
		now:           func() time.Time { return now },
		announceAfter: 20 * time.Second,
	}
	a.disconnected(map[string]string{"error": "x"}) // t=0
	now = now.Add(10 * time.Second)
	a.disconnected(map[string]string{"error": "x"}) // t=10, still under threshold
	if len(term) != 0 {
		t.Fatalf("before threshold the pane must be silent, got %v", term)
	}
	now = now.Add(15 * time.Second) // t=25, past the 20s threshold
	a.disconnected(map[string]string{"error": "x"})
	a.connected()

	wantTerm := []string{"Lost connection to daemon", "Connection to daemon restored"}
	if !reflect.DeepEqual(term, wantTerm) {
		t.Errorf("term = %v, want %v", term, wantTerm)
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
	defer func() { _ = f.Close() }()
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
	diffAndLog(prev, curr, captureLog(&lines))
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
	got := diffAndLog(prev, curr, captureLog(&lines))
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

// TestLogDaemonVersionMismatchWarnsOnDiffer asserts a warning is emitted when
// the bridge's own version and the daemon's reported version are both non-empty
// and differ. The check is separate from diffAndLog so it runs on every
// (re)connection (diffAndLog's "initial state" line is emitted once per process,
// so it could not carry a per-reconnect version check).
func TestLogDaemonVersionMismatchWarnsOnDiffer(t *testing.T) {
	var lines []string
	logDaemonVersionMismatch("26.07.08+bridge", "26.07.01+daemon", captureLog(&lines))
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 warning line, got %d: %v", len(lines), lines)
	}
	// §6: the warning must advise restarting the CLIENT (this bridge), since the
	// feature targets the newer-daemon case. Assert against the shared constant
	// so a future reword updates both sites together.
	if lines[0] != reexecMismatchWarnLine {
		t.Fatalf("expected the mismatch warning %q, got %q", reexecMismatchWarnLine, lines[0])
	}
	if !strings.Contains(lines[0], "restart this bridge") {
		t.Fatalf("mismatch warning must advise restarting the client, got %q", lines[0])
	}
}

// TestLogDaemonVersionMismatchSilentOnMatch asserts equal versions warn nothing.
func TestLogDaemonVersionMismatchSilentOnMatch(t *testing.T) {
	var lines []string
	logDaemonVersionMismatch("26.07.08+same", "26.07.08+same", captureLog(&lines))
	if len(lines) != 0 {
		t.Fatalf("expected no lines for matching versions, got %v", lines)
	}
}

// TestLogDaemonVersionMismatchSilentOnEmptyDaemonVersion asserts an empty daemon
// version never warns (Mismatch with "" is false), even when the bridge's own
// version is non-empty.
func TestLogDaemonVersionMismatchSilentOnEmptyDaemonVersion(t *testing.T) {
	var lines []string
	logDaemonVersionMismatch("26.07.08+bridge", "", captureLog(&lines))
	if len(lines) != 0 {
		t.Fatalf("expected no lines when daemon version is empty, got %v", lines)
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

// TestSnapshotForWorkspaceWorkspaceColor asserts the workspace-color wiring in
// snapshotForWorkspace: it drives WorkspaceColor/HasWorkspaceColor from
// render.CmuxFiveHourColor over the 5h fields (FiveHourPct + FiveHourResetsAt),
// NOT the paused window reset, and appends the red-branch countdown onto the
// progress label. Uses a fixed now (never time.Now()) so the countdown math is
// deterministic.
func TestSnapshotForWorkspaceWorkspaceColor(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	const staleAfter = 10 * time.Minute
	const ws = "workspace:1"
	fp := func(v float64) *float64 { return &v }
	ts := func(tm time.Time) *timestamppb.Timestamp { return timestamppb.New(tm) }

	// workingDirs builds a Dirs slice with one session in ws marked "working" so
	// snapshotForWorkspace sees a live matching state to roll up.
	workingDirs := func() []*pb.Directory {
		return []*pb.Directory{{Sessions: []*pb.SessionView{
			{CmuxWorkspaceId: ws, Status: "working", Pid: 111, SessionId: "sid-a"},
		}}}
	}

	cases := []struct {
		name            string
		state           *pb.DaemonState
		wantHasColor    bool
		wantColor       string
		wantLabelSuffix string // "" means don't assert the label
	}{
		{
			// pct 85 -> red; reset now+2h30m -> "(2h 30m)". LimitsCapturedAt=now
			// (non-stale) + FiveHourPct set makes CmuxBlockProgress yield a label
			// the countdown rides on: "block 85% (2h 30m)".
			name: "red with countdown on label",
			state: &pb.DaemonState{
				Dirs:             workingDirs(),
				FiveHourPct:      fp(85),
				FiveHourResetsAt: ts(now.Add(2*time.Hour + 30*time.Minute)),
				LimitsCapturedAt: ts(now),
			},
			wantHasColor:    true,
			wantColor:       "#cc3333",
			wantLabelSuffix: "(2h 30m)",
		},
		{
			// nil pct -> color clears, but the bridge still has an opinion.
			name: "clear when pct is nil",
			state: &pb.DaemonState{
				Dirs:             workingDirs(),
				FiveHourPct:      nil,
				FiveHourResetsAt: ts(now.Add(2 * time.Hour)),
				LimitsCapturedAt: ts(now),
			},
			wantHasColor: true,
			wantColor:    "",
		},
		{
			// WindowResetsAt (paused) differs from FiveHourResetsAt (5h). The
			// countdown must reflect the 5h reset (now+1h -> "(1h 0m)"), proving
			// the color/countdown path reads the 5h field, not the pause field.
			// WindowResetsAt being set flips State to StatePaused — expected; we
			// assert only the color and label here.
			name: "uses 5h reset not window_resets_at",
			state: &pb.DaemonState{
				Dirs:             workingDirs(),
				FiveHourPct:      fp(85),
				FiveHourResetsAt: ts(now.Add(1 * time.Hour)),
				WindowResetsAt:   ts(now.Add(5 * time.Hour)),
				LimitsCapturedAt: ts(now),
			},
			wantHasColor:    true,
			wantColor:       "#cc3333",
			wantLabelSuffix: "(1h 0m)",
		},
		{
			// pct 50, reset now+4h -> rem=14400, ptb=(18000-14400)*100/18000=20;
			// 50 > 20 -> yellow (ahead of pace).
			name: "yellow ahead of pace",
			state: &pb.DaemonState{
				Dirs:             workingDirs(),
				FiveHourPct:      fp(50),
				FiveHourResetsAt: ts(now.Add(4 * time.Hour)),
				LimitsCapturedAt: ts(now),
			},
			wantHasColor: true,
			wantColor:    "#e0b000",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := snapshotForWorkspace(tc.state, ws, now, staleAfter)
			if snap.HasWorkspaceColor != tc.wantHasColor {
				t.Errorf("HasWorkspaceColor = %v, want %v", snap.HasWorkspaceColor, tc.wantHasColor)
			}
			if snap.WorkspaceColor != tc.wantColor {
				t.Errorf("WorkspaceColor = %q, want %q", snap.WorkspaceColor, tc.wantColor)
			}
			if tc.wantLabelSuffix != "" && !strings.HasSuffix(snap.ProgressLabel, tc.wantLabelSuffix) {
				t.Errorf("ProgressLabel = %q, want suffix %q", snap.ProgressLabel, tc.wantLabelSuffix)
			}
		})
	}
}

// TestBridgeShutdownSignalsIncludePaneClose pins bead pg2-gveej: the bridge must
// catch SIGHUP (the signal a closing cmux pane delivers) in addition to the
// daemon's SIGINT/SIGTERM, so its deferred reporter.Clear() runs on exit instead
// of the process dying on the default disposition with a stale sidebar.
func TestBridgeShutdownSignalsIncludePaneClose(t *testing.T) {
	want := map[os.Signal]bool{syscall.SIGINT: false, syscall.SIGTERM: false, syscall.SIGHUP: false}
	for _, s := range bridgeShutdownSignals {
		if _, ok := want[s]; ok {
			want[s] = true
		}
	}
	for s, present := range want {
		if !present {
			t.Errorf("bridgeShutdownSignals missing %v", s)
		}
	}
}

// TestBridgeShutdownContextCancelsOnSignal verifies the signal→cancel wiring
// (bead pg2-gveej): a delivered SIGHUP cancels the bridge's context, which is
// what lets runCmuxBridge return and its deferred reporter.Clear() fire. Uses
// SIGHUP specifically because `go test` does not special-case it, so delivering
// it to our own process is safe once NotifyContext has installed the handler.
func TestBridgeShutdownContextCancelsOnSignal(t *testing.T) {
	ctx, stop := newBridgeShutdownContext()
	defer stop()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("kill self with SIGHUP: %v", err)
	}
	select {
	case <-ctx.Done():
		// success: the signal cancelled the context
	case <-time.After(2 * time.Second):
		t.Fatal("context not cancelled within 2s of SIGHUP")
	}
}
