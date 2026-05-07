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
	got, err := RateLimitPause(path)
	if err != nil {
		t.Fatalf("RateLimitPause err = %v, want nil", err)
	}
	if got.IsZero() {
		t.Fatal("RateLimitPause returned zero time, want renewal time")
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
	got, err := RateLimitPause(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.IsZero() {
		t.Error("RateLimitPause returned non-zero time, want zero (user resumed)")
	}
}

func TestRateLimitPauseFalseNoEvent(t *testing.T) {
	path := t.TempDir() + "/t.jsonl"
	body := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}` + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	got, err := RateLimitPause(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.IsZero() {
		t.Error("RateLimitPause returned non-zero time, want zero (no api_error)")
	}
}

func TestRateLimitPauseFalseZeroRetry(t *testing.T) {
	ts := time.Date(2026, 4, 10, 17, 0, 0, 0, time.UTC)
	path := t.TempDir() + "/t.jsonl"
	if err := writeTestFile(path, rateEvent(ts, 0)+"\n"); err != nil {
		t.Fatal(err)
	}
	got, err := RateLimitPause(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.IsZero() {
		t.Error("RateLimitPause returned non-zero time, want zero (retryInMs=0)")
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
	got, err := RateLimitPause(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.IsZero() {
		t.Error("RateLimitPause returned non-zero time, want zero (assistant event follows error)")
	}
}

func TestParseLimitResetTextStandard(t *testing.T) {
	// Event at 2026-05-05 17:12:37 UTC → 13:12:37 EDT.
	// "3:30pm (America/New_York)" → 19:30 UTC same day.
	ev := time.Date(2026, 5, 5, 17, 12, 37, 0, time.UTC)
	got, ok := parseLimitResetText("You've hit your limit · resets 3:30pm (America/New_York)", ev)
	if !ok {
		t.Fatal("ok=false, want true")
	}
	want := time.Date(2026, 5, 5, 19, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got.UTC(), want)
	}
}

func TestParseLimitResetTextDayRollover(t *testing.T) {
	// Event at 2026-05-05 23:00:00 UTC → 19:00 EDT.
	// "1:00am (America/New_York)" parsed for 2026-05-05 = 05:00 UTC same day, which
	// is BEFORE the event time. Expect rollover to 2026-05-06 05:00 UTC.
	ev := time.Date(2026, 5, 5, 23, 0, 0, 0, time.UTC)
	got, ok := parseLimitResetText("You've hit your limit · resets 1:00am (America/New_York)", ev)
	if !ok {
		t.Fatal("ok=false, want true")
	}
	want := time.Date(2026, 5, 6, 5, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got.UTC(), want)
	}
}

func TestParseLimitResetTextTwelveHour(t *testing.T) {
	ev := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Time
	}{
		{"resets 12:00am (UTC)", time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)},
		{"resets 12:30am (UTC)", time.Date(2026, 5, 5, 0, 30, 0, 0, time.UTC)},
		{"resets 12:00pm (UTC)", time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)},
		{"resets 12:30pm (UTC)", time.Date(2026, 5, 5, 12, 30, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		got, ok := parseLimitResetText(c.in, ev)
		if !ok {
			t.Errorf("%q: ok=false, want true", c.in)
			continue
		}
		if !got.Equal(c.want) {
			t.Errorf("%q: got %v, want %v", c.in, got.UTC(), c.want)
		}
	}
}

func TestParseLimitResetTextRejectsUnknownText(t *testing.T) {
	ev := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	cases := []string{
		"unrelated text",
		"resets soon",
		"resets 3:30pm",                       // no TZ
		"resets 25:00am (UTC)",                // bad hour
		"resets 3:60pm (UTC)",                 // bad minute
		"resets 3:30pm (Not/A_Real_Zone_Foo)", // bad TZ
	}
	for _, c := range cases {
		if _, ok := parseLimitResetText(c, ev); ok {
			t.Errorf("%q: ok=true, want false", c)
		}
	}
}
