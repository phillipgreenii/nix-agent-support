package projection

import (
	"slices"
	"time"
)

// The timer math is derived from the segments and boosts at the read time;
// nothing ticks into the log. Every method takes the instant it reads at, so
// it answers for any instant, not only the present.

// Elapsed is the cycle's running time up to now: the sum of its segments, an
// open segment ending at now, and only the part of each segment before now.
// It is never negative.
func (c Cycle) Elapsed(now time.Time) time.Duration {
	var d time.Duration
	for _, s := range c.Segments {
		end := now
		if s.End != nil && s.End.Before(now) {
			end = *s.End
		}
		if end.After(s.Start) {
			d += end.Sub(s.Start)
		}
	}
	return d
}

// Remaining is the planned minutes plus the boosts effective by now, less the
// elapsed time. It is negative in overtime.
func (c Cycle) Remaining(now time.Time) time.Duration {
	return c.budget(now) - c.Elapsed(now)
}

// budget is the planned time plus the boosts effective at or before now.
func (c Cycle) budget(now time.Time) time.Duration {
	d := time.Duration(c.PlannedMinutes) * time.Minute
	for _, b := range c.Boosts {
		if !b.At.After(now) {
			d += time.Duration(b.Minutes) * time.Minute
		}
	}
	return d
}

// Overtime reports whether the cycle is running at now with time remaining
// below zero. A paused cycle is never in overtime.
func (c Cycle) Overtime(now time.Time) bool {
	return c.StatusAt(now) == Running && c.Remaining(now) < 0
}

// TimeUp reports whether the cycle, running or paused at now, has no time
// remaining. It is the alert boundary: true at the very instant the remaining
// time reaches zero, where Overtime is still false.
func (c Cycle) TimeUp(now time.Time) bool {
	switch c.StatusAt(now) {
	case Running, Paused:
		return c.Remaining(now) <= 0
	}
	return false
}

// OvertimeSince returns, while the cycle is in overtime at now, the instant
// its remaining time last reached zero: the start of the stretch, up to now,
// in which it never went above zero. A pause inside the stretch keeps it, a
// boost that leaves the remaining time at or below zero keeps it, and a boost
// that lifts it above zero ends it, so the next crossing starts a new one.
func (c Cycle) OvertimeSince(now time.Time) (time.Time, bool) {
	if !c.Overtime(now) {
		return time.Time{}, false
	}
	// Between two neighbouring change points the cycle is either running or
	// not, and no boost takes effect, so the remaining time is linear there.
	points := []time.Time{now}
	for _, s := range c.Segments {
		points = append(points, s.Start)
		if s.End != nil {
			points = append(points, *s.End)
		}
	}
	for _, b := range c.Boosts {
		points = append(points, b.At)
	}
	slices.SortFunc(points, time.Time.Compare)
	points = slices.CompactFunc(points, time.Time.Equal)

	left := time.Duration(c.PlannedMinutes) * time.Minute
	over := false
	var since time.Time
	next := 0 // the next boost to apply; the boosts are in time order
	for i, p := range points {
		if p.After(now) {
			break
		}
		for next < len(c.Boosts) && !c.Boosts[next].At.After(p) {
			left += time.Duration(c.Boosts[next].Minutes) * time.Minute
			next++
			if left > 0 {
				over = false
			}
		}
		if i+1 == len(points) || points[i+1].After(now) {
			break
		}
		if c.StatusAt(p) != Running {
			continue
		}
		d := points[i+1].Sub(p)
		if !over && left-d <= 0 {
			since, over = p.Add(left), true
		}
		left -= d
	}
	return since, true
}

// StatusAt is the cycle's state at instant t: not started before its start;
// running from the start or a resume, up to but not including the next pause
// or stop; paused from a pause; stopped at and after StoppedAt.
func (c Cycle) StatusAt(t time.Time) CycleStatus {
	if len(c.Segments) == 0 || t.Before(c.Segments[0].Start) {
		return NotStarted
	}
	if c.StoppedAt != nil && !t.Before(*c.StoppedAt) {
		return Stopped
	}
	for _, s := range c.Segments {
		if !t.Before(s.Start) && (s.End == nil || t.Before(*s.End)) {
			return Running
		}
	}
	return Paused
}
