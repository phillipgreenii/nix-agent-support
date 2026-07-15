package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeStatusFile writes a <name> under claudeHome/projects/<slug>/ with the given
// JSONL lines. Creates the project dir if needed.
func writeStatusFile(t *testing.T, claudeHome, slug, name string, lines ...string) {
	t.Helper()
	dir := filepath.Join(claudeHome, "projects", slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSiblingLimitsSource_NewestAcrossFilesByTS is the account-global reader contract
// (ADR 0021 §1): read across ALL *.status.jsonl files ignoring session_id, and let the
// newest record by ts define the current window and CapturedAt. Under ADR 0029 the
// reported percentage is that window's peak; here the newest record (sess-b) sits in
// its OWN window (a distinct five_hour_resets_at), so the window peak equals sess-b's
// own value — the assertions below coincide with the pre-0029 "newest record" result.
// The peak-vs-newest distinction is exercised by the same-window tests further down.
func TestSiblingLimitsSource_NewestAcrossFilesByTS(t *testing.T) {
	home := t.TempDir()
	// Two different sessions (different session_id, different project dirs) — the
	// reader must ignore session_id.
	writeStatusFile(
		t, home, "-proj-a", "sess-a.status.jsonl",
		`{"ts":1700000000,"session_id":"sess-a","five_hour_pct":10,"five_hour_resets_at":1782958200}`,
		`{"ts":1700000100,"session_id":"sess-a","five_hour_pct":20,"five_hour_resets_at":1782958200}`,
	)
	writeStatusFile(
		t, home, "-proj-b", "sess-b.status.jsonl",
		`{"ts":1700000200,"session_id":"sess-b","five_hour_pct":34,"five_hour_resets_at":1782958999,"seven_day_pct":5,"seven_day_resets_at":1783000000}`,
	)

	src := &SiblingLimitsSource{ClaudeHome: home}
	got, err := src.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got == nil {
		t.Fatal("Current returned nil, want the newest record")
	}
	// Newest ts is 1700000200 (sess-b) in its own window; sess-a's records belong to a
	// different (earlier) window and are not part of sess-b's window peak.
	if got.FiveHourPct == nil || *got.FiveHourPct != 34 {
		t.Errorf("FiveHourPct = %v, want 34 (peak of the newest record's window)", got.FiveHourPct)
	}
	if got.SevenDayPct == nil || *got.SevenDayPct != 5 {
		t.Errorf("SevenDayPct = %v, want 5", got.SevenDayPct)
	}
	if want := time.Unix(1700000200, 0); !got.CapturedAt.Equal(want) {
		t.Errorf("CapturedAt = %v, want %v (from ts)", got.CapturedAt, want)
	}
	if want := time.Unix(1782958999, 0); !got.FiveHourResetsAt.Equal(want) {
		t.Errorf("FiveHourResetsAt = %v, want %v", got.FiveHourResetsAt, want)
	}
	if want := time.Unix(1783000000, 0); !got.SevenDayResetsAt.Equal(want) {
		t.Errorf("SevenDayResetsAt = %v, want %v", got.SevenDayResetsAt, want)
	}
}

// TestSiblingLimitsSource_AbsentFieldsStayUnknown proves a missing window / value
// reads back as nil / zero, never 0 or 1970 (per-field optionality, ADR 0021 §1).
func TestSiblingLimitsSource_AbsentFieldsStayUnknown(t *testing.T) {
	home := t.TempDir()
	// Only five_hour present (the Phase 0 seven_day-absent case).
	writeStatusFile(
		t, home, "-proj", "s.status.jsonl",
		`{"ts":1700000000,"session_id":"s","five_hour_pct":34,"five_hour_resets_at":1782958200}`,
	)
	src := &SiblingLimitsSource{ClaudeHome: home}
	got, err := src.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got.FiveHourPct == nil || *got.FiveHourPct != 34 {
		t.Errorf("FiveHourPct = %v, want 34", got.FiveHourPct)
	}
	if got.SevenDayPct != nil {
		t.Errorf("SevenDayPct = %v, want nil (absent, not 0)", *got.SevenDayPct)
	}
	if !got.SevenDayResetsAt.IsZero() {
		t.Errorf("SevenDayResetsAt = %v, want zero (absent, never 1970)", got.SevenDayResetsAt)
	}
}

// TestSiblingLimitsSource_RealZeroDistinctFromUnknown pins that a written 0% is a
// real reading, not "unknown".
func TestSiblingLimitsSource_RealZeroDistinctFromUnknown(t *testing.T) {
	home := t.TempDir()
	writeStatusFile(
		t, home, "-proj", "s.status.jsonl",
		`{"ts":1700000000,"session_id":"s","seven_day_pct":0}`,
	)
	src := &SiblingLimitsSource{ClaudeHome: home}
	got, err := src.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got.SevenDayPct == nil {
		t.Fatal("SevenDayPct = nil, want present 0 (real reading)")
	}
	if *got.SevenDayPct != 0 {
		t.Errorf("SevenDayPct = %v, want 0", *got.SevenDayPct)
	}
}

// TestSiblingLimitsSource_EqualTSTiebreak: when two records share the newest ts,
// the reader must return one of them deterministically (last-writer / lexical) —
// not error and not merge. Here both carry the same ts but different values; the
// result must be one whole record (not a mix).
func TestSiblingLimitsSource_EqualTSTiebreak(t *testing.T) {
	home := t.TempDir()
	writeStatusFile(
		t, home, "-proj-a", "a.status.jsonl",
		`{"ts":1700000000,"session_id":"a","five_hour_pct":10}`,
	)
	writeStatusFile(
		t, home, "-proj-b", "b.status.jsonl",
		`{"ts":1700000000,"session_id":"b","five_hour_pct":20}`,
	)
	src := &SiblingLimitsSource{ClaudeHome: home}
	got, err := src.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got == nil || got.FiveHourPct == nil {
		t.Fatal("want a record with FiveHourPct set")
	}
	if *got.FiveHourPct != 10 && *got.FiveHourPct != 20 {
		t.Errorf("FiveHourPct = %v, want a whole record (10 or 20), not a merge", *got.FiveHourPct)
	}
}

// TestSiblingLimitsSource_EmptyDir returns (nil, nil) when there are no status
// files (or the projects dir is missing) — "no reading yet", not an error.
func TestSiblingLimitsSource_EmptyDir(t *testing.T) {
	home := t.TempDir()
	src := &SiblingLimitsSource{ClaudeHome: home}
	got, err := src.Current(context.Background())
	if err != nil {
		t.Fatalf("Current on empty: %v", err)
	}
	if got != nil {
		t.Errorf("Current = %v, want nil (no reading yet)", got)
	}
}

// TestSiblingLimitsSource_IgnoresTranscripts confirms the reader only reads
// *.status.jsonl, never the transcript .jsonl next to it.
func TestSiblingLimitsSource_IgnoresTranscripts(t *testing.T) {
	home := t.TempDir()
	// A transcript that happens to contain a "ts" — must be ignored.
	writeStatusFile(
		t, home, "-proj", "s.jsonl",
		`{"ts":9999999999,"type":"user"}`,
	)
	writeStatusFile(
		t, home, "-proj", "s.status.jsonl",
		`{"ts":1700000000,"session_id":"s","five_hour_pct":34}`,
	)
	src := &SiblingLimitsSource{ClaudeHome: home}
	got, err := src.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got == nil || got.FiveHourPct == nil || *got.FiveHourPct != 34 {
		t.Fatalf("want the status record (34), got %+v", got)
	}
	if want := time.Unix(1700000000, 0); !got.CapturedAt.Equal(want) {
		t.Errorf("CapturedAt = %v, want %v (never the transcript ts)", got.CapturedAt, want)
	}
}

// TestSiblingLimitsSource_ImplementsPort asserts the adapter satisfies the port.
func TestSiblingLimitsSource_ImplementsPort(t *testing.T) {
	var _ LimitsSource = (*SiblingLimitsSource)(nil)
}

// TestSiblingLimitsSource_HoldsWindowPeakOnRegression is the pg2-itdwk regression
// (ADR 0029): Claude's five_hour used_percentage is non-monotonic near the cap —
// within ONE window (constant five_hour_resets_at) it oscillated 100→47→100→50 with
// strictly increasing ts. The reader MUST report the window PEAK (100), not the
// literal newest record (50). CapturedAt stays the newest ts (stream liveness).
func TestSiblingLimitsSource_HoldsWindowPeakOnRegression(t *testing.T) {
	home := t.TempDir()
	const reset = 1784135400 // one fixed 5h window
	writeStatusFile(
		t, home, "-proj", "s.status.jsonl",
		`{"ts":1784122281,"session_id":"s","five_hour_pct":100,"five_hour_resets_at":1784135400}`,
		`{"ts":1784122635,"session_id":"s","five_hour_pct":47,"five_hour_resets_at":1784135400}`,
		`{"ts":1784122637,"session_id":"s","five_hour_pct":100,"five_hour_resets_at":1784135400}`,
		`{"ts":1784122786,"session_id":"s","five_hour_pct":50,"five_hour_resets_at":1784135400}`,
	)
	src := &SiblingLimitsSource{ClaudeHome: home}
	got, err := src.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got == nil || got.FiveHourPct == nil {
		t.Fatal("want a record with FiveHourPct set")
	}
	if *got.FiveHourPct != 100 {
		t.Errorf("FiveHourPct = %v, want 100 (window peak, not the newest 50%% dip)", *got.FiveHourPct)
	}
	if want := time.Unix(1784122786, 0); !got.CapturedAt.Equal(want) {
		t.Errorf("CapturedAt = %v, want %v (newest ts = stream liveness)", got.CapturedAt, want)
	}
	if want := time.Unix(reset, 0); !got.FiveHourResetsAt.Equal(want) {
		t.Errorf("FiveHourResetsAt = %v, want %v", got.FiveHourResetsAt, want)
	}
}

// TestSiblingLimitsSource_NewWindowReleasesPeak proves the peak is scoped to the
// CURRENT window (ADR 0029): a genuinely new window (a record with a later
// five_hour_resets_at) releases the prior window's peak. The old window's 100 MUST
// NOT leak into the new window's reading.
func TestSiblingLimitsSource_NewWindowReleasesPeak(t *testing.T) {
	home := t.TempDir()
	writeStatusFile(
		t, home, "-proj", "s.status.jsonl",
		`{"ts":1784122786,"session_id":"s","five_hour_pct":100,"five_hour_resets_at":1784135400}`,
		`{"ts":1784136624,"session_id":"s","five_hour_pct":30,"five_hour_resets_at":1784154600}`,
	)
	src := &SiblingLimitsSource{ClaudeHome: home}
	got, err := src.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got == nil || got.FiveHourPct == nil {
		t.Fatal("want a record with FiveHourPct set")
	}
	if *got.FiveHourPct != 30 {
		t.Errorf("FiveHourPct = %v, want 30 (new window releases the old 100 peak)", *got.FiveHourPct)
	}
	if want := time.Unix(1784154600, 0); !got.FiveHourResetsAt.Equal(want) {
		t.Errorf("FiveHourResetsAt = %v, want %v (the new window)", got.FiveHourResetsAt, want)
	}
	if want := time.Unix(1784136624, 0); !got.CapturedAt.Equal(want) {
		t.Errorf("CapturedAt = %v, want %v", got.CapturedAt, want)
	}
}

// TestSiblingLimitsSource_PeakAcrossFilesSameWindow proves the window peak is
// ACCOUNT-GLOBAL across sessions/files (ADR 0021 §1 unchanged): the peak may live
// in a different file than the newest record, and must still win.
func TestSiblingLimitsSource_PeakAcrossFilesSameWindow(t *testing.T) {
	home := t.TempDir()
	// Newest record (ts 1784122900) is in proj-a at 40%; proj-b holds the 90% peak
	// for the same window at an earlier ts.
	writeStatusFile(
		t, home, "-proj-a", "a.status.jsonl",
		`{"ts":1784122900,"session_id":"a","five_hour_pct":40,"five_hour_resets_at":1784135400}`,
	)
	writeStatusFile(
		t, home, "-proj-b", "b.status.jsonl",
		`{"ts":1784122100,"session_id":"b","five_hour_pct":90,"five_hour_resets_at":1784135400}`,
	)
	src := &SiblingLimitsSource{ClaudeHome: home}
	got, err := src.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got == nil || got.FiveHourPct == nil {
		t.Fatal("want a record with FiveHourPct set")
	}
	if *got.FiveHourPct != 90 {
		t.Errorf("FiveHourPct = %v, want 90 (account-global window peak across files)", *got.FiveHourPct)
	}
	if want := time.Unix(1784122900, 0); !got.CapturedAt.Equal(want) {
		t.Errorf("CapturedAt = %v, want %v (newest ts across files)", got.CapturedAt, want)
	}
}

// TestSiblingLimitsSource_WindowAllNilPctStaysUnknown pins the empty-peak edge (ADR
// 0029): a window whose records carry a resets_at but NO five_hour_pct reads back as
// nil (unknown), never a fabricated 0.
func TestSiblingLimitsSource_WindowAllNilPctStaysUnknown(t *testing.T) {
	home := t.TempDir()
	writeStatusFile(
		t, home, "-proj", "s.status.jsonl",
		`{"ts":1784122786,"session_id":"s","five_hour_resets_at":1784135400}`,
	)
	src := &SiblingLimitsSource{ClaudeHome: home}
	got, err := src.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got == nil {
		t.Fatal("want a record (resets_at present)")
	}
	if got.FiveHourPct != nil {
		t.Errorf("FiveHourPct = %v, want nil (no pct in window — never 0)", *got.FiveHourPct)
	}
	if want := time.Unix(1784135400, 0); !got.FiveHourResetsAt.Equal(want) {
		t.Errorf("FiveHourResetsAt = %v, want %v", got.FiveHourResetsAt, want)
	}
}

// TestSiblingLimitsSource_NewestMissingResetsKeepsWindowPeak pins the edge the
// critique flagged (ADR 0029): if the globally-newest record omits
// five_hour_resets_at, the current window is defined by the newest RESETS-BEARING
// record — so the window peak is preserved rather than collapsing to the
// resets-less newest record's value. CapturedAt still tracks the newest ts.
func TestSiblingLimitsSource_NewestMissingResetsKeepsWindowPeak(t *testing.T) {
	home := t.TempDir()
	writeStatusFile(
		t, home, "-proj", "s.status.jsonl",
		`{"ts":1784122281,"session_id":"s","five_hour_pct":100,"five_hour_resets_at":1784135400}`,
		`{"ts":1784122786,"session_id":"s","five_hour_pct":50}`,
	)
	src := &SiblingLimitsSource{ClaudeHome: home}
	got, err := src.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got == nil || got.FiveHourPct == nil {
		t.Fatal("want a record with FiveHourPct set")
	}
	if *got.FiveHourPct != 100 {
		t.Errorf("FiveHourPct = %v, want 100 (window peak held; newest record lacks resets_at)", *got.FiveHourPct)
	}
	if want := time.Unix(1784135400, 0); !got.FiveHourResetsAt.Equal(want) {
		t.Errorf("FiveHourResetsAt = %v, want %v (newest resets-bearing record's window)", got.FiveHourResetsAt, want)
	}
	if want := time.Unix(1784122786, 0); !got.CapturedAt.Equal(want) {
		t.Errorf("CapturedAt = %v, want %v (globally newest ts)", got.CapturedAt, want)
	}
}

// TestSiblingLimitsSource_LaggingOldWindowRecordDoesNotMaskNewWindow pins the
// max-resets_at window selection (ADR 0029). A straggler session reports the OLD
// window's resets_at together with its stale percentage, and can render (fresh ts)
// AFTER a new window has begun. The current window MUST be the greatest resets_at
// (the new window), not the newest-ts record (the straggler) — otherwise the stale
// old-window value would mask the new window.
func TestSiblingLimitsSource_LaggingOldWindowRecordDoesNotMaskNewWindow(t *testing.T) {
	home := t.TempDir()
	writeStatusFile(
		t, home, "-proj-new", "new.status.jsonl",
		`{"ts":1784136624,"session_id":"new","five_hour_pct":4,"five_hour_resets_at":1784154600}`,
	)
	writeStatusFile(
		t, home, "-proj-lag", "lag.status.jsonl",
		// Newer ts, but the OLD window's resets_at and a stale 50% snapshot.
		`{"ts":1784136700,"session_id":"lag","five_hour_pct":50,"five_hour_resets_at":1784135400}`,
	)
	src := &SiblingLimitsSource{ClaudeHome: home}
	got, err := src.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got == nil || got.FiveHourPct == nil {
		t.Fatal("want a record with FiveHourPct set")
	}
	if *got.FiveHourPct != 4 {
		t.Errorf("FiveHourPct = %v, want 4 (new window; the lagging old-window 50%% must not win)", *got.FiveHourPct)
	}
	if want := time.Unix(1784154600, 0); !got.FiveHourResetsAt.Equal(want) {
		t.Errorf("FiveHourResetsAt = %v, want %v (greatest resets_at)", got.FiveHourResetsAt, want)
	}
	if want := time.Unix(1784136700, 0); !got.CapturedAt.Equal(want) {
		t.Errorf("CapturedAt = %v, want %v (newest ts across all records)", got.CapturedAt, want)
	}
}

// TestSiblingLimitsSource_SevenDayWindowPeak proves the peak-hold applies
// symmetrically to the 7d window keyed by seven_day_resets_at (ADR 0029).
func TestSiblingLimitsSource_SevenDayWindowPeak(t *testing.T) {
	home := t.TempDir()
	writeStatusFile(
		t, home, "-proj", "s.status.jsonl",
		`{"ts":1784122281,"session_id":"s","seven_day_pct":80,"seven_day_resets_at":1784600000}`,
		`{"ts":1784122786,"session_id":"s","seven_day_pct":40,"seven_day_resets_at":1784600000}`,
	)
	src := &SiblingLimitsSource{ClaudeHome: home}
	got, err := src.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got == nil || got.SevenDayPct == nil {
		t.Fatal("want a record with SevenDayPct set")
	}
	if *got.SevenDayPct != 80 {
		t.Errorf("SevenDayPct = %v, want 80 (7d window peak)", *got.SevenDayPct)
	}
	if want := time.Unix(1784600000, 0); !got.SevenDayResetsAt.Equal(want) {
		t.Errorf("SevenDayResetsAt = %v, want %v", got.SevenDayResetsAt, want)
	}
}

// TestSiblingLimitsSource_NoWindowFallbackNewestByTS covers the degenerate no-window
// fallback (ADR 0029): when NO record carries a resets_at there is no window to key on,
// so the pct falls back to the globally-newest record by ts — deliberately NOT the max,
// since without a resets_at unrelated readings must not be pooled into a "window peak".
func TestSiblingLimitsSource_NoWindowFallbackNewestByTS(t *testing.T) {
	home := t.TempDir()
	writeStatusFile(
		t, home, "-proj-a", "a.status.jsonl",
		`{"ts":1700000000,"session_id":"a","five_hour_pct":90}`,
	)
	writeStatusFile(
		t, home, "-proj-b", "b.status.jsonl",
		`{"ts":1700000100,"session_id":"b","five_hour_pct":20}`,
	)
	src := &SiblingLimitsSource{ClaudeHome: home}
	got, err := src.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got == nil || got.FiveHourPct == nil {
		t.Fatal("want a record with FiveHourPct set")
	}
	if *got.FiveHourPct != 20 {
		t.Errorf("FiveHourPct = %v, want 20 (no window: newest-by-ts, NOT the max 90)", *got.FiveHourPct)
	}
	if !got.FiveHourResetsAt.IsZero() {
		t.Errorf("FiveHourResetsAt = %v, want zero (no window)", got.FiveHourResetsAt)
	}
	if want := time.Unix(1700000100, 0); !got.CapturedAt.Equal(want) {
		t.Errorf("CapturedAt = %v, want %v (newest ts)", got.CapturedAt, want)
	}
}
