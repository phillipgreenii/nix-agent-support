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

// syntheticRateLimitEvent returns a JSONL line for the new rate-limit shape.
func syntheticRateLimitEvent(ts time.Time, text string) string {
	return `{"type":"assistant","timestamp":"` + ts.UTC().Format(time.RFC3339Nano) +
		`","message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"` +
		text + `"}]},"error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429}`
}

func TestRateLimitPauseDetectsSyntheticAssistant(t *testing.T) {
	ts := time.Date(2026, 5, 5, 17, 12, 37, 0, time.UTC) // 13:12 EDT
	path := t.TempDir() + "/t.jsonl"
	body := syntheticRateLimitEvent(ts, "You've hit your limit · resets 3:30pm (America/New_York)") + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	got, err := RateLimitPause(path)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := time.Date(2026, 5, 5, 19, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got.UTC(), want)
	}
}

func TestRateLimitPauseSyntheticClearedByLaterUser(t *testing.T) {
	ts := time.Date(2026, 5, 5, 17, 12, 37, 0, time.UTC)
	path := t.TempDir() + "/t.jsonl"
	body := syntheticRateLimitEvent(ts, "You've hit your limit · resets 3:30pm (America/New_York)") + "\n" +
		`{"type":"user","timestamp":"2026-05-05T19:35:00Z","message":{"role":"user","content":"continue"}}` + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	got, err := RateLimitPause(path)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !got.IsZero() {
		t.Errorf("got %v, want zero (user resumed after rate-limit)", got)
	}
}

func TestRateLimitPauseSyntheticIgnoredWhenTextUnparseable(t *testing.T) {
	ts := time.Date(2026, 5, 5, 17, 12, 37, 0, time.UTC)
	path := t.TempDir() + "/t.jsonl"
	body := syntheticRateLimitEvent(ts, "You've hit your limit · come back tomorrow") + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	got, err := RateLimitPause(path)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !got.IsZero() {
		t.Errorf("got %v, want zero (text not parseable)", got)
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

// TestParseLimitResetTextHourOnly covers the bare-hour variation observed in
// real transcripts ("resets 1pm (TZ)") which lacks a :MM component.
func TestParseLimitResetTextHourOnly(t *testing.T) {
	// Event 2026-05-05 17:12 UTC = 13:12 EDT.
	ev := time.Date(2026, 5, 5, 17, 12, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Time
	}{
		// 1pm EDT = 17:00 UTC. 13:12 EDT precedes 13:00 EDT only when ev > 13:00 EDT,
		// here ev=13:12 EDT is AFTER 13:00 EDT, so candidate rolls to next day 17:00 UTC.
		{"resets 1pm (America/New_York)", time.Date(2026, 5, 6, 17, 0, 0, 0, time.UTC)},
		// 2pm EDT = 18:00 UTC same day (13:12 EDT < 14:00 EDT).
		{"resets 2pm (America/New_York)", time.Date(2026, 5, 5, 18, 0, 0, 0, time.UTC)},
		// 12am EDT = 04:00 UTC next day.
		{"resets 12am (America/New_York)", time.Date(2026, 5, 6, 4, 0, 0, 0, time.UTC)},
		// 12pm EDT = 16:00 UTC. ev (13:12 EDT) < 12:00 EDT? No — ev is 13:12 EDT,
		// which is AFTER 12:00 EDT, so rolls to next day 16:00 UTC.
		{"resets 12pm (America/New_York)", time.Date(2026, 5, 6, 16, 0, 0, 0, time.UTC)},
		// 10am UTC, ev=17:12 UTC > 10:00 UTC → next day.
		{"resets 10am (UTC)", time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC)},
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

// TestParseLimitResetTextDatePrefixed covers the weekly-limit variation observed
// in real transcripts: "resets Apr 13, 11am (TZ)".
func TestParseLimitResetTextDatePrefixed(t *testing.T) {
	// Event 2026-04-06 12:00 UTC. "Apr 13, 11am (America/New_York)" →
	// 2026-04-13 11:00 EDT = 2026-04-13 15:00 UTC.
	ev := time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC)
	got, ok := parseLimitResetText("You've hit your limit · resets Apr 13, 11am (America/New_York)", ev)
	if !ok {
		t.Fatal("ok=false, want true")
	}
	want := time.Date(2026, 4, 13, 15, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got.UTC(), want)
	}
}

// TestParseLimitResetTextDatePrefixedWithMinutes asserts the same prefix form
// works when the time has minutes ("Apr 13, 11:30am").
func TestParseLimitResetTextDatePrefixedWithMinutes(t *testing.T) {
	ev := time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC)
	got, ok := parseLimitResetText("resets Apr 13, 11:30am (America/New_York)", ev)
	if !ok {
		t.Fatal("ok=false, want true")
	}
	want := time.Date(2026, 4, 13, 15, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got.UTC(), want)
	}
}

// TestParseLimitResetTextDatePrefixedYearRollover covers the case where the
// month+day in the message has already passed in the event's year and must be
// resolved as the same date in the *next* year.
func TestParseLimitResetTextDatePrefixedYearRollover(t *testing.T) {
	// Event 2026-12-30 12:00 UTC, reset "Jan 5, 11am (UTC)" → must be 2027-01-05.
	ev := time.Date(2026, 12, 30, 12, 0, 0, 0, time.UTC)
	got, ok := parseLimitResetText("resets Jan 5, 11am (UTC)", ev)
	if !ok {
		t.Fatal("ok=false, want true")
	}
	want := time.Date(2027, 1, 5, 11, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got.UTC(), want)
	}
}

// TestParseLimitResetTextDatePrefixedAllMonths verifies every month abbreviation
// observed/expected resolves correctly.
func TestParseLimitResetTextDatePrefixedAllMonths(t *testing.T) {
	ev := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		in       string
		wantYear int
		wantMon  time.Month
	}{
		{"resets Jan 15, 9am (UTC)", 2026, time.January},
		{"resets Feb 1, 9am (UTC)", 2026, time.February},
		{"resets Mar 1, 9am (UTC)", 2026, time.March},
		{"resets Apr 1, 9am (UTC)", 2026, time.April},
		{"resets May 1, 9am (UTC)", 2026, time.May},
		{"resets Jun 1, 9am (UTC)", 2026, time.June},
		{"resets Jul 1, 9am (UTC)", 2026, time.July},
		{"resets Aug 1, 9am (UTC)", 2026, time.August},
		{"resets Sep 1, 9am (UTC)", 2026, time.September},
		{"resets Oct 1, 9am (UTC)", 2026, time.October},
		{"resets Nov 1, 9am (UTC)", 2026, time.November},
		{"resets Dec 1, 9am (UTC)", 2026, time.December},
	}
	for _, c := range cases {
		got, ok := parseLimitResetText(c.in, ev)
		if !ok {
			t.Errorf("%q: ok=false, want true", c.in)
			continue
		}
		if got.Year() != c.wantYear || got.Month() != c.wantMon {
			t.Errorf("%q: got %v-%v, want %d-%v", c.in, got.Year(), got.Month(), c.wantYear, c.wantMon)
		}
	}
}
