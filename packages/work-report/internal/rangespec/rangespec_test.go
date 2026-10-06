package rangespec

import (
	"strings"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("zone %s unavailable: %v", name, err)
	}
	return loc
}

func at(loc *time.Location, y int, m time.Month, d, h, mi int) time.Time {
	return time.Date(y, m, d, h, mi, 0, 0, loc)
}

func TestResolveForms(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	// Wednesday 2026-10-07 15:30 local (EDT, UTC-4).
	now := at(ny, 2026, time.October, 7, 15, 30)

	tests := []struct {
		name  string
		spec  string
		since time.Time
		bef   time.Time
		open  bool
	}{
		{"empty is today", "", at(ny, 2026, 10, 7, 0, 0), at(ny, 2026, 10, 8, 0, 0), false},
		{"today", "today", at(ny, 2026, 10, 7, 0, 0), at(ny, 2026, 10, 8, 0, 0), false},
		{"yesterday", "yesterday", at(ny, 2026, 10, 6, 0, 0), at(ny, 2026, 10, 7, 0, 0), false},
		{"single date", "2026-09-01", at(ny, 2026, 9, 1, 0, 0), at(ny, 2026, 9, 2, 0, 0), false},
		{"date range inclusive", "2026-09-01..2026-09-03", at(ny, 2026, 9, 1, 0, 0), at(ny, 2026, 9, 4, 0, 0), false},
		{"date range same day", "2026-09-30..2026-09-30", at(ny, 2026, 9, 30, 0, 0), at(ny, 2026, 10, 1, 0, 0), false},
		{"date range across month end", "2026-09-29..2026-10-01", at(ny, 2026, 9, 29, 0, 0), at(ny, 2026, 10, 2, 0, 0), false},
		{"last-3d calendar", "last-3d", at(ny, 2026, 10, 4, 15, 30), now, false},
		{"last-36h elapsed", "last-36h", now.Add(-36 * time.Hour), now, false},
		{"week to now", "week", at(ny, 2026, 10, 5, 0, 0), now, false},
		{"last-week", "last-week", at(ny, 2026, 9, 28, 0, 0), at(ny, 2026, 10, 5, 0, 0), false},
		{"all", "all", time.Time{}, now, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(tc.spec, now, ny)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.spec, err)
			}
			if !got.Since.Equal(tc.since) || !got.Before.Equal(tc.bef) || got.OpenStart != tc.open {
				t.Errorf("Resolve(%q) = {Since:%v Before:%v Open:%v}, want {Since:%v Before:%v Open:%v}",
					tc.spec, got.Since, got.Before, got.OpenStart, tc.since, tc.bef, tc.open)
			}
			if got.OpenStart != got.Since.IsZero() {
				t.Errorf("Since zero iff OpenStart violated: %+v", got)
			}
			if got.Before.IsZero() {
				t.Errorf("Before must always be set: %+v", got)
			}
		})
	}
}

func TestResolveWeekBoundaries(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	monday := at(ny, 2026, time.October, 5, 0, 0)
	cases := []struct {
		name string
		now  time.Time
	}{
		{"monday midnight", monday},
		{"sunday late", at(ny, 2026, time.October, 11, 23, 59)},
		{"wednesday", at(ny, 2026, time.October, 7, 12, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve("week", tc.now, ny)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Since.Equal(monday) || !got.Before.Equal(tc.now) {
				t.Errorf("week = [%v, %v), want [%v, %v)", got.Since, got.Before, monday, tc.now)
			}
		})
	}
}

func TestResolveUsesZoneNotUTC(t *testing.T) {
	// 2026-10-07 02:30 UTC is still 2026-10-06 in New York and already
	// 2026-10-07 in Tokyo; "today" must follow the configured zone.
	instant := time.Date(2026, 10, 7, 2, 30, 0, 0, time.UTC)
	ny := mustLoc(t, "America/New_York")
	tokyo := mustLoc(t, "Asia/Tokyo")

	gotNY, _ := Resolve("today", instant, ny)
	if want := at(ny, 2026, 10, 6, 0, 0); !gotNY.Since.Equal(want) {
		t.Errorf("NY today Since = %v, want %v", gotNY.Since, want)
	}
	gotTK, _ := Resolve("today", instant, tokyo)
	if want := at(tokyo, 2026, 10, 7, 0, 0); !gotTK.Since.Equal(want) {
		t.Errorf("Tokyo today Since = %v, want %v", gotTK.Since, want)
	}
}

func TestResolveDST(t *testing.T) {
	ny := mustLoc(t, "America/New_York")

	// Spring forward: 2026-03-08 is a 23-hour local day (EST -> EDT at 02:00).
	// Fall back: 2026-11-01 is a 25-hour local day (EDT -> EST at 02:00).
	t.Run("23-hour day via date", func(t *testing.T) {
		got, err := Resolve("2026-03-08", at(ny, 2026, 3, 20, 12, 0), ny)
		if err != nil {
			t.Fatal(err)
		}
		wantSince := time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC)  // midnight EST
		wantBefore := time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC) // midnight EDT
		if !got.Since.Equal(wantSince) || !got.Before.Equal(wantBefore) {
			t.Errorf("got [%v, %v), want [%v, %v)", got.Since, got.Before, wantSince, wantBefore)
		}
		if d := got.Before.Sub(got.Since); d != 23*time.Hour {
			t.Errorf("day length = %v, want 23h", d)
		}
	})
	t.Run("25-hour day via date", func(t *testing.T) {
		got, err := Resolve("2026-11-01", at(ny, 2026, 11, 20, 12, 0), ny)
		if err != nil {
			t.Fatal(err)
		}
		wantSince := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)  // midnight EDT
		wantBefore := time.Date(2026, 11, 2, 5, 0, 0, 0, time.UTC) // midnight EST
		if !got.Since.Equal(wantSince) || !got.Before.Equal(wantBefore) {
			t.Errorf("got [%v, %v), want [%v, %v)", got.Since, got.Before, wantSince, wantBefore)
		}
		if d := got.Before.Sub(got.Since); d != 25*time.Hour {
			t.Errorf("day length = %v, want 25h", d)
		}
	})
	t.Run("today on the 23-hour day", func(t *testing.T) {
		got, _ := Resolve("today", at(ny, 2026, 3, 8, 18, 0), ny)
		if d := got.Before.Sub(got.Since); d != 23*time.Hour {
			t.Errorf("today length = %v, want 23h", d)
		}
	})
	t.Run("yesterday on the 25-hour day", func(t *testing.T) {
		// now is Monday 2026-11-02; yesterday is the 25-hour Sunday.
		got, _ := Resolve("yesterday", at(ny, 2026, 11, 2, 9, 0), ny)
		wantSince := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
		if !got.Since.Equal(wantSince) {
			t.Errorf("yesterday Since = %v, want %v", got.Since, wantSince)
		}
		if d := got.Before.Sub(got.Since); d != 25*time.Hour {
			t.Errorf("yesterday length = %v, want 25h", d)
		}
	})
	t.Run("date range across spring forward", func(t *testing.T) {
		got, _ := Resolve("2026-03-07..2026-03-08", at(ny, 2026, 3, 20, 12, 0), ny)
		wantSince := time.Date(2026, 3, 7, 5, 0, 0, 0, time.UTC)
		wantBefore := time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC)
		if !got.Since.Equal(wantSince) || !got.Before.Equal(wantBefore) {
			t.Errorf("got [%v, %v), want [%v, %v)", got.Since, got.Before, wantSince, wantBefore)
		}
	})
	t.Run("last-week across spring forward", func(t *testing.T) {
		// now Wed 2026-03-11; this week starts Mon 03-09 (EDT), the previous
		// started Mon 03-02 (EST), so last-week is 7 days minus 1 hour long.
		got, _ := Resolve("last-week", at(ny, 2026, 3, 11, 10, 0), ny)
		wantSince := time.Date(2026, 3, 2, 5, 0, 0, 0, time.UTC)
		wantBefore := time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC)
		if !got.Since.Equal(wantSince) || !got.Before.Equal(wantBefore) {
			t.Errorf("got [%v, %v), want [%v, %v)", got.Since, got.Before, wantSince, wantBefore)
		}
	})
	t.Run("last-1d is calendar-day across spring forward", func(t *testing.T) {
		now := at(ny, 2026, 3, 8, 12, 0) // EDT, after the transition
		got, _ := Resolve("last-1d", now, ny)
		if want := at(ny, 2026, 3, 7, 12, 0); !got.Since.Equal(want) {
			t.Errorf("Since = %v, want %v (same wall clock yesterday)", got.Since, want)
		}
		if d := now.Sub(got.Since); d != 23*time.Hour {
			t.Errorf("elapsed = %v, want 23h across the transition", d)
		}
	})
	t.Run("last-24h is elapsed across spring forward", func(t *testing.T) {
		now := at(ny, 2026, 3, 8, 12, 0)
		got, _ := Resolve("last-24h", now, ny)
		if want := now.Add(-24 * time.Hour); !got.Since.Equal(want) {
			t.Errorf("Since = %v, want %v", got.Since, want)
		}
		if d := now.Sub(got.Since); d != 24*time.Hour {
			t.Errorf("elapsed = %v, want 24h", d)
		}
	})
	t.Run("last-1d is 25 elapsed hours across fall back", func(t *testing.T) {
		now := at(ny, 2026, 11, 1, 12, 0) // EST, after the transition
		got, _ := Resolve("last-1d", now, ny)
		if d := now.Sub(got.Since); d != 25*time.Hour {
			t.Errorf("elapsed = %v, want 25h across fall back", d)
		}
	})
}

func TestResolveUTCZone(t *testing.T) {
	now := time.Date(2026, 10, 7, 15, 30, 0, 0, time.UTC)
	got, err := Resolve("today", now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Since.Equal(time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)) ||
		!got.Before.Equal(time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("unexpected UTC today: %+v", got)
	}
}

func TestResolveErrors(t *testing.T) {
	now := time.Date(2026, 10, 7, 15, 30, 0, 0, time.UTC)
	for _, spec := range []string{
		"tomorrow", "Today", "2026-13-01", "2026-02-30", "2026-9-1", "26-09-01",
		"2026-09-01..", "..2026-09-01", "2026-09-03..2026-09-01", "2026-09-01..bogus",
		"last-d", "last-0d", "last-0h", "last-3", "last-3w", "last--3d", "last-+3d", "last-3.5d",
		"last-", "week ", " all", "all..all",
	} {
		t.Run(spec, func(t *testing.T) {
			_, err := Resolve(spec, now, time.UTC)
			if err == nil {
				t.Fatalf("Resolve(%q) succeeded, want error", spec)
			}
		})
	}

	t.Run("unrecognized names accepted forms", func(t *testing.T) {
		_, err := Resolve("tomorrow", now, time.UTC)
		if err == nil || !strings.Contains(err.Error(), Forms) {
			t.Errorf("error %v does not name the accepted forms", err)
		}
	})
	t.Run("nil zone", func(t *testing.T) {
		if _, err := Resolve("today", now, nil); err == nil {
			t.Error("nil zone accepted")
		}
	})
}
