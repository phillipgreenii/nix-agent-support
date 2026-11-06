package query

import (
	"testing"
	"time"

	_ "time/tzdata"
)

// Host zone files win over the embedded copy, so these tests pin only stable
// 2026 transitions and never assert which source answered.

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

func local(t *testing.T, loc *time.Location, layout string) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02 15:04", layout, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func cal(loc *time.Location, h, m int, days ...time.Weekday) CalendarTrigger {
	return CalendarTrigger{At: ClockTime{h, m}, Days: days, Loc: loc, OnMissed: "next-slot"}
}

func TestCalendarKindAccessors(t *testing.T) {
	var tr Trigger = cal(time.UTC, 3, 30)
	if !IsCalendar(tr) {
		t.Error("IsCalendar(calendar) = false")
	}
	if IsCalendar(nil) || IsCalendar(PeriodTrigger{}) || IsPeriod(tr) || IsManual(tr) {
		t.Error("calendar trigger misclassified")
	}
	if _, ok := Calendar(PeriodTrigger{}); ok {
		t.Error("Calendar(period) ok")
	}
	if _, ok := Calendar(nil); ok {
		t.Error("Calendar(nil) ok")
	}
	if got, ok := Calendar(tr); !ok || got.At != (ClockTime{3, 30}) {
		t.Errorf("Calendar(calendar) = %+v, %v", got, ok)
	}
}

func TestCalendarNextPrev(t *testing.T) {
	et := mustLoc(t, "US/Eastern")
	london := mustLoc(t, "Europe/London")
	// Havana changes its clocks at local midnight (2026-03-08 00:00 -> 01:00).
	havana := mustLoc(t, "America/Havana")

	type tc struct {
		name       string
		c          CalendarTrigger
		after      time.Time
		wantNext   time.Time
		wantPrev   time.Time // Prev(after)
		next2      time.Time // Next(wantNext), zero to skip
		skipPrev   bool
		prevOfNext time.Time // Prev(wantNext) == wantNext always; kept implicit
	}
	utc := func(s string) time.Time {
		v, err := time.Parse("2006-01-02 15:04", s)
		if err != nil {
			t.Fatal(err)
		}
		return v.UTC()
	}
	cases := []tc{
		{
			name:     "spring-forward gap: 02:30 does not exist, fires at 03:00 EDT",
			c:        cal(et, 2, 30, time.Sunday),
			after:    local(t, et, "2026-03-07 12:00"),
			wantNext: utc("2026-03-08 07:00"), // 03:00 EDT == 07:00 UTC
			wantPrev: local(t, et, "2026-03-01 02:30"),
			next2:    local(t, et, "2026-03-15 02:30"),
		},
		{
			name:     "fall-back overlap: 01:30 fires at its first (EDT) occurrence",
			c:        cal(et, 1, 30),
			after:    local(t, et, "2026-11-01 00:00"),
			wantNext: utc("2026-11-01 05:30"), // 01:30 EDT == 05:30 UTC
			wantPrev: local(t, et, "2026-10-31 01:30"),
			next2:    local(t, et, "2026-11-02 01:30"),
		},
		{
			name:     "after the second (EST) occurrence the next is the following day",
			c:        cal(et, 1, 30),
			after:    utc("2026-11-01 06:30"), // 01:30 EST
			wantNext: local(t, et, "2026-11-02 01:30"),
			// Prev is the one slot of that day, the first occurrence.
			wantPrev: utc("2026-11-01 05:30"),
		},
		{
			name:     "leap day",
			c:        cal(et, 0, 0),
			after:    local(t, et, "2028-02-28 23:00"),
			wantNext: local(t, et, "2028-02-29 00:00"),
			wantPrev: local(t, et, "2028-02-28 00:00"),
			next2:    local(t, et, "2028-03-01 00:00"),
		},
		{
			name:     "year boundary",
			c:        cal(et, 0, 0),
			after:    local(t, et, "2026-12-31 12:00"),
			wantNext: local(t, et, "2027-01-01 00:00"),
			wantPrev: local(t, et, "2026-12-31 00:00"),
		},
		{
			name:     "month boundary",
			c:        cal(et, 0, 0),
			after:    local(t, et, "2026-01-31 12:00"),
			wantNext: local(t, et, "2026-02-01 00:00"),
			wantPrev: local(t, et, "2026-01-31 00:00"),
		},
		{
			name:     "Monday slot across a DST week is +7 days of wall time",
			c:        cal(et, 9, 0, time.Monday),
			after:    local(t, et, "2026-03-02 09:00"), // exactly a slot
			wantNext: local(t, et, "2026-03-09 09:00"),
			wantPrev: local(t, et, "2026-03-02 09:00"), // Prev(t) is t itself at a slot
			next2:    local(t, et, "2026-03-16 09:00"),
		},
		{
			name:     "one nanosecond before a slot returns the slot",
			c:        cal(et, 9, 0, time.Monday),
			after:    local(t, et, "2026-03-09 09:00").Add(-time.Nanosecond),
			wantNext: local(t, et, "2026-03-09 09:00"),
			wantPrev: local(t, et, "2026-03-02 09:00"),
		},
		{
			name:     "London spring-forward: 01:30 does not exist on 2026-03-29",
			c:        cal(london, 1, 30),
			after:    local(t, london, "2026-03-28 12:00"),
			wantNext: utc("2026-03-29 01:00"), // transition: 01:00 GMT -> 02:00 BST
			wantPrev: local(t, london, "2026-03-28 01:30"),
			next2:    local(t, london, "2026-03-30 01:30"),
		},
		{
			name:     "London fall-back: 01:30 occurs twice on 2026-10-25, first is BST",
			c:        cal(london, 1, 30),
			after:    local(t, london, "2026-10-24 12:00"),
			wantNext: utc("2026-10-25 00:30"), // 01:30 BST
			wantPrev: local(t, london, "2026-10-24 01:30"),
			next2:    local(t, london, "2026-10-26 01:30"),
		},
		{
			name: "midnight transition (Havana 2026-03-08 00:00 -> 01:00): 00:30 does not exist",
			c:    cal(havana, 0, 30),
			// Havana is UTC-5 before the change, so the transition instant is
			// 00:00 CST == 05:00 UTC, and that is where the slot lands.
			after:    local(t, havana, "2026-03-07 12:00"),
			wantNext: utc("2026-03-08 05:00"),
			wantPrev: local(t, havana, "2026-03-07 00:30"),
			next2:    local(t, havana, "2026-03-09 00:30"),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.c.Next(c.after); !got.Equal(c.wantNext) {
				t.Errorf("Next(%v) = %v, want %v", c.after, got, c.wantNext)
			}
			if got := c.c.Prev(c.wantNext); !got.Equal(c.wantNext) {
				t.Errorf("Prev(slot %v) = %v, want the slot itself", c.wantNext, got)
			}
			if got := c.c.Prev(c.wantNext.Add(-time.Nanosecond)); !got.Before(c.wantNext) {
				t.Errorf("Prev(one ns before slot) = %v, want an earlier slot", got)
			}
			if got := c.c.Prev(c.after); !got.Equal(c.wantPrev) {
				t.Errorf("Prev(%v) = %v, want %v", c.after, got, c.wantPrev)
			}
			if !c.next2.IsZero() {
				if got := c.c.Next(c.wantNext); !got.Equal(c.next2) {
					t.Errorf("Next(slot %v) = %v, want %v", c.wantNext, got, c.next2)
				}
			}
		})
	}
}

// The slot sequence must strictly increase: a gap slot resolved to the
// transition instant must never tie or reorder with its neighbours.
func TestCalendarNextIsStrictlyIncreasingAcrossDST(t *testing.T) {
	et := mustLoc(t, "US/Eastern")
	c := cal(et, 2, 30)
	cur := local(t, et, "2026-02-20 00:00")
	for i := 0; i < 400; i++ {
		n := c.Next(cur)
		if !n.After(cur) {
			t.Fatalf("Next(%v) = %v, not after", cur, n)
		}
		cur = n
	}
}

func TestCalendarMaxGap(t *testing.T) {
	utc := time.UTC
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, utc) // no DST in UTC
	if got := cal(utc, 3, 30).MaxGap(now); got != 24*time.Hour {
		t.Errorf("daily MaxGap = %v, want 24h", got)
	}
	if got := cal(utc, 3, 30, time.Sunday).MaxGap(now); got != 7*24*time.Hour {
		t.Errorf("weekly MaxGap = %v, want 168h", got)
	}
	// Mon and Thu: gaps 3d and 4d, the largest is 4d.
	if got := cal(utc, 3, 30, time.Monday, time.Thursday).MaxGap(now); got != 4*24*time.Hour {
		t.Errorf("mon+thu MaxGap = %v, want 96h", got)
	}
}
