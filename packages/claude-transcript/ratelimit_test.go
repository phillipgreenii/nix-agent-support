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

// --- bead pg2-8ef5c: 12-hour conversion, range guards, discriminator ---------
//
// Every event time below is an explicit instant in an explicit location and
// every reset clause names an explicit IANA zone, so none of these assertions
// depends on the machine's local zone or on the current date.

// TestParseLimitResetTextTwelveHourQuadrants pins all four quadrants of the
// 12-hour clock conversion in ONE table, with the resolved ABSOLUTE hour spelled
// out per case. The two conversion lines exist only for the irregular ends of a
// 12-hour clock — 12am is hour 0 and 12pm stays hour 12 — and a regression in
// either is a silent 12-HOUR error in the one number this function produces.
//
// The quadrants were previously asserted only incidentally, spread across
// TestParseLimitResetTextStandard, …DayRollover, …TwelveHour and …HourOnly, each
// of which mixes the conversion with roll-forward arithmetic. Here every case
// shares one event time — 2026-05-04 23:00 UTC — which every quadrant's clock
// time precedes on the event's own day, so all four roll forward by the same 24h
// and the only thing the expected value varies by is the converted hour.
func TestParseLimitResetTextTwelveHourQuadrants(t *testing.T) {
	ev := time.Date(2026, 5, 4, 23, 0, 0, 0, time.UTC)
	cases := []struct {
		in       string
		wantHour int
		wantMin  int
	}{
		{"resets 12am (UTC)", 0, 0},  // 12am is hour 0, NOT hour 12
		{"resets 1am (UTC)", 1, 0},   // a regular am hour passes through unchanged
		{"resets 12pm (UTC)", 12, 0}, // 12pm STAYS hour 12, NOT hour 24
		{"resets 1pm (UTC)", 13, 0},  // a regular pm hour gains 12
		{"resets 12:15am (UTC)", 0, 15},
		{"resets 1:15am (UTC)", 1, 15},
		{"resets 12:15pm (UTC)", 12, 15},
		{"resets 1:15pm (UTC)", 13, 15},
	}
	for _, c := range cases {
		got, ok := parseLimitResetText(c.in, ev)
		if !ok {
			t.Errorf("%q: ok=false, want true", c.in)
			continue
		}
		want := time.Date(2026, 5, 5, c.wantHour, c.wantMin, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Errorf("%q: got %v, want %v", c.in, got.UTC(), want)
		}
	}
}

// TestParseLimitResetTextComponentBoundaries pins the INCLUSIVE accept edges of
// the three range guards (hour 1..12, minute 0..59, day 1..31) and, on the
// reject side, OBSERVES THE RETURNED (zero, false) rather than merely checking
// that nothing panicked.
//
// Every input here is chosen to MATCH limitResetRe, so each one reaches the
// guard it targets instead of falling out at the regex-miss path — the hour and
// day groups are `\d{1,2}` (so 0, 13, 32 all match) and the minute group is
// `\d{2}` (so 60 matches). An out-of-range component that the regex rejected
// first would exercise the `m == nil` path and prove nothing about the guard.
func TestParseLimitResetTextComponentBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		ev     time.Time
		wantOK bool
		want   time.Time
	}{
		// hour: 1..12 inclusive.
		{
			name: "hour 1 accepted", in: "resets 1am (UTC)",
			ev: time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC), wantOK: true,
			want: time.Date(2026, 5, 5, 1, 0, 0, 0, time.UTC),
		},
		{
			name: "hour 12 accepted", in: "resets 12pm (UTC)",
			ev: time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC), wantOK: true,
			want: time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC),
		},
		{
			name: "hour 0 rejected", in: "resets 0:30am (UTC)",
			ev: time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "hour 13 rejected", in: "resets 13:00pm (UTC)",
			ev: time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC),
		},
		// minute: 0..59 inclusive.
		{
			name: "minute 0 accepted", in: "resets 3:00pm (UTC)",
			ev: time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC), wantOK: true,
			want: time.Date(2026, 5, 5, 15, 0, 0, 0, time.UTC),
		},
		{
			name: "minute 59 accepted", in: "resets 3:59pm (UTC)",
			ev: time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC), wantOK: true,
			want: time.Date(2026, 5, 5, 15, 59, 0, 0, time.UTC),
		},
		{
			name: "minute 60 rejected", in: "resets 3:60pm (UTC)",
			ev: time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC),
		},
		// day: 1..31 inclusive (the weekly month+day shape).
		{
			name: "day 1 accepted", in: "resets Apr 1, 9am (UTC)",
			ev: time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC), wantOK: true,
			want: time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		},
		{
			name: "day 31 accepted", in: "resets Mar 31, 9am (UTC)",
			ev: time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC), wantOK: true,
			want: time.Date(2026, 3, 31, 9, 0, 0, 0, time.UTC),
		},
		{
			// Unguarded, time.Date would NORMALIZE day 0 to the last day of the
			// preceding month (Feb 28) — a plausible-looking instant no message
			// stated. The event sits under a day before it, so the normalized
			// value would be in range and accepted but for the guard.
			name: "day 0 rejected", in: "resets Mar 0, 9am (UTC)",
			ev: time.Date(2026, 2, 27, 12, 0, 0, 0, time.UTC),
		},
		{
			// Likewise day 32 would normalize forward to Apr 1.
			name: "day 32 rejected", in: "resets Mar 32, 9am (UTC)",
			ev: time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC),
		},
		{
			// The month-abbreviation lookup's own reject path: the regex accepts
			// any `[A-Z][a-z]{2}`, so an abbreviation outside monthAbbrev reaches
			// the lookup and must be discarded there.
			name: "unknown month abbreviation rejected", in: "resets Xyz 15, 9am (UTC)",
			ev: time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseLimitResetText(c.in, c.ev)
			if ok != c.wantOK {
				t.Fatalf("%q: ok = %v, want %v (got %v)", c.in, ok, c.wantOK, got.UTC())
			}
			if !c.wantOK {
				if !got.IsZero() {
					t.Errorf("%q: got %v, want the zero time on rejection", c.in, got.UTC())
				}
				return
			}
			if !got.Equal(c.want) {
				t.Errorf("%q: got %v, want %v", c.in, got.UTC(), c.want)
			}
		})
	}
}

// TestResetWithinHorizonRejectsZeroReset observes the RETURN VALUE of the
// zero-reset reject path, which no other test reaches: parseLimitResetText never
// hands ResetWithinHorizon a zero candidate, so only a direct call can pin it. A
// zero resetsAt is the unknown sentinel rather than an instant, and without the
// guard it would compare as long before any horizon and be ACCEPTED.
func TestResetWithinHorizonRejectsZeroReset(t *testing.T) {
	ev := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	if ResetWithinHorizon(time.Time{}, ev) {
		t.Error("ResetWithinHorizon(zero, ev) = true, want false (zero is the unknown sentinel, never an instant)")
	}
	// A zero eventTime must not rescue a zero resetsAt either: zero is not
	// "0 past the horizon", it is "no instant at all".
	if ResetWithinHorizon(time.Time{}, time.Time{}) {
		t.Error("ResetWithinHorizon(zero, zero) = true, want false")
	}
	// Positive control, so the assertions above cannot pass vacuously.
	if !ResetWithinHorizon(ev.Add(time.Hour), ev) {
		t.Error("ResetWithinHorizon(ev+1h, ev) = false, want true")
	}
}

// legacyRateEventShaped renders a legacy system/api_error rate-limit line with
// every field the discriminator tests under caller control, so one fixture can
// fail exactly ONE of its conjuncts and leave the rest satisfied.
func legacyRateEventShaped(evType, subtype, errType string, ts time.Time, retryInMs int64) string {
	return `{"type":"` + evType + `","subtype":"` + subtype +
		`","timestamp":"` + ts.UTC().Format(time.RFC3339Nano) +
		`","retryInMs":` + fmt.Sprintf("%d", retryInMs) +
		`,"error":{"status":429,"error":{"type":"error","error":{"type":"` + errType +
		`","message":"limit exceeded"}}}}`
}

// TestRateLimitPauseDiscriminatorRequiresEveryConjunct pins the legacy
// rate-limit discriminator as a CONJUNCTION. Each fixture is a well-formed line
// that unmarshals cleanly into the scan struct and satisfies every conjunct but
// one, so relaxing any single one to a disjunction admits an unrelated event as
// a rate-limit event and gives it a reset time.
func TestRateLimitPauseDiscriminatorRequiresEveryConjunct(t *testing.T) {
	ts := time.Date(2026, 4, 10, 17, 0, 0, 0, time.UTC)
	const inRange = 3600000 // one hour, comfortably inside MaxResetHorizon

	rejected := []struct {
		name string
		line string
	}{
		{
			// Fails only `type == "system"`. "summary" is deliberately neither
			// user nor assistant, so it cannot double as a session resume.
			name: "not a system event",
			line: legacyRateEventShaped("summary", "api_error", "rate_limit_error", ts, inRange),
		},
		{
			// Fails only `subtype == "api_error"`.
			name: "system event of another subtype",
			line: legacyRateEventShaped("system", "info", "rate_limit_error", ts, inRange),
		},
		{
			// Fails only `error.error.error.type == "rate_limit_error"`: a real
			// api_error, but an overload rather than a rate limit.
			name: "api_error that is not a rate limit",
			line: legacyRateEventShaped("system", "api_error", "overloaded_error", ts, inRange),
		},
		{
			// Fails only `retryInMs > 0`.
			name: "zero retryInMs",
			line: legacyRateEventShaped("system", "api_error", "rate_limit_error", ts, 0),
		},
		{
			// Fails only `retryInMs > 0`, on the other side of the boundary.
			name: "negative retryInMs",
			line: legacyRateEventShaped("system", "api_error", "rate_limit_error", ts, -1),
		},
	}
	for _, c := range rejected {
		t.Run(c.name, func(t *testing.T) {
			path := t.TempDir() + "/t.jsonl"
			if err := writeTestFile(path, c.line+"\n"); err != nil {
				t.Fatal(err)
			}
			got, err := RateLimitPause(path)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.IsZero() {
				t.Errorf("resetsAt = %v, want zero — this event fails a discriminator conjunct and must not become a rate-limit event", got.UTC())
			}
		})
	}

	// Positive control: the SAME renderer with every conjunct satisfied does
	// produce a reset. Without it, a fixture bug that made every line
	// unparseable would let all of the above pass for the wrong reason.
	t.Run("every conjunct satisfied", func(t *testing.T) {
		path := t.TempDir() + "/t.jsonl"
		line := legacyRateEventShaped("system", "api_error", "rate_limit_error", ts, inRange)
		if err := writeTestFile(path, line+"\n"); err != nil {
			t.Fatal(err)
		}
		got, err := RateLimitPause(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := ts.Add(inRange * time.Millisecond)
		if !got.Equal(want) {
			t.Errorf("resetsAt = %v, want %v", got.UTC(), want)
		}
	})
}
