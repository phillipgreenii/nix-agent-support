package claudetranscript

import (
	"fmt"
	"math"
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
	// Event 2026-04-07 12:00 UTC. "Apr 13, 11am (America/New_York)" →
	// 2026-04-13 11:00 EDT = 2026-04-13 15:00 UTC. The event sits ~6d3h before
	// the reset: inside MaxResetHorizon, as a real weekly limit always is.
	ev := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)
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
	ev := time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC)
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
//
// Each case gets its OWN event time, the day before its target date, so the
// resolved reset stays inside MaxResetHorizon. A single shared January event (the
// pre-bound shape of this test) put most months hundreds of days out — instants
// the horizon now correctly rejects, and which no real weekly limit produces.
func TestParseLimitResetTextDatePrefixedAllMonths(t *testing.T) {
	months := []struct {
		abbr string
		mon  time.Month
	}{
		{"Jan", time.January},
		{"Feb", time.February},
		{"Mar", time.March},
		{"Apr", time.April},
		{"May", time.May},
		{"Jun", time.June},
		{"Jul", time.July},
		{"Aug", time.August},
		{"Sep", time.September},
		{"Oct", time.October},
		{"Nov", time.November},
		{"Dec", time.December},
	}
	for _, c := range months {
		in := "resets " + c.abbr + " 15, 9am (UTC)"
		ev := time.Date(2026, c.mon, 14, 0, 0, 0, 0, time.UTC)
		got, ok := parseLimitResetText(in, ev)
		if !ok {
			t.Errorf("%q: ok=false, want true", in)
			continue
		}
		want := time.Date(2026, c.mon, 15, 9, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Errorf("%q: got %v, want %v", in, got.UTC(), want)
		}
	}
}

// --- MaxResetHorizon: upper bound at ingestion (bead pg2-yzs6a) ---------------

// TestParseLimitResetTextRejectsBeyondHorizon proves the prose path DISCARDS a
// resolved instant beyond MaxResetHorizon instead of returning it. Both
// garbage-HIGH shapes the year-less weekly clause can produce are covered: a
// far-future month+day in the event's own year, and the +1-YEAR rollover applied
// to a date that already passed.
func TestParseLimitResetTextRejectsBeyondHorizon(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ev   time.Time
	}{
		{
			name: "far-future date in the event year",
			in:   "You've hit your limit · resets Aug 15, 11am (UTC)",
			ev:   time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC), // ~45 days out
		},
		{
			name: "year rollover on an already-passed date",
			in:   "resets Feb 3, 9am (UTC)",
			ev:   time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC), // rolls to 2027 → ~247 days out
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseLimitResetText(c.in, c.ev)
			if ok {
				t.Errorf("ok=true (got %v), want false — beyond MaxResetHorizon must be discarded", got.UTC())
			}
			if !got.IsZero() {
				t.Errorf("got %v, want zero time (discarded, never clamped to the horizon)", got.UTC())
			}
		})
	}
}

// TestParseLimitResetTextHorizonBoundary pins the accept/reject edge: exactly
// MaxResetHorizon past the event is ACCEPTED (a real weekly limit reported the
// instant the window opened), one minute beyond it is rejected.
func TestParseLimitResetTextHorizonBoundary(t *testing.T) {
	reset := time.Date(2026, 4, 13, 11, 0, 0, 0, time.UTC)

	atEdge := reset.Add(-MaxResetHorizon)
	got, ok := parseLimitResetText("resets Apr 13, 11am (UTC)", atEdge)
	if !ok {
		t.Fatalf("ok=false at exactly MaxResetHorizon (%v), want true", MaxResetHorizon)
	}
	if !got.Equal(reset) {
		t.Errorf("got %v, want %v", got.UTC(), reset)
	}

	pastEdge := atEdge.Add(-time.Minute)
	if got, ok := parseLimitResetText("resets Apr 13, 11am (UTC)", pastEdge); ok {
		t.Errorf("ok=true (got %v) one minute beyond MaxResetHorizon, want false", got.UTC())
	}
}

// TestRetryInMsResetsAt covers the legacy numeric path's bound, including the
// overflow trap: a huge retryInMs must NOT be converted to a time.Duration first
// (that wraps negative and reads as a plausible PAST instant).
func TestRetryInMsResetsAt(t *testing.T) {
	ev := time.Date(2026, 4, 10, 17, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		retry   int64
		wantOK  bool
		wantAt  time.Time
		explain string
	}{
		{name: "one minute", retry: 60000, wantOK: true, wantAt: ev.Add(time.Minute)},
		{name: "exactly the horizon", retry: MaxRetryInMs, wantOK: true, wantAt: ev.Add(MaxResetHorizon)},
		{name: "one ms beyond the horizon", retry: MaxRetryInMs + 1, wantOK: false},
		{name: "zero", retry: 0, wantOK: false},
		{name: "negative", retry: -1, wantOK: false},
		// Would overflow time.Duration (ns) and wrap NEGATIVE if converted first.
		{name: "max int64 (duration overflow)", retry: math.MaxInt64, wantOK: false},
		// Seconds-vs-milliseconds confusion upstream: an epoch-sized number.
		{name: "epoch-sized garbage", retry: 1782958200000, wantOK: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := RetryInMsResetsAt(ev, c.retry)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v (got %v)", ok, c.wantOK, got.UTC())
			}
			if !c.wantOK {
				if !got.IsZero() {
					t.Errorf("got %v, want zero time on rejection", got.UTC())
				}
				return
			}
			if !got.Equal(c.wantAt) {
				t.Errorf("got %v, want %v", got.UTC(), c.wantAt)
			}
		})
	}
}

// TestRateLimitPauseRejectsOutOfHorizonRetryInMs proves the bound reaches
// RateLimitPause: a lone out-of-horizon legacy event yields no reset at all.
func TestRateLimitPauseRejectsOutOfHorizonRetryInMs(t *testing.T) {
	ts := time.Date(2026, 4, 10, 17, 0, 0, 0, time.UTC)
	path := t.TempDir() + "/t.jsonl"
	if err := writeTestFile(path, rateEvent(ts, MaxRetryInMs+1)+"\n"); err != nil {
		t.Fatal(err)
	}
	got, err := RateLimitPause(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("resetsAt = %v, want zero (retryInMs beyond MaxResetHorizon)", got)
	}
}

// TestRateLimitPauseOutOfHorizonDoesNotSupersedeGoodReset proves the discarded
// event does not become "the last rate-limit event": an earlier in-range reset
// survives a later garbage one, so a malformed line cannot blank a known window.
func TestRateLimitPauseOutOfHorizonDoesNotSupersedeGoodReset(t *testing.T) {
	good := time.Date(2026, 4, 10, 17, 0, 0, 0, time.UTC)
	garbage := good.Add(time.Minute)
	path := t.TempDir() + "/t.jsonl"
	body := rateEvent(good, 3600000) + "\n" + rateEvent(garbage, math.MaxInt64) + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	got, err := RateLimitPause(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := good.Add(time.Hour)
	if !got.Equal(want) {
		t.Errorf("resetsAt = %v, want %v (earlier in-range reset must survive)", got, want)
	}
}
