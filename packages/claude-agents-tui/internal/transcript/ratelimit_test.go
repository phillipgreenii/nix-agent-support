package transcript

import (
	"fmt"
	"testing"
	"time"
)

// rateEvent returns a JSONL line for a rate_limit_error api_error event.
func rateEvent(ts time.Time, retryInMs int64) string {
	return `{"type":"system","subtype":"api_error","timestamp":"` + ts.UTC().Format(time.RFC3339Nano) +
		`","retryInMs":` + fmt.Sprintf("%d", retryInMs) +
		`,"error":{"status":429,"error":{"type":"error","error":{"type":"rate_limit_error","message":"limit exceeded"}}}}`
}

func TestRateLimitPauseDetectsError(t *testing.T) {
	ts := time.Date(2026, 4, 10, 17, 0, 0, 0, time.UTC)
	path := t.TempDir() + "/t.jsonl"
	if err := writeTestFile(path, rateEvent(ts, 3600000)+"\n"); err != nil {
		t.Fatal(err)
	}
	got, ok := RateLimitPause(path)
	if !ok {
		t.Fatal("RateLimitPause ok=false, want true")
	}
	want := ts.Add(3600000 * time.Millisecond)
	if !got.Equal(want) {
		t.Errorf("resetsAt = %v, want %v", got, want)
	}
}

func TestRateLimitPauseFalseAfterUserResumes(t *testing.T) {
	ts := time.Date(2026, 4, 10, 17, 0, 0, 0, time.UTC)
	path := t.TempDir() + "/t.jsonl"
	body := rateEvent(ts, 3600000) + "\n" +
		`{"type":"user","message":{"role":"user","content":"continue"}}` + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	_, ok := RateLimitPause(path)
	if ok {
		t.Error("RateLimitPause ok=true, want false (user resumed)")
	}
}

func TestRateLimitPauseFalseNoEvent(t *testing.T) {
	path := t.TempDir() + "/t.jsonl"
	body := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}` + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	_, ok := RateLimitPause(path)
	if ok {
		t.Error("RateLimitPause ok=true, want false (no api_error)")
	}
}

func TestRateLimitPauseFalseZeroRetry(t *testing.T) {
	ts := time.Date(2026, 4, 10, 17, 0, 0, 0, time.UTC)
	path := t.TempDir() + "/t.jsonl"
	if err := writeTestFile(path, rateEvent(ts, 0)+"\n"); err != nil {
		t.Fatal(err)
	}
	_, ok := RateLimitPause(path)
	if ok {
		t.Error("RateLimitPause ok=true, want false (retryInMs=0)")
	}
}

func TestRateLimitPauseFalseAssistantResumes(t *testing.T) {
	ts := time.Date(2026, 4, 10, 17, 0, 0, 0, time.UTC)
	path := t.TempDir() + "/t.jsonl"
	body := rateEvent(ts, 3600000) + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"resuming"}]}}` + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	_, ok := RateLimitPause(path)
	if ok {
		t.Error("RateLimitPause ok=true, want false (assistant event follows error)")
	}
}
