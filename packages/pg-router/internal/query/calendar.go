package query

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// CalendarTrigger fires a timer query at a wall-clock time of day (optionally
// on named weekdays) in a named zone: the "calendar" trigger kind. This file is
// the schedule arithmetic only (Next and Prev); firing, state and the missed-slot
// policies live in the driver.
//
// Field types (the design leaves them open): At is a clock time as hour and
// minute, Days is the set of weekdays (empty means every day), Loc is the
// resolved zone (never nil once built by the config layer), OnMissed is the
// validated policy string ("run-now" or "next-slot") and ExpiresIn is the
// per-event expiry window (zero means "until the next slot").
type CalendarTrigger struct {
	At        ClockTime
	Days      []time.Weekday
	Loc       *time.Location
	OnMissed  string
	ExpiresIn time.Duration
	// TZSet is true when the configuration named a zone (tz); false means Loc is
	// the host zone at process start (doctor warns about a UTC host then).
	TZSet bool
}

// ClockTime is a 24-hour wall-clock time of day.
type ClockTime struct{ Hour, Minute int }

func (CalendarTrigger) kind() triggerKind { return kindCalendar }

// IsCalendar reports whether t is a calendar trigger.
func IsCalendar(t Trigger) bool {
	return t != nil && t.kind() == kindCalendar
}

// Calendar returns the trigger's calendar spec and true iff t is a
// CalendarTrigger.
func Calendar(t Trigger) (CalendarTrigger, bool) {
	if t == nil {
		return CalendarTrigger{}, false
	}
	ct, ok := t.(CalendarTrigger)
	return ct, ok
}

func (c CalendarTrigger) loc() *time.Location {
	if c.Loc == nil {
		return time.UTC
	}
	return c.Loc
}

func (c CalendarTrigger) runsOn(d time.Weekday) bool {
	if len(c.Days) == 0 {
		return true
	}
	for _, w := range c.Days {
		if w == d {
			return true
		}
	}
	return false
}

// Next returns the first slot strictly greater than after: a slot passed as
// after returns the following slot, and one nanosecond before a slot returns
// that slot. It returns the zero time when the trigger has no weekday that can
// ever match (not reachable for a config-validated trigger).
func (c CalendarTrigger) Next(after time.Time) time.Time {
	for _, s := range c.slotsAround(after, -1, 8) {
		if s.After(after) {
			return s
		}
	}
	return time.Time{}
}

// Prev returns the latest slot <= t, or the zero time when none exists within
// the scanned window.
func (c CalendarTrigger) Prev(t time.Time) time.Time {
	slots := c.slotsAround(t, -8, 1)
	for i := len(slots) - 1; i >= 0; i-- {
		if !slots[i].After(t) {
			return slots[i]
		}
	}
	return time.Time{}
}

// slotsAround returns the slot instants, ascending, for the local dates
// [date(t)+from, date(t)+to] in the trigger's zone.
func (c CalendarTrigger) slotsAround(t time.Time, from, to int) []time.Time {
	loc := c.loc()
	lt := t.In(loc)
	base := time.Date(lt.Year(), lt.Month(), lt.Day(), 0, 0, 0, 0, time.UTC)
	var out []time.Time
	for off := from; off <= to; off++ {
		d := base.AddDate(0, 0, off)
		if !c.runsOn(d.Weekday()) {
			continue
		}
		out = append(out, resolveWallTime(loc, d.Year(), d.Month(), d.Day(), c.At.Hour, c.At.Minute))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// resolveWallTime maps a local date and H:M to an instant, applying the two
// daylight-saving rules explicitly because time.Date's result for a gap or an
// overlap is unspecified:
//
//   - Rule A (spring-forward): a wall time that does not exist resolves to the
//     transition instant, the first instant after the gap.
//   - Rule B (fall-back): a wall time that occurs twice resolves to its first
//     occurrence.
//
// The day's transition is found with Time.ZoneBounds around local noon (a time
// that exists in every real zone), whose period bounds are the nearest
// transitions on either side.
func resolveWallTime(loc *time.Location, y int, m time.Month, d, h, min int) time.Time {
	wall := time.Date(y, m, d, h, min, 0, 0, time.UTC) // the wall time as if UTC
	noon := time.Date(y, m, d, 12, 0, 0, 0, loc)
	start, end := noon.ZoneBounds()
	for _, tr := range []time.Time{start, end} {
		if tr.IsZero() {
			continue
		}
		_, before := tr.Add(-time.Nanosecond).In(loc).Zone()
		_, after := tr.In(loc).Zone()
		bo, ao := time.Duration(before)*time.Second, time.Duration(after)*time.Second
		switch {
		case ao > bo:
			// Gap: wall times [tr+bo, tr+ao) do not exist.
			if !wall.Before(tr.Add(bo)) && wall.Before(tr.Add(ao)) {
				return tr
			}
		case ao < bo:
			// Overlap: wall times [tr+ao, tr+bo) occur twice; take the first,
			// which is on the pre-transition offset.
			if !wall.Before(tr.Add(ao)) && wall.Before(tr.Add(bo)) {
				return wall.Add(-bo).In(loc)
			}
		}
	}
	return time.Date(y, m, d, h, min, 0, 0, loc)
}

// MaxGap returns the largest gap between consecutive slots over the 14 days
// after now (daily 24h, weekly 7d, absent a DST shift in the window). It is the
// trigger's expected tick interval for staleness alerts. now is injected so
// callers and tests are deterministic.
func (c CalendarTrigger) MaxGap(now time.Time) time.Duration {
	end := now.AddDate(0, 0, 14)
	var maxGap time.Duration
	prev := c.Next(now)
	for prev.Before(end) && !prev.IsZero() {
		next := c.Next(prev)
		if next.IsZero() {
			break
		}
		if g := next.Sub(prev); g > maxGap {
			maxGap = g
		}
		prev = next
	}
	return maxGap
}

// slotLayout is the minute-precision RFC 3339 form operators read in status and
// config check output (for example 2026-11-01T03:30-08:00).
const slotLayout = "2006-01-02T15:04Z07:00"

// FormatSlot renders t in the trigger's zone at minute precision.
func (c CalendarTrigger) FormatSlot(t time.Time) string {
	return t.In(c.loc()).Format(slotLayout)
}

// ZoneName is the zone's name, or "host zone" when the trigger uses the host
// zone (whose Go name is the unhelpful "Local").
func (c CalendarTrigger) ZoneName() string {
	if n := c.loc().String(); n != "Local" {
		return n
	}
	return "host zone"
}

// Describe is the resolved schedule as one operator-readable phrase, for example
// "calendar 03:30 sun America/Los_Angeles" or "calendar 02:00 daily UTC".
func (c CalendarTrigger) Describe() string {
	days := "daily"
	if len(c.Days) > 0 {
		names := make([]string, len(c.Days))
		for i, d := range c.Days {
			names[i] = strings.ToLower(d.String()[:3])
		}
		days = strings.Join(names, ",")
	}
	return fmt.Sprintf("calendar %02d:%02d %s %s", c.At.Hour, c.At.Minute, days, c.ZoneName())
}

// NextN returns the next n slots strictly after t, ascending.
func (c CalendarTrigger) NextN(t time.Time, n int) []time.Time {
	var out []time.Time
	for len(out) < n {
		t = c.Next(t)
		if t.IsZero() {
			break
		}
		out = append(out, t)
	}
	return out
}

// MovedByDST reports whether slot, resolved by Next or Prev, sits at a wall time
// other than the configured one: the configured time falls in a spring-forward
// gap and the slot moved to the transition instant.
func (c CalendarTrigger) MovedByDST(slot time.Time) bool {
	l := slot.In(c.loc())
	return l.Hour() != c.At.Hour || l.Minute() != c.At.Minute
}
