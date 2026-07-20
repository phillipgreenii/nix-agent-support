package corpus

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

// assistantLineTS is a timestamped assistant usage line (pricing records are
// keyed on timestamp, so the untimed assistantLine helper would anchor blocks at
// the zero time and never be "active").
func assistantLineTS(model string, in, out int, ts time.Time) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"role":"assistant","model":%q,"usage":{"input_tokens":%d,"output_tokens":%d,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},"content":[]}}`,
		ts.Format(time.RFC3339Nano), model, in, out)
}

func newMonitorAllObservers(home string, disc *session.Discoverer, prices usage.PriceTable) (*Monitor, *UsagePricingObserver, *LimitsObserver) {
	m := New(home, disc)
	m.Register(NewSessionSnapshotObserver())
	m.Register(NewSubagentErrorObserver())
	po := NewUsagePricingObserver(prices)
	lo := NewLimitsObserver()
	m.Register(po)
	m.Register(lo)
	return m, po, lo
}

// TestScan_PopulatesPricingAndLimits proves the Monitor's walk feeds pricing from
// a NON-active in-window transcript (no session record) and feeds limits from a
// status sibling — files beyond the active-session set.
func TestScan_PopulatesPricingAndLimits(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	home := filepath.Join(root, "claude-home")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Active session with a priced transcript.
	dirProj := projectDir(t, home, "/tmp/proj")
	writeSessionRecord(t, sessionsDir, 111, "s1", "/tmp/proj", "")
	writeTranscript(t, dirProj, "s1.jsonl", now, assistantLineTS("m", 100, 50, now.Add(-1*time.Hour)))
	// A status sibling in the same project dir (rate_limits at 42%).
	writeStatus(t, filepath.Join(dirProj, "s1.status.jsonl"),
		fmt.Sprintf(`{"ts":%d,"five_hour_pct":42,"five_hour_resets_at":%d}`+"\n", now.Unix(), now.Add(3*time.Hour).Unix()),
		now)

	// A NON-active transcript in a different project dir (no session record) — only
	// the walk can surface it for pricing.
	dirOther := projectDir(t, home, "/tmp/other")
	writeTranscript(t, dirOther, "orphan.jsonl", now, assistantLineTS("m", 200, 30, now.Add(-2*time.Hour)))

	disc := &session.Discoverer{SessionsDir: sessionsDir, PidAlive: func(int) bool { return true }, ReadEnv: stubEnv}
	m, _, _ := newMonitorAllObservers(home, disc, pricingFixture())
	if _, err := m.Scan(now); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	block := m.Block(now)
	if block == nil {
		t.Fatalf("Block = nil, want an active block")
	}
	// Default prices input 5/Mtok, output 25/Mtok. Both files: input 300, output 80.
	want := 300.0/1e6*5 + 80.0/1e6*25
	if diff := block.CostUSD - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("Block.CostUSD = %v, want %v (active + non-active transcripts both priced)", block.CostUSD, want)
	}

	lim := m.Limits()
	if lim == nil || lim.FiveHourPct == nil || *lim.FiveHourPct != 42 {
		t.Fatalf("Limits = %+v, want FiveHourPct 42 from the status sibling", lim)
	}
}

// TestScan_ActiveTranscriptFoldedOnceFeedsBoth proves an active session's
// transcript is folded ONCE (session loop) and feeds BOTH SessionSnapshot and
// UsagePricing — the walk does not re-fold it (dedup via foldedPaths).
func TestScan_ActiveTranscriptFoldedOnceFeedsBoth(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	home := filepath.Join(root, "claude-home")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := projectDir(t, home, "/tmp/proj")
	writeSessionRecord(t, sessionsDir, 111, "s1", "/tmp/proj", "")
	writeTranscript(t, dir, "s1.jsonl", now, assistantLineTS("m", 100, 50, now.Add(-1*time.Hour)))

	disc := &session.Discoverer{SessionsDir: sessionsDir, PidAlive: func(int) bool { return true }, ReadEnv: stubEnv}
	m, _, _ := newMonitorAllObservers(home, disc, pricingFixture())
	if _, err := m.Scan(now); err != nil {
		t.Fatal(err)
	}

	if snap, ok := m.SessionSnapshot("s1"); !ok || snap.TotalTokens != 50 {
		t.Fatalf("SessionSnapshot(s1) not fed: (%+v,%v)", snap, ok)
	}
	if m.Block(now) == nil {
		t.Fatalf("Block nil — active transcript not routed to pricing")
	}
	if got := m.PricingFilesLastScan(); got != 1 {
		t.Errorf("PricingFilesLastScan = %d, want 1 (single transcript, not double-counted)", got)
	}
	if got := m.TranscriptScansLastScan(); got != 1 {
		t.Errorf("TranscriptScansLastScan = %d, want 1 (folded once; walk skipped via foldedPaths)", got)
	}
}

// TestScan_PrunesVanishedPricingAndStatus proves a vanished non-active transcript
// and a vanished status sibling are dropped from the pricing / limits projections.
func TestScan_PrunesVanishedPricingAndStatus(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	home := filepath.Join(root, "claude-home")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := projectDir(t, home, "/tmp/proj")
	writeTranscript(t, dir, "a.jsonl", now, assistantLineTS("m", 10, 5, now.Add(-1*time.Hour)))
	orphan := writeTranscript(t, dir, "b.jsonl", now, assistantLineTS("m", 20, 5, now.Add(-1*time.Hour)))
	statusB := filepath.Join(dir, "b.status.jsonl")
	writeStatus(t, filepath.Join(dir, "a.status.jsonl"), fmt.Sprintf(`{"ts":%d,"five_hour_pct":10,"five_hour_resets_at":%d}`+"\n", now.Unix(), now.Add(3*time.Hour).Unix()), now)
	writeStatus(t, statusB, fmt.Sprintf(`{"ts":%d,"five_hour_pct":20,"five_hour_resets_at":%d}`+"\n", now.Unix(), now.Add(3*time.Hour).Unix()), now)

	disc := &session.Discoverer{SessionsDir: sessionsDir, PidAlive: func(int) bool { return true }, ReadEnv: stubEnv}
	m, po, lo := newMonitorAllObservers(home, disc, pricingFixture())
	if _, err := m.Scan(now); err != nil {
		t.Fatal(err)
	}
	if len(po.recs) != 2 || len(lo.recs) != 2 {
		t.Fatalf("after scan: pricing paths=%d limits paths=%d, want 2/2", len(po.recs), len(lo.recs))
	}

	// b.jsonl and b.status.jsonl vanish.
	if err := os.Remove(orphan); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(statusB); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Scan(now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(po.recs) != 1 || len(lo.recs) != 1 {
		t.Fatalf("after prune: pricing paths=%d limits paths=%d, want 1/1", len(po.recs), len(lo.recs))
	}
}

// TestScan_PricingWindowDropsAncient proves the documented windowing bound: a
// transcript whose mtime is older than the pricing window (max(sinceMonday, 12h))
// is NEVER opened for pricing — only the in-window file is folded — while the
// active block still reflects the recent record. (Ported from the removed
// corpus_pricing_equivalence_test.go windowing test, now Monitor-only.)
func TestScan_PricingWindowDropsAncient(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC) // Wed; window ~60h (this week)
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	home := filepath.Join(root, "claude-home")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Ancient file: mtime + record before this Monday (outside the window).
	ancient := now.Add(-4 * 24 * time.Hour)
	writeTranscript(t, projectDir(t, home, "/w/old"), "old.jsonl", ancient,
		assistantLineTS("m", 9999, 9999, ancient))
	// Recent in-window file.
	writeTranscript(t, projectDir(t, home, "/w/new"), "new.jsonl", now,
		assistantLineTS("m", 100, 50, now.Add(-1*time.Hour)))

	disc := &session.Discoverer{SessionsDir: sessionsDir, PidAlive: func(int) bool { return true }, ReadEnv: stubEnv}
	m, _, _ := newMonitorAllObservers(home, disc, pricingFixture())
	if _, err := m.Scan(now); err != nil {
		t.Fatal(err)
	}
	if got := m.PricingFilesLastScan(); got != 1 {
		t.Errorf("PricingFilesLastScan = %d, want 1 (ancient out-of-window file must NOT be opened)", got)
	}
	block := m.Block(now)
	if block == nil {
		t.Fatalf("Block = nil, want the recent record's active block")
	}
	// Recent-only cost: input 100, output 50 at Default 5/25 per Mtok.
	want := 100.0/1e6*5 + 50.0/1e6*25
	if diff := block.CostUSD - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("Block.CostUSD = %v, want %v (recent only; ancient excluded)", block.CostUSD, want)
	}
}

// TestScan_CostProbeErrOnCorruptTranscript proves a transcript scan failure (an
// oversized line) threads through to Monitor.CostProbed — so the TUI's
// "5h unavailable — cost scan failed" signal survives the fold (SHOULD-FIX 4).
func TestScan_CostProbeErrOnCorruptTranscript(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	home := filepath.Join(root, "claude-home")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := projectDir(t, home, "/tmp/proj")
	// A line exceeding transcript.maxTranscriptLine (16 MiB) makes ScanIncremental
	// return bufio.ErrTooLong — the same failure the old NativePricer surfaced.
	oversized := strings.Repeat("x", 16*1024*1024+10)
	writeTranscript(t, dir, "big.jsonl", now, oversized)

	disc := &session.Discoverer{SessionsDir: sessionsDir, PidAlive: func(int) bool { return true }, ReadEnv: stubEnv}
	m, _, _ := newMonitorAllObservers(home, disc, pricingFixture())
	if _, err := m.Scan(now); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	m.Block(now) // set probed, as the poller does each tick
	if _, err := m.CostProbed(); err == nil {
		t.Fatalf("CostProbed err = nil, want non-nil (corrupt pricing file must surface)")
	}
}

// TestScan_SteadyStatePricingAndStatus: on an unchanged corpus, the second Scan
// re-reads no status file and re-parses no transcript (all cache_hit) — the
// pricing/limits folds add no per-tick re-read.
func TestScan_SteadyStatePricingAndStatus(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	home := filepath.Join(root, "claude-home")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := projectDir(t, home, "/tmp/proj")
	writeSessionRecord(t, sessionsDir, 111, "s1", "/tmp/proj", "")
	writeTranscript(t, dir, "s1.jsonl", now, assistantLineTS("m", 100, 50, now.Add(-1*time.Hour)))
	writeStatus(t, filepath.Join(dir, "s1.status.jsonl"), fmt.Sprintf(`{"ts":%d,"five_hour_pct":42,"five_hour_resets_at":%d}`+"\n", now.Unix(), now.Add(3*time.Hour).Unix()), now)

	disc := &session.Discoverer{SessionsDir: sessionsDir, PidAlive: func(int) bool { return true }, ReadEnv: stubEnv}
	m, _, _ := newMonitorAllObservers(home, disc, pricingFixture())

	if _, err := m.Scan(now); err != nil { // cold
		t.Fatal(err)
	}
	if m.StatusReadsLastScan() == 0 || m.TranscriptScansLastScan() == 0 {
		t.Fatalf("cold scan did no pricing/status work: statusReads=%d scans=%d", m.StatusReadsLastScan(), m.TranscriptScansLastScan())
	}
	if _, err := m.Scan(now.Add(time.Minute)); err != nil { // steady state
		t.Fatal(err)
	}
	if got := m.StatusReadsLastScan(); got != 0 {
		t.Errorf("StatusReadsLastScan = %d, want 0 (unchanged status file reused)", got)
	}
	if got := m.TranscriptScansLastScan(); got != 0 {
		t.Errorf("TranscriptScansLastScan = %d, want 0 (all cache_hit)", got)
	}
}
