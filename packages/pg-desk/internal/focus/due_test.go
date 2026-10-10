package focus

import (
	"testing"
	"time"
)

func TestParseDueDay(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		raw  string
		loc  *time.Location
		want string // YYYY-MM-DD, or "" for unparseable
	}{
		{"date only is that calendar day (UTC)", "2026-10-10", time.UTC, "2026-10-10"},
		{"date only is that calendar day (New York)", "2026-10-10", ny, "2026-10-10"},
		{"date only is that calendar day (Los Angeles)", "2026-10-10", la, "2026-10-10"},
		{"padded", "  2026-10-10 ", time.UTC, "2026-10-10"},
		{"UTC timestamp converts forward", "2026-10-10T23:30:00Z", ny, "2026-10-10"},
		{"UTC timestamp past local midnight", "2026-10-11T02:30:00Z", ny, "2026-10-10"},
		{"UTC timestamp in UTC", "2026-10-11T02:30:00Z", time.UTC, "2026-10-11"},
		{"offset timestamp to UTC", "2026-10-10T23:30:00-05:00", time.UTC, "2026-10-11"},
		{"offset timestamp to Los Angeles", "2026-10-10T23:30:00-05:00", la, "2026-10-10"},
		{"fractional seconds", "2026-10-10T23:30:00.123456Z", time.UTC, "2026-10-10"},
		{"jira offset without a colon", "2026-10-10T23:30:00.000-0500", time.UTC, "2026-10-11"},
		{"no offset reads in the zone", "2026-10-10T23:59:59", la, "2026-10-10"},
		{"empty", "", time.UTC, ""},
		{"blank", "   ", time.UTC, ""},
		{"garbage", "next tuesday", time.UTC, ""},
		{"impossible date", "2026-02-30", time.UTC, ""},
		{"month first", "10/10/2026", time.UTC, ""},
		{"unpadded", "2026-1-5", time.UTC, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			day, ok := parseDueDay(c.raw, c.loc)
			if c.want == "" {
				if ok {
					t.Fatalf("parseDueDay(%q) = %s, want unparseable", c.raw, day.format())
				}
				return
			}
			if !ok || day.format() != c.want {
				t.Fatalf("parseDueDay(%q) = %v %v, want %s", c.raw, day.format(), ok, c.want)
			}
		})
	}
}

func TestCalendarDayArithmetic(t *testing.T) {
	a := dayOf(2026, time.October, 10)
	if got := dayOf(2026, time.October, 17) - a; got != 7 {
		t.Errorf("Oct 17 - Oct 10 = %d, want 7", got)
	}
	// Across a month, a leap day and a DST change the difference is calendar days.
	if got := dayOf(2026, time.November, 1) - dayOf(2026, time.October, 31); got != 1 {
		t.Errorf("Nov 1 - Oct 31 = %d, want 1", got)
	}
	if got := dayOf(2028, time.March, 1) - dayOf(2028, time.February, 28); got != 2 {
		t.Errorf("2028 Mar 1 - Feb 28 = %d, want 2 (leap year)", got)
	}
	if got := dayOf(2026, time.November, 2) - dayOf(2026, time.November, 1); got != 1 {
		t.Errorf("Nov 2 - Nov 1 = %d, want 1", got)
	}
	if dayOf(1969, time.December, 31).format() != "1969-12-31" {
		t.Errorf("a day before the epoch formats as %q", dayOf(1969, time.December, 31).format())
	}
}

func TestAddressedDay(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC) // 22:00 Oct 9 in New York
	if got := addressedDay(time.Time{}, now, ny).format(); got != "2026-10-09" {
		t.Errorf("default day = %s, want the clock's local day 2026-10-09", got)
	}
	if got := addressedDay(time.Time{}, now, time.UTC).format(); got != "2026-10-10" {
		t.Errorf("default day in UTC = %s, want 2026-10-10", got)
	}
	// An explicit date is its own calendar date, whatever the clock says.
	date := time.Date(2026, 10, 20, 0, 0, 0, 0, ny)
	if got := addressedDay(date, now, ny).format(); got != "2026-10-20" {
		t.Errorf("explicit day = %s, want 2026-10-20", got)
	}
}
