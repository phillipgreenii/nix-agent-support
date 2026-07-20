package poller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/corpus"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
	"github.com/phillipgreenii/pa-monitor/internal/daemon"
)

// eqStatus writes a <cwd-slug>/<filename> status sibling under home/projects.
func eqStatus(t *testing.T, home, cwd, filename string, lines ...string) {
	t.Helper()
	dir := filepath.Join(home, "projects", eqSlug(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// buildAll4Monitor constructs a Monitor with all four observers over home, Scans
// once at now, and returns it.
func buildAll4Monitor(t *testing.T, sessionsDir, home string, pidAlive func(int) bool, now time.Time) *corpus.Monitor {
	t.Helper()
	mon := corpus.New(home, &session.Discoverer{SessionsDir: sessionsDir, PidAlive: pidAlive})
	mon.Register(corpus.NewSessionSnapshotObserver())
	mon.Register(corpus.NewSubagentErrorObserver())
	mon.Register(corpus.NewUsagePricingObserver(eqPrices))
	mon.Register(corpus.NewLimitsObserver())
	if _, err := mon.Scan(now); err != nil {
		t.Fatalf("Monitor.Scan: %v", err)
	}
	return mon
}

// TestPricingEquivalence_MonitorVsNativeSources is the source-level gate: on one
// shared corpus (main transcripts + a subagent transcript + non-active orphan +
// status siblings, spanning a >=5h block gap), the Monitor's UsagePricing/Limits
// projections MUST equal the old NativePricer / SiblingLimitsSource byte-for-byte.
func TestPricingEquivalence_MonitorVsNativeSources(t *testing.T) {
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	home := filepath.Join(root, "claude-home")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC) // Wednesday, on the hour
	ctx := context.Background()

	// Active session s1: main transcript with an OLD record (block 1) and a recent
	// record (block 2, after a >=5h gap), plus a subagent transcript (billable).
	eqSession(t, sessionsDir, 900101, "s1", "", "/w/a")
	aMain := eqTranscript(
		t, home, "/w/a", "s1.jsonl", now,
		eqUserPrompt("hi"),
		eqAssistantTS("m", 100, 40, now.Add(-6*time.Hour)),    // block 1
		eqAssistantTS("m", 200, 30, now.Add(-30*time.Minute)), // block 2
	)
	eqSubagent(t, aMain, "agent-1.jsonl", now, eqAssistantTS("m", 50, 20, now.Add(-15*time.Minute))) // block 2 (recursive)

	// Non-active orphan transcript (no session record) — only the walk prices it.
	eqTranscript(t, home, "/w/c", "orphan.jsonl", now,
		eqAssistantTS("m", 70, 10, now.Add(-10*time.Minute))) // block 2

	// Status siblings across two project dirs (ADR-0029 window-peak: peak 100 lives
	// in a different file than the newest-ts record).
	rst := now.Add(3 * time.Hour).Unix()
	eqStatus(t, home, "/w/a", "s1.status.jsonl",
		fmt.Sprintf(`{"ts":1000,"five_hour_pct":100,"five_hour_resets_at":%d}`, rst),
		fmt.Sprintf(`{"ts":2000,"five_hour_pct":50,"five_hour_resets_at":%d}`, rst))
	eqStatus(t, home, "/w/b", "other.status.jsonl",
		fmt.Sprintf(`{"ts":1500,"five_hour_pct":90,"five_hour_resets_at":%d}`, rst))

	pidAlive := func(int) bool { return true }

	mon := buildAll4Monitor(t, sessionsDir, home, pidAlive, now)
	pricer := &usage.NativePricer{ClaudeHome: home, Prices: eqPrices, Now: func() time.Time { return now }}
	sibling := &daemon.SiblingLimitsSource{ClaudeHome: home}

	// Block equivalence.
	wantBlock, _ := pricer.ActiveBlock(ctx)
	gotBlock := mon.Block(now)
	if wantBlock == nil {
		t.Fatalf("NativePricer produced a nil block; fixture broken")
	}
	if !reflect.DeepEqual(gotBlock, wantBlock) {
		t.Fatalf("block mismatch:\n Monitor=%+v\n Native =%+v", gotBlock, wantBlock)
	}

	// Weekly equivalence.
	wantWeek, _ := pricer.CurrentWeekly(ctx)
	if !reflect.DeepEqual(mon.Weekly(now), wantWeek) {
		t.Fatalf("weekly mismatch:\n Monitor=%+v\n Native =%+v", mon.Weekly(now), wantWeek)
	}

	// CostProbed equivalence.
	gotProbed, gotErr := mon.CostProbed()
	wantProbed, wantErr := pricer.Probed()
	if gotProbed != wantProbed || (gotErr == nil) != (wantErr == nil) {
		t.Fatalf("CostProbed = (%v,%v), want (%v,%v)", gotProbed, gotErr, wantProbed, wantErr)
	}

	// Limits equivalence.
	wantLimits, _ := sibling.Current(ctx)
	if !reflect.DeepEqual(mon.Limits(), wantLimits) {
		t.Fatalf("limits mismatch:\n Monitor=%+v\n Sibling=%+v", mon.Limits(), wantLimits)
	}
	if wantLimits == nil || wantLimits.FiveHourPct == nil || *wantLimits.FiveHourPct != 100 {
		t.Fatalf("expected peak 100 across files, got %+v", wantLimits)
	}
}

// TestPricingWindow_DropsAncientButKeepsBlock proves the documented windowing
// bound: a record in a file older than the pricing window is EXCLUDED by the
// Monitor, but because a >=5h gap separates it from the recent block, the active
// block is byte-identical to the full-history NativePricer.
func TestPricingWindow_DropsAncientButKeepsBlock(t *testing.T) {
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	home := filepath.Join(root, "claude-home")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC) // Wed; pricing window ~60h (this week)
	ctx := context.Background()

	// Ancient file: mtime + record before this Monday (outside the window). The
	// Monitor never opens it; NativePricer (unbounded) does.
	ancient := now.Add(-4 * 24 * time.Hour) // previous Saturday
	eqTranscript(t, home, "/w/old", "old.jsonl", ancient,
		eqAssistantTS("m", 9999, 9999, ancient))
	// Recent file, well after a >=5h gap.
	eqTranscript(t, home, "/w/new", "new.jsonl", now,
		eqAssistantTS("m", 100, 50, now.Add(-1*time.Hour)))

	pidAlive := func(int) bool { return true }
	mon := buildAll4Monitor(t, sessionsDir, home, pidAlive, now)
	pricer := &usage.NativePricer{ClaudeHome: home, Prices: eqPrices, Now: func() time.Time { return now }}

	wantBlock, _ := pricer.ActiveBlock(ctx)
	gotBlock := mon.Block(now)
	if wantBlock == nil || gotBlock == nil {
		t.Fatalf("expected non-nil active blocks: native=%v monitor=%v", wantBlock, gotBlock)
	}
	// The active block excludes the ancient record in BOTH (it is in an earlier,
	// discarded block for Native; never opened for the Monitor) -> identical.
	if !reflect.DeepEqual(gotBlock, wantBlock) {
		t.Fatalf("windowed block != full-history block (gap should make them equal):\n Monitor=%+v\n Native =%+v", gotBlock, wantBlock)
	}
}
