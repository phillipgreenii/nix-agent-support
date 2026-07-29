package limits

import (
	"math"
	"os"
	"testing"
	"time"
)

func i64(v int64) *int64    { return &v }
func f(v float64) *float64  { return &v }
func at(ts int64) time.Time { return time.Unix(ts, 0) }

// wantPct asserts a *float64 equals the expected value (or both nil).
func wantPct(t *testing.T, label string, got *float64, want *float64) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("%s = %v, want nil (unknown)", label, *got)
	case want != nil && got == nil:
		t.Errorf("%s = nil, want %v", label, *want)
	case want != nil && got != nil && *got != *want:
		t.Errorf("%s = %v, want %v", label, *got, *want)
	}
}

// TestCurrentHoldsWindowPeakOnRegression: one fixed window, pct oscillates
// 100->47->100->50 with strictly increasing ts. The reader returns the PEAK
// (100), not the newest-ts 50; CapturedAt is the newest ts (reading liveness).
func TestCurrentHoldsWindowPeakOnRegression(t *testing.T) {
	recs := []Record{
		{TS: i64(100), FiveHourPct: f(100), FiveHourResetsAt: i64(1000)},
		{TS: i64(200), FiveHourPct: f(47), FiveHourResetsAt: i64(1000)},
		{TS: i64(300), FiveHourPct: f(100), FiveHourResetsAt: i64(1000)},
		{TS: i64(400), FiveHourPct: f(50), FiveHourResetsAt: i64(1000)},
	}
	l := Current(recs)
	wantPct(t, "FiveHourPct", l.FiveHourPct, f(100))
	if !l.FiveHourResetsAt.Equal(at(1000)) {
		t.Errorf("FiveHourResetsAt = %v, want %v", l.FiveHourResetsAt, at(1000))
	}
	if !l.CapturedAt.Equal(at(400)) {
		t.Errorf("CapturedAt = %v, want %v (newest ts)", l.CapturedAt, at(400))
	}
}

// TestCurrentPeakAcrossFilesSameWindow: the newest-ts record (40%) is not the
// peak; a record from another session/file (90%) in the same window wins.
func TestCurrentPeakAcrossFilesSameWindow(t *testing.T) {
	recs := []Record{
		{TS: i64(500), FiveHourPct: f(40), FiveHourResetsAt: i64(1000)},
		{TS: i64(200), FiveHourPct: f(90), FiveHourResetsAt: i64(1000)},
	}
	l := Current(recs)
	wantPct(t, "FiveHourPct", l.FiveHourPct, f(90))
	if !l.CapturedAt.Equal(at(500)) {
		t.Errorf("CapturedAt = %v, want %v", l.CapturedAt, at(500))
	}
}

// TestCurrentLaggingOldWindowRecordDoesNotMaskNewWindow: a lagging record with a
// NEWER ts but the OLD window's reset must not mask the new (greater-reset) window.
func TestCurrentLaggingOldWindowRecordDoesNotMaskNewWindow(t *testing.T) {
	recs := []Record{
		{TS: i64(600), FiveHourPct: f(4), FiveHourResetsAt: i64(2000)},  // new window
		{TS: i64(700), FiveHourPct: f(50), FiveHourResetsAt: i64(1000)}, // lagging straggler, newer ts, old window
	}
	l := Current(recs)
	wantPct(t, "FiveHourPct", l.FiveHourPct, f(4))
	if !l.FiveHourResetsAt.Equal(at(2000)) {
		t.Errorf("FiveHourResetsAt = %v, want %v (greatest reset)", l.FiveHourResetsAt, at(2000))
	}
	if !l.CapturedAt.Equal(at(700)) {
		t.Errorf("CapturedAt = %v, want %v", l.CapturedAt, at(700))
	}
}

// TestCurrentNewWindowReleasesPeak: a later reset releases the prior window's peak.
func TestCurrentNewWindowReleasesPeak(t *testing.T) {
	recs := []Record{
		{TS: i64(100), FiveHourPct: f(100), FiveHourResetsAt: i64(1000)},
		{TS: i64(200), FiveHourPct: f(30), FiveHourResetsAt: i64(2000)},
	}
	l := Current(recs)
	wantPct(t, "FiveHourPct", l.FiveHourPct, f(30))
	if !l.FiveHourResetsAt.Equal(at(2000)) {
		t.Errorf("FiveHourResetsAt = %v, want %v", l.FiveHourResetsAt, at(2000))
	}
}

// TestCurrentWindowAllNilPctStaysUnknown: a window with a reset but no pct returns
// nil pct (never a fabricated 0), reset still surfaced.
func TestCurrentWindowAllNilPctStaysUnknown(t *testing.T) {
	recs := []Record{{TS: i64(100), FiveHourResetsAt: i64(1000)}}
	l := Current(recs)
	wantPct(t, "FiveHourPct", l.FiveHourPct, nil)
	if !l.FiveHourResetsAt.Equal(at(1000)) {
		t.Errorf("FiveHourResetsAt = %v, want %v", l.FiveHourResetsAt, at(1000))
	}
}

// TestCurrentNewestMissingResetsKeepsWindowPeak: the globally-newest record omits
// a reset; the window is defined by the newest resets-bearing record, peak held.
func TestCurrentNewestMissingResetsKeepsWindowPeak(t *testing.T) {
	recs := []Record{
		{TS: i64(100), FiveHourPct: f(100), FiveHourResetsAt: i64(1000)},
		{TS: i64(200)}, // newest ts, no pct, no reset
	}
	l := Current(recs)
	wantPct(t, "FiveHourPct", l.FiveHourPct, f(100))
	if !l.FiveHourResetsAt.Equal(at(1000)) {
		t.Errorf("FiveHourResetsAt = %v, want %v", l.FiveHourResetsAt, at(1000))
	}
	if !l.CapturedAt.Equal(at(200)) {
		t.Errorf("CapturedAt = %v, want %v", l.CapturedAt, at(200))
	}
}

// TestCurrentSevenDayWindowPeak: peak-hold applies symmetrically to the 7d window.
func TestCurrentSevenDayWindowPeak(t *testing.T) {
	recs := []Record{
		{TS: i64(100), SevenDayPct: f(40), SevenDayResetsAt: i64(5000)},
		{TS: i64(200), SevenDayPct: f(80), SevenDayResetsAt: i64(5000)},
	}
	l := Current(recs)
	wantPct(t, "SevenDayPct", l.SevenDayPct, f(80))
	if !l.SevenDayResetsAt.Equal(at(5000)) {
		t.Errorf("SevenDayResetsAt = %v, want %v", l.SevenDayResetsAt, at(5000))
	}
}

// TestCurrentNoWindowFallbackNewestByTS: no record carries a reset -> fall back to
// the newest-by-ts value (20), NOT the max (90); reset stays zero.
func TestCurrentNoWindowFallbackNewestByTS(t *testing.T) {
	recs := []Record{
		{TS: i64(100), FiveHourPct: f(90)},
		{TS: i64(200), FiveHourPct: f(20)},
	}
	l := Current(recs)
	wantPct(t, "FiveHourPct", l.FiveHourPct, f(20))
	if !l.FiveHourResetsAt.IsZero() {
		t.Errorf("FiveHourResetsAt = %v, want zero (no window)", l.FiveHourResetsAt)
	}
}

// TestCurrentAbsentFieldsStayUnknown: a missing 7d window stays nil/zero, never 0/1970.
func TestCurrentAbsentFieldsStayUnknown(t *testing.T) {
	recs := []Record{{TS: i64(100), FiveHourPct: f(50), FiveHourResetsAt: i64(1000)}}
	l := Current(recs)
	wantPct(t, "SevenDayPct", l.SevenDayPct, nil)
	if !l.SevenDayResetsAt.IsZero() {
		t.Errorf("SevenDayResetsAt = %v, want zero", l.SevenDayResetsAt)
	}
}

// TestCurrentRealZeroDistinctFromUnknown: a written 0% is a present reading.
func TestCurrentRealZeroDistinctFromUnknown(t *testing.T) {
	recs := []Record{{TS: i64(100), FiveHourPct: f(0), FiveHourResetsAt: i64(1000)}}
	l := Current(recs)
	if l.FiveHourPct == nil || *l.FiveHourPct != 0 {
		t.Errorf("FiveHourPct = %v, want a real 0", l.FiveHourPct)
	}
}

// TestCurrentEmptyReturnsNil: no records -> nil (matches the old (nil,nil) guard).
func TestCurrentEmptyReturnsNil(t *testing.T) {
	if l := Current(nil); l != nil {
		t.Errorf("Current(nil) = %+v, want nil", l)
	}
}

// TestReadStatusRecordsSkipsTslessAndUnparseable proves the file parser drops
// ts-less and malformed lines and keeps the good ones.
func TestReadStatusRecordsSkipsTslessAndUnparseable(t *testing.T) {
	path := t.TempDir() + "/s.status.jsonl"
	body := `{"five_hour_pct":50,"five_hour_resets_at":1000}
not json
{"ts":1234,"five_hour_pct":80,"five_hour_resets_at":1000}

`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	recs := ReadStatusRecords(path)
	if len(recs) != 1 {
		t.Fatalf("ReadStatusRecords len = %d, want 1 (ts-less + malformed + blank dropped)", len(recs))
	}
	if recs[0].TS == nil || *recs[0].TS != 1234 {
		t.Errorf("kept record ts = %v, want 1234", recs[0].TS)
	}
}

// --- reset-epoch upper bound at ingestion (bead pg2-yzs6a) -------------------

// TestCurrentDiscardsFiveHourResetBeyondWindow is the criterion-1 test: a
// far-future five_hour_resets_at is DISCARDED, not elected. Without the bound the
// greatest-reset rule makes the garbage epoch the current window and drags its
// percentage along with it, which is what poisons the nudger's once-per-window
// latch.
func TestCurrentDiscardsFiveHourResetBeyondWindow(t *testing.T) {
	const ts = int64(1_700_000_000)
	legit := ts + 3600                 // 1h out — inside the 5h window
	garbage := ts + int64(365*24*3600) // a year out — upstream garbage
	recs := []Record{
		{TS: i64(ts), FiveHourPct: f(80), FiveHourResetsAt: i64(legit)},
		{TS: i64(ts), FiveHourPct: f(99), FiveHourResetsAt: i64(garbage)},
	}
	l := Current(recs)
	if l == nil {
		t.Fatal("Current = nil, want a reading")
	}
	if !l.FiveHourResetsAt.Equal(at(legit)) {
		t.Errorf("FiveHourResetsAt = %v, want %v (garbage epoch must be discarded, not latched)",
			l.FiveHourResetsAt, at(legit))
	}
	// The rejected record's pct must not leak into the legitimate window either.
	wantPct(t, "FiveHourPct", l.FiveHourPct, f(80))
}

// TestCurrentDiscardsOnlyRecordWithOutOfRangeReset proves rejection degrades to
// "window unknown" rather than clamping to the horizon: with no in-range epoch at
// all there is no window, and the pct falls back to the no-window path.
func TestCurrentDiscardsOnlyRecordWithOutOfRangeReset(t *testing.T) {
	const ts = int64(1_700_000_000)
	recs := []Record{{TS: i64(ts), FiveHourPct: f(42), FiveHourResetsAt: i64(ts + int64(365*24*3600))}}
	l := Current(recs)
	if l == nil {
		t.Fatal("Current = nil, want a reading")
	}
	if !l.FiveHourResetsAt.IsZero() {
		t.Errorf("FiveHourResetsAt = %v, want zero (unknown — never clamped to the horizon)", l.FiveHourResetsAt)
	}
	wantPct(t, "FiveHourPct", l.FiveHourPct, f(42))
}

// TestBoundedResetHorizonEdges pins the accept/reject edge per window, plus the
// two shapes that cannot be validated or would break the comparison.
func TestBoundedResetHorizonEdges(t *testing.T) {
	const ts = int64(1_700_000_000)
	fiveSecs := int64(fiveHourWindow / time.Second)
	sevenSecs := int64(sevenDayWindow / time.Second)

	cases := []struct {
		name   string
		rec    Record
		window time.Duration
		want   *int64
	}{
		{
			name:   "5h: exactly one window out is accepted",
			rec:    Record{TS: i64(ts), FiveHourResetsAt: i64(ts + fiveSecs)},
			window: fiveHourWindow,
			want:   i64(ts + fiveSecs),
		},
		{
			name:   "5h: one second beyond is rejected",
			rec:    Record{TS: i64(ts), FiveHourResetsAt: i64(ts + fiveSecs + 1)},
			window: fiveHourWindow,
			want:   nil,
		},
		{
			name:   "5h: a PAST reset is kept (stragglers are legitimate; no lower bound)",
			rec:    Record{TS: i64(ts), FiveHourResetsAt: i64(ts - fiveSecs)},
			window: fiveHourWindow,
			want:   i64(ts - fiveSecs),
		},
		{
			name:   "7d: six days out is accepted",
			rec:    Record{TS: i64(ts), FiveHourResetsAt: i64(ts + 6*24*3600)},
			window: sevenDayWindow,
			want:   i64(ts + 6*24*3600),
		},
		{
			name:   "7d: eight days out is rejected",
			rec:    Record{TS: i64(ts), FiveHourResetsAt: i64(ts + 8*24*3600)},
			window: sevenDayWindow,
			want:   nil,
		},
		{
			name:   "7d: exactly one window out is accepted",
			rec:    Record{TS: i64(ts), FiveHourResetsAt: i64(ts + sevenSecs)},
			window: sevenDayWindow,
			want:   i64(ts + sevenSecs),
		},
		{
			name:   "no ts: nothing to validate against, so rejected",
			rec:    Record{FiveHourResetsAt: i64(ts + 60)},
			window: fiveHourWindow,
			want:   nil,
		},
		{
			name:   "no reset: absent stays absent",
			rec:    Record{TS: i64(ts)},
			window: fiveHourWindow,
			want:   nil,
		},
		{
			// The comparison must not overflow into ACCEPTANCE. ts near MaxInt64
			// wraps the ts+horizon sum negative, which must reject.
			name:   "overflowing ts fails safe (rejected)",
			rec:    Record{TS: i64(math.MaxInt64 - 10), FiveHourResetsAt: i64(math.MaxInt64)},
			window: fiveHourWindow,
			want:   nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := boundedReset(c.rec, func(r Record) *int64 { return r.FiveHourResetsAt }, c.window)
			switch {
			case c.want == nil && got != nil:
				t.Errorf("boundedReset = %d, want nil (rejected)", *got)
			case c.want != nil && got == nil:
				t.Errorf("boundedReset = nil, want %d", *c.want)
			case c.want != nil && *got != *c.want:
				t.Errorf("boundedReset = %d, want %d", *got, *c.want)
			}
		})
	}
}

// TestCurrentDiscardsSevenDayResetBeyondWindow covers the 7d window's own bound,
// so the two windows are not silently sharing the looser one.
func TestCurrentDiscardsSevenDayResetBeyondWindow(t *testing.T) {
	const ts = int64(1_700_000_000)
	legit := ts + 6*24*3600
	garbage := ts + 30*24*3600
	recs := []Record{
		{TS: i64(ts), SevenDayPct: f(60), SevenDayResetsAt: i64(legit)},
		{TS: i64(ts), SevenDayPct: f(95), SevenDayResetsAt: i64(garbage)},
	}
	l := Current(recs)
	if l == nil {
		t.Fatal("Current = nil, want a reading")
	}
	if !l.SevenDayResetsAt.Equal(at(legit)) {
		t.Errorf("SevenDayResetsAt = %v, want %v (garbage epoch discarded)", l.SevenDayResetsAt, at(legit))
	}
	wantPct(t, "SevenDayPct", l.SevenDayPct, f(60))
}
