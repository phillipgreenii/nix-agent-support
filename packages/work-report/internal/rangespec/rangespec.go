// Package rangespec resolves a work-report range spec string into an absolute
// half-open instant range [Since, Before) in a configured IANA time zone.
//
// Accepted specs (an empty spec means "today"):
//
//	today                       the local day containing now
//	yesterday                   the local day before today
//	YYYY-MM-DD                  that local day
//	YYYY-MM-DD..YYYY-MM-DD      both dates inclusive (Before = start of the day after the second)
//	last-<N>d                   the N calendar days ending at now (see below)
//	last-<N>h                   the N elapsed hours ending at now
//	week                        this ISO week: Monday 00:00 through now
//	last-week                   the previous ISO week: Monday 00:00 up to, excluding, the following Monday 00:00
//	all                         open-ended start (OpenStart) through now
//
// Day boundaries are computed in the supplied zone, so daylight-saving
// transitions are handled exactly once, here: a local day may be 23 or 25
// hours long and its boundaries are still the correct local-midnight instants.
//
// last-<N>d is a CALENDAR-day window: Since is the same local wall-clock time
// N calendar days before now (so across a DST transition it may differ from
// N*24h by an hour). last-<N>h is an ELAPSED-duration window: Since is exactly
// N hours before now, regardless of zone.
//
// Ranges are half-open: Since inclusive, Before exclusive. Before is always
// set; for open-start ranges it is now. Since is the zero time iff OpenStart.
package rangespec

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Range is an absolute half-open instant range [Since, Before).
type Range struct {
	Since, Before time.Time
	OpenStart     bool // Since is the zero time iff OpenStart
}

// Forms lists the accepted spec forms, for error messages and help text.
const Forms = "today, yesterday, YYYY-MM-DD, YYYY-MM-DD..YYYY-MM-DD, last-<N>d, last-<N>h, week, last-week, all"

const dateLayout = "2006-01-02"

// Resolve turns spec into an absolute Range using now and the zone loc for day
// boundaries. An unrecognized spec yields an error naming the accepted forms.
func Resolve(spec string, now time.Time, loc *time.Location) (Range, error) {
	if loc == nil {
		return Range{}, errors.New("rangespec: nil time zone")
	}
	now = now.In(loc)

	switch spec {
	case "", "today":
		start := startOfDay(now)
		return Range{Since: start, Before: nextDay(start)}, nil
	case "yesterday":
		today := startOfDay(now)
		return Range{Since: prevDay(today), Before: today}, nil
	case "week":
		return Range{Since: startOfISOWeek(now), Before: now}, nil
	case "last-week":
		thisWeek := startOfISOWeek(now)
		return Range{Since: thisWeek.AddDate(0, 0, -7), Before: thisWeek}, nil
	case "all":
		return Range{OpenStart: true, Before: now}, nil
	}

	if first, second, ok := strings.Cut(spec, ".."); ok {
		from, err := parseDate(first, loc)
		if err != nil {
			return Range{}, unrecognized(spec)
		}
		to, err := parseDate(second, loc)
		if err != nil {
			return Range{}, unrecognized(spec)
		}
		if to.Before(from) {
			return Range{}, fmt.Errorf("rangespec: range %q ends before it starts", spec)
		}
		return Range{Since: from, Before: nextDay(to)}, nil
	}

	if day, err := parseDate(spec, loc); err == nil {
		return Range{Since: day, Before: nextDay(day)}, nil
	}

	if n, unit, ok := parseLast(spec); ok {
		switch unit {
		case 'd':
			return Range{Since: now.AddDate(0, 0, -n), Before: now}, nil
		case 'h':
			return Range{Since: now.Add(-time.Duration(n) * time.Hour), Before: now}, nil
		}
	}

	return Range{}, unrecognized(spec)
}

func unrecognized(spec string) error {
	return fmt.Errorf("rangespec: unrecognized range spec %q; accepted forms: %s", spec, Forms)
}

// parseDate parses a strict YYYY-MM-DD as local midnight in loc.
func parseDate(s string, loc *time.Location) (time.Time, error) {
	return time.ParseInLocation(dateLayout, s, loc)
}

// parseLast parses last-<N>d / last-<N>h with N a positive decimal integer.
func parseLast(spec string) (n int, unit byte, ok bool) {
	rest, found := strings.CutPrefix(spec, "last-")
	if !found || len(rest) < 2 {
		return 0, 0, false
	}
	unit = rest[len(rest)-1]
	if unit != 'd' && unit != 'h' {
		return 0, 0, false
	}
	digits := rest[:len(rest)-1]
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, 0, false
		}
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n < 1 {
		return 0, 0, false
	}
	return n, unit, true
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func nextDay(startOfDay time.Time) time.Time {
	y, m, d := startOfDay.Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, startOfDay.Location())
}

func prevDay(startOfDay time.Time) time.Time {
	y, m, d := startOfDay.Date()
	return time.Date(y, m, d-1, 0, 0, 0, 0, startOfDay.Location())
}

// startOfISOWeek returns Monday 00:00 local of the ISO week containing t.
func startOfISOWeek(t time.Time) time.Time {
	back := (int(t.Weekday()) + 6) % 7 // Monday=0 .. Sunday=6
	y, m, d := t.Date()
	return time.Date(y, m, d-back, 0, 0, 0, 0, t.Location())
}
