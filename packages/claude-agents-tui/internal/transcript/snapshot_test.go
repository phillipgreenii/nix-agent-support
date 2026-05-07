package transcript

import (
	"testing"
	"time"
)

func TestScanMatchesIndividualFunctions(t *testing.T) {
	path := "../../tests/fixtures/transcripts/basic.jsonl"

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
