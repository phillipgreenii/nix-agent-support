// Package civil holds zone-free calendar dates and times of day. A civil
// value names no instant: resolving it needs a zone (see package zone).
package civil

import (
	"fmt"
	"time"
)

// Date is a calendar date with no zone. Its text and JSON form is
// "2026-10-07".
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// TimeOfDay is a wall-clock time with no zone and no date. Its text and JSON
// form is "09:30".
type TimeOfDay struct {
	Hour, Minute int
}

// digits parses s as exactly len(s) ASCII digits.
func digits(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// ParseDate parses a strict "YYYY-MM-DD" date that exists in the calendar.
func ParseDate(s string) (Date, error) {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return Date{}, fmt.Errorf("date %q is not in the form YYYY-MM-DD", s)
	}
	y, okY := digits(s[0:4])
	m, okM := digits(s[5:7])
	d, okD := digits(s[8:10])
	if !okY || !okM || !okD {
		return Date{}, fmt.Errorf("date %q is not in the form YYYY-MM-DD", s)
	}
	date := Date{Year: y, Month: time.Month(m), Day: d}
	if m < 1 || m > 12 || d < 1 || date.toTime().Day() != d || date.toTime().Month() != time.Month(m) {
		return Date{}, fmt.Errorf("date %q does not exist in the calendar", s)
	}
	return date, nil
}

// ParseTimeOfDay parses a strict "HH:MM" time of day, 00:00 to 23:59.
func ParseTimeOfDay(s string) (TimeOfDay, error) {
	if len(s) != 5 || s[2] != ':' {
		return TimeOfDay{}, fmt.Errorf("time of day %q is not in the form HH:MM", s)
	}
	h, okH := digits(s[0:2])
	m, okM := digits(s[3:5])
	if !okH || !okM {
		return TimeOfDay{}, fmt.Errorf("time of day %q is not in the form HH:MM", s)
	}
	if h > 23 || m > 59 {
		return TimeOfDay{}, fmt.Errorf("time of day %q is out of range (00:00 to 23:59)", s)
	}
	return TimeOfDay{Hour: h, Minute: m}, nil
}

var weekdays = map[string]time.Weekday{
	"mon": time.Monday,
	"tue": time.Tuesday,
	"wed": time.Wednesday,
	"thu": time.Thursday,
	"fri": time.Friday,
	"sat": time.Saturday,
	"sun": time.Sunday,
}

// ParseWeekday parses a lower-case three-letter weekday, "mon" to "sun".
func ParseWeekday(s string) (time.Weekday, error) {
	if w, ok := weekdays[s]; ok {
		return w, nil
	}
	return 0, fmt.Errorf("weekday %q is not one of mon, tue, wed, thu, fri, sat, sun", s)
}

// toTime is the date's midnight in UTC; UTC has no gaps, so day arithmetic
// on it is exact.
func (d Date) toTime() time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
}

func fromTime(t time.Time) Date {
	return Date{Year: t.Year(), Month: t.Month(), Day: t.Day()}
}

// String returns the date as "YYYY-MM-DD".
func (d Date) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, int(d.Month), d.Day)
}

// AddDays returns the date n calendar days later (earlier when n is negative).
func (d Date) AddDays(n int) Date {
	return fromTime(d.toTime().AddDate(0, 0, n))
}

// DaysUntil returns the number of days from d to o; it is negative when o is
// earlier.
func (d Date) DaysUntil(o Date) int {
	return int(o.toTime().Sub(d.toTime()) / (24 * time.Hour))
}

// Weekday returns the day of the week.
func (d Date) Weekday() time.Weekday { return d.toTime().Weekday() }

// Compare returns -1, 0 or 1 as d is before, equal to or after o.
func (d Date) Compare(o Date) int {
	return d.toTime().Compare(o.toTime())
}

// MarshalText implements encoding.TextMarshaler (and so JSON).
func (d Date) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler (and so JSON).
func (d *Date) UnmarshalText(b []byte) error {
	p, err := ParseDate(string(b))
	if err != nil {
		return err
	}
	*d = p
	return nil
}

// String returns the time of day as "HH:MM".
func (t TimeOfDay) String() string {
	return fmt.Sprintf("%02d:%02d", t.Hour, t.Minute)
}

// MarshalText implements encoding.TextMarshaler (and so JSON).
func (t TimeOfDay) MarshalText() ([]byte, error) { return []byte(t.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler (and so JSON).
func (t *TimeOfDay) UnmarshalText(b []byte) error {
	p, err := ParseTimeOfDay(string(b))
	if err != nil {
		return err
	}
	*t = p
	return nil
}
