package transcript

import (
	"math"
	"testing"
	"time"

	ct "github.com/phillipgreenii/claude-transcript"
)

func TestScanMatchesIndividualFunctions(t *testing.T) {
	path := "../../../tests/fixtures/transcripts/basic.jsonl"

	snap, err := Scan(path)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	fp, _ := FirstPrompt(path)
	if snap.FirstPrompt != fp {
		t.Errorf("FirstPrompt: got %q, want %q", snap.FirstPrompt, fp)
	}

	ctx, _ := LatestContext(path)
	if snap.Model != ctx.Model {
		t.Errorf("Model: got %q, want %q", snap.Model, ctx.Model)
	}
	if snap.ContextTokens != ctx.ContextTokens {
		t.Errorf("ContextTokens: got %d, want %d", snap.ContextTokens, ctx.ContextTokens)
	}
	if snap.TotalTokens != ctx.TotalTokens {
		t.Errorf("TotalTokens: got %d, want %d", snap.TotalTokens, ctx.TotalTokens)
	}

	subs, _ := OpenSubagents(path)
	if snap.SubagentCount != subs {
		t.Errorf("SubagentCount: got %d, want %d", snap.SubagentCount, subs)
	}

	waiting, _ := IsAwaitingInput(path)
	if snap.AwaitingInput != waiting {
		t.Errorf("AwaitingInput: got %v, want %v", snap.AwaitingInput, waiting)
	}

	resetsAt, _ := RateLimitPause(path)
	if !snap.RateLimitResetsAt.Equal(resetsAt) {
		t.Errorf("RateLimitResetsAt: got %v, want %v", snap.RateLimitResetsAt, resetsAt)
	}
}

func TestScanEmptyFile(t *testing.T) {
	path := t.TempDir() + "/empty.jsonl"
	if err := writeTestFile(path, ""); err != nil {
		t.Fatal(err)
	}
	snap, err := Scan(path)
	if err != nil {
		t.Fatalf("Scan empty: %v", err)
	}
	if snap.FirstPrompt != "" || snap.Model != "" || snap.ContextTokens != 0 ||
		snap.TotalTokens != 0 || snap.SubagentCount != 0 || snap.AwaitingInput ||
		!snap.RateLimitResetsAt.IsZero() {
		t.Errorf("empty file should yield zero Snapshot, got %+v", snap)
	}
}

func TestScanRateLimitPause(t *testing.T) {
	ts := time.Date(2026, 4, 10, 17, 0, 0, 0, time.UTC)
	path := t.TempDir() + "/rl.jsonl"
	if err := writeTestFile(path, rateEvent(ts, 3600000)+"\n"); err != nil {
		t.Fatal(err)
	}
	snap, err := Scan(path)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := ts.Add(3600000 * time.Millisecond)
	if !snap.RateLimitResetsAt.Equal(want) {
		t.Errorf("RateLimitResetsAt: got %v, want %v", snap.RateLimitResetsAt, want)
	}
}

func TestScanMissingFile(t *testing.T) {
	_, err := Scan("/nonexistent/path/transcript.jsonl")
	if err != nil {
		t.Errorf("Scan missing file should return nil error, got %v", err)
	}
}

func TestScanRateLimitClearedAfterResume(t *testing.T) {
	ts := time.Date(2026, 4, 10, 17, 0, 0, 0, time.UTC)
	path := t.TempDir() + "/rl.jsonl"
	body := rateEvent(ts, 3600000) + "\n" +
		`{"type":"user","message":{"role":"user","content":"continue"}}` + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	snap, err := Scan(path)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !snap.RateLimitResetsAt.IsZero() {
		t.Errorf("RateLimitResetsAt should be zero after user resumes, got %v", snap.RateLimitResetsAt)
	}
}

func TestScanSyntheticRateLimit(t *testing.T) {
	ts := time.Date(2026, 5, 5, 17, 12, 37, 0, time.UTC)
	path := t.TempDir() + "/synth.jsonl"
	body := `{"type":"assistant","timestamp":"` + ts.UTC().Format(time.RFC3339Nano) +
		`","message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"You've hit your limit · resets 3:30pm (America/New_York)"}]},"error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429}` + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	snap, err := Scan(path)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := time.Date(2026, 5, 5, 19, 30, 0, 0, time.UTC)
	if !snap.RateLimitResetsAt.Equal(want) {
		t.Errorf("RateLimitResetsAt = %v, want %v", snap.RateLimitResetsAt.UTC(), want)
	}
}

func TestScanSyntheticRateLimitClearedByLaterUser(t *testing.T) {
	ts := time.Date(2026, 5, 5, 17, 12, 37, 0, time.UTC)
	path := t.TempDir() + "/synth_cleared.jsonl"
	body := `{"type":"assistant","timestamp":"` + ts.UTC().Format(time.RFC3339Nano) +
		`","message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"You've hit your limit · resets 3:30pm (America/New_York)"}]},"error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429}` + "\n" +
		`{"type":"user","timestamp":"2026-05-05T19:35:00Z","message":{"role":"user","content":"continue"}}` + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	snap, err := Scan(path)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !snap.RateLimitResetsAt.IsZero() {
		t.Errorf("RateLimitResetsAt = %v, want zero (user resumed)", snap.RateLimitResetsAt)
	}
}

func TestSnapshotPopulatesLastErrorForRetryable(t *testing.T) {
	ts := time.Date(2026, 5, 19, 20, 54, 0, 0, time.UTC)
	path := t.TempDir() + "/t.jsonl"
	body := apiErrorEvent(ts, ErrUnknown, "API Error: The socket connection was closed unexpectedly") + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	snap, err := Scan(path)
	if err != nil {
		t.Fatalf("Scan err = %v", err)
	}
	if snap.LastError == nil {
		t.Fatal("LastError = nil, want non-nil")
	}
	if snap.LastError.Kind != ErrUnknown {
		t.Errorf("LastError.Kind = %q, want %q", snap.LastError.Kind, ErrUnknown)
	}
	if !snap.LastError.IsTerminal {
		t.Error("LastError.IsTerminal = false, want true")
	}
	if !snap.LastErrorRetryable {
		t.Error("LastErrorRetryable = false, want true (unknown socket-drop is transient)")
	}
}

func TestSnapshotLastErrorNilWhenNoApiError(t *testing.T) {
	path := t.TempDir() + "/t.jsonl"
	body := `{"type":"user","message":{"role":"user","content":"hi"}}` + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	snap, err := Scan(path)
	if err != nil {
		t.Fatalf("Scan err = %v", err)
	}
	if snap.LastError != nil {
		t.Errorf("LastError = %+v, want nil", snap.LastError)
	}
}

// TestScanSurfacesStreamIdleTimeout mirrors TestLastAPIErrorDetectsStreamIdleTimeout
// at the Snapshot level: Scan must populate LastError for the unknown-kind
// stream-idle-timeout event so it reaches the poller/TUI. (bead pg2-lpxq)
func TestScanSurfacesStreamIdleTimeout(t *testing.T) {
	ts := time.Date(2026, 6, 12, 14, 0, 0, 0, time.UTC)
	const text = "API Error: Stream idle timeout - partial response received"
	path := t.TempDir() + "/t.jsonl"
	if err := writeTestFile(path, apiErrorEvent(ts, ErrUnknown, text)+"\n"); err != nil {
		t.Fatal(err)
	}
	snap, err := Scan(path)
	if err != nil {
		t.Fatalf("Scan err = %v, want nil", err)
	}
	if snap.LastError == nil {
		t.Fatal("LastError = nil, want populated stream-idle-timeout error")
	}
	if snap.LastError.Kind != ErrUnknown || !snap.LastErrorRetryable || !snap.LastError.IsTerminal {
		t.Errorf("LastError = %+v retryable=%v, want unknown/retryable/terminal", snap.LastError, snap.LastErrorRetryable)
	}
}

// --- retryInMs upper bound at ingestion (bead pg2-yzs6a) ---------------------

// TestScanRejectsOutOfHorizonRetryInMs proves the legacy NUMERIC path is bounded
// in this scanner too, not only in claude-transcript's RateLimitPause: an
// out-of-horizon delay yields no reset at all rather than a fabricated far-future
// window.
func TestScanRejectsOutOfHorizonRetryInMs(t *testing.T) {
	ts := time.Date(2026, 4, 10, 17, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		retry int64
	}{
		{"one ms beyond the horizon", ct.MaxRetryInMs + 1},
		{"epoch-sized garbage (seconds/ms confusion)", 1782958200000},
		{"max int64 (would overflow time.Duration to a PAST instant)", math.MaxInt64},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := t.TempDir() + "/rl.jsonl"
			if err := writeTestFile(path, rateEvent(ts, c.retry)+"\n"); err != nil {
				t.Fatal(err)
			}
			snap, err := Scan(path)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if !snap.RateLimitResetsAt.IsZero() {
				t.Errorf("RateLimitResetsAt = %v, want zero (retryInMs beyond ct.MaxResetHorizon, discarded not clamped)",
					snap.RateLimitResetsAt)
			}
			// The independent oracle must agree — the two implementations of this
			// path are only safe while they bound it identically.
			if rl, _ := RateLimitPause(path); !snap.RateLimitResetsAt.Equal(rl) {
				t.Errorf("RateLimitResetsAt = %v, but RateLimitPause = %v (the two paths must agree)",
					snap.RateLimitResetsAt, rl)
			}
		})
	}
}

// TestScanOutOfHorizonRetryInMsDoesNotSupersedeGoodReset proves a discarded event
// does not blank a known window: the earlier in-range reset survives, matching
// RateLimitPause.
func TestScanOutOfHorizonRetryInMsDoesNotSupersedeGoodReset(t *testing.T) {
	good := time.Date(2026, 4, 10, 17, 0, 0, 0, time.UTC)
	path := t.TempDir() + "/rl.jsonl"
	body := rateEvent(good, 3600000) + "\n" + rateEvent(good.Add(time.Minute), math.MaxInt64) + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	snap, err := Scan(path)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := good.Add(time.Hour)
	if !snap.RateLimitResetsAt.Equal(want) {
		t.Errorf("RateLimitResetsAt = %v, want %v (earlier in-range reset must survive)", snap.RateLimitResetsAt, want)
	}
	if rl, _ := RateLimitPause(path); !snap.RateLimitResetsAt.Equal(rl) {
		t.Errorf("RateLimitResetsAt = %v, but RateLimitPause = %v (the two paths must agree)", snap.RateLimitResetsAt, rl)
	}
}

// TestScanRejectsOutOfHorizonProseReset proves the PROSE path is bounded through
// this scanner as well: the year-less weekly clause resolving to a far-future
// instant leaves RateLimitResetsAt unknown, which is the state the nudger's
// limit-pause producer already handles.
func TestScanRejectsOutOfHorizonProseReset(t *testing.T) {
	ts := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	path := t.TempDir() + "/synth.jsonl"
	// "Feb 3" already passed in 2026, so the parser rolls it a whole YEAR forward.
	body := `{"type":"assistant","timestamp":"` + ts.UTC().Format(time.RFC3339Nano) +
		`","message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"You've hit your limit · resets Feb 3, 9am (UTC)"}]},"error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429}` + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	snap, err := Scan(path)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !snap.RateLimitResetsAt.IsZero() {
		t.Errorf("RateLimitResetsAt = %v, want zero (prose reset beyond ct.MaxResetHorizon)", snap.RateLimitResetsAt)
	}
}
