package freshness

import (
	"testing"
	"time"
)

func TestBoundSeconds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		interval int
		want     int
	}{
		{"declared 60s daemon cadence", 60, 120},
		{"declared 10m cadence", 600, 1200},
		{"undeclared (zero) falls back to the default cadence", 0, DefaultSyncIntervalSeconds * BoundIntervals},
		{"negative is treated as undeclared", -5, DefaultSyncIntervalSeconds * BoundIntervals},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := BoundSeconds(tc.interval); got != tc.want {
				t.Errorf("BoundSeconds(%d) = %d, want %d", tc.interval, got, tc.want)
			}
		})
	}
	// A zero bound would flag every read stale; the fallback must prevent it.
	if BoundSeconds(0) <= 0 {
		t.Errorf("BoundSeconds(0) must be positive, got %d", BoundSeconds(0))
	}
}

func TestIsStale(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		asOf  time.Time
		bound int
		want  bool
	}{
		{"just synced", now, 120, false},
		{"inside the bound", now.Add(-119 * time.Second), 120, false},
		{"exactly at the bound is NOT yet past it", now.Add(-120 * time.Second), 120, false},
		{"one second past the bound", now.Add(-121 * time.Second), 120, true},
		{"far past the bound", now.Add(-99 * time.Hour), 120, true},
		{"unknown as-of is stale by definition", time.Time{}, 120, true},
		{"future as-of (clock skew) is fresh, not stale", now.Add(30 * time.Second), 120, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsStale(tc.asOf, now, tc.bound); got != tc.want {
				t.Errorf("IsStale(%v, %v, %d) = %v, want %v", tc.asOf, now, tc.bound, got, tc.want)
			}
		})
	}
}

func TestAgeSeconds(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		asOf time.Time
		want int
	}{
		{"90s old", now.Add(-90 * time.Second), 90},
		{"sub-second truncates to 0", now.Add(-900 * time.Millisecond), 0},
		{"future as-of floors at 0", now.Add(30 * time.Second), 0},
		{"unknown as-of reports 0 (age unknown)", time.Time{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := AgeSeconds(tc.asOf, now); got != tc.want {
				t.Errorf("AgeSeconds(%v, %v) = %d, want %d", tc.asOf, now, got, tc.want)
			}
		})
	}
}

// TestParseAsOf pins the store's on-disk timestamp format (RFC3339 UTC) and the
// fail-closed mapping: anything unusable becomes the ZERO time, which IsStale
// reports stale.
func TestParseAsOf(t *testing.T) {
	want := time.Date(2026, 7, 29, 11, 58, 0, 0, time.UTC)
	if got := ParseAsOf("2026-07-29T11:58:00Z"); !got.Equal(want) {
		t.Errorf("ParseAsOf(RFC3339) = %v, want %v", got, want)
	}
	// A non-UTC offset is normalized to UTC but keeps the same instant.
	if got := ParseAsOf("2026-07-29T06:58:00-05:00"); !got.Equal(want) {
		t.Errorf("ParseAsOf(offset form) = %v, want the same instant %v", got, want)
	}
	for _, bad := range []string{"", "not-a-time", "2026-07-29", "1700000000"} {
		got := ParseAsOf(bad)
		if !got.IsZero() {
			t.Errorf("ParseAsOf(%q) = %v, want the zero time", bad, got)
		}
		if !IsStale(got, time.Now().UTC(), 120) {
			t.Errorf("ParseAsOf(%q) must feed a STALE verdict (fail closed)", bad)
		}
	}
}
