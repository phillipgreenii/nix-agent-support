package focus

import (
	"strings"
	"time"
)

// HorizonDays is the width of the rank's due-date horizon: a due date 0 to
// HorizonDays days after the addressed day, inclusive, is inside it.
const HorizonDays = 7

// dueLayouts are the timestamp layouts a due value may carry, tried in order.
// The first two carry their own offset and are converted to the focus time
// zone; the last has none and is read in the focus time zone.
var (
	zonedDueLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999Z0700"}
	zonelessLayout  = "2006-01-02T15:04:05.999999999"
)

// calendarDay is a calendar date as a count of days since 1970-01-01, so two
// days compare as integers and a difference is a number of calendar days.
type calendarDay int64

func dayOf(year int, month time.Month, day int) calendarDay {
	return calendarDay(time.Date(year, month, day, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

// day is the calendar date t shows in its own location.
func dayOfTime(t time.Time) calendarDay {
	y, m, d := t.Date()
	return dayOf(y, m, d)
}

func (d calendarDay) format() string {
	return time.Unix(int64(d)*86400, 0).UTC().Format(time.DateOnly)
}

// parseDueDay reads a stored due value as a calendar day in loc. A date-only
// value (YYYY-MM-DD) is that calendar day; a timestamp with an offset is
// converted to loc and then truncated to its day; a timestamp with no offset
// is read in loc. An empty or unparseable value is not a day (ok false); the
// caller counts the unparseable ones and never treats one as an error.
func parseDueDay(raw string, loc *time.Location) (day calendarDay, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return dayOfTime(t), true
	}
	for _, layout := range zonedDueLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return dayOfTime(t.In(loc)), true
		}
	}
	if t, err := time.ParseInLocation(zonelessLayout, s, loc); err == nil {
		return dayOfTime(t), true
	}
	return 0, false
}

// addressedDay is the calendar day the rank is computed for: the calendar
// date of date when it is set (in its own location, so a caller that built
// it from --date has already chosen the zone), else the day of the injected
// clock's reading in loc.
func addressedDay(date time.Time, now time.Time, loc *time.Location) calendarDay {
	if !date.IsZero() {
		return dayOfTime(date)
	}
	return dayOfTime(now.In(loc))
}
