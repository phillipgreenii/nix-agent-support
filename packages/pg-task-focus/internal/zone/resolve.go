package zone

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
)

// wallClock reads t in loc as a wall-clock time held in a UTC value, so wall
// clock arithmetic is plain subtraction.
func wallClock(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), l.Nanosecond(), time.UTC)
}

// ResolveCivil returns the UTC instant at which the zone's wall clock reads
// the civil date and time of day.
//
// A civil time that does not exist (inside a gap, or on a date the zone
// skips) resolves to the first valid instant after the gap. A civil time that
// occurs twice resolves to the earlier occurrence. The standard library does
// neither reliably, so both are computed here.
func ResolveCivil(z Zone, d civil.Date, t civil.TimeOfDay) time.Time {
	loc := z.loc
	want := time.Date(d.Year, d.Month, d.Day, t.Hour, t.Minute, 0, 0, time.UTC)

	// The offsets in effect a day either side of the wall time (read as an
	// instant) cover both sides of any transition near it. Each offset gives a
	// candidate instant, kept when it reads back as the request.
	var best time.Time
	found := false
	for _, probe := range []time.Time{want.Add(-24 * time.Hour), want, want.Add(24 * time.Hour)} {
		_, off := probe.In(loc).Zone()
		cand := want.Add(-time.Duration(off) * time.Second)
		if wallClock(cand, loc).Equal(want) && (!found || cand.Before(best)) {
			best, found = cand, true
		}
	}
	if found {
		return best.UTC()
	}

	// A gap: no offset reads back as the request. The answer is the instant
	// the gap ends, the end of the zone interval that precedes it.
	horizon := want.Add(48 * time.Hour)
	cur := want.Add(-48 * time.Hour)
	for range 16 {
		_, end := cur.In(loc).ZoneBounds()
		if end.IsZero() || end.After(horizon) {
			break
		}
		gapStart := wallClock(end.Add(-time.Nanosecond), loc).Add(time.Nanosecond)
		gapEnd := wallClock(end, loc)
		if !want.Before(gapStart) && want.Before(gapEnd) {
			return end.UTC()
		}
		cur = end
	}
	// Unreachable for a zone with a consistent rule set; the standard
	// library's own answer is the best remaining one.
	return time.Date(d.Year, d.Month, d.Day, t.Hour, t.Minute, 0, 0, loc).UTC()
}
