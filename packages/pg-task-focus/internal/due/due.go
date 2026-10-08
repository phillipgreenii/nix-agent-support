// Package due resolves a task's due rule against the civil dates of a period.
// A rule names a time of day, a zone and, by cadence, a weekday or a day of
// the sprint. It resolves in its own zone, whatever zone the period was
// created in, to one UTC instant that the task instance stores.
package due

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/zone"
)

// Cadence is how often a task recurs, and so which fields its rule takes.
type Cadence string

// The cadences. A daily rule takes at and tz; a weekly rule adds weekday; a
// sprint rule adds day.
const (
	Daily  Cadence = "daily"
	Weekly Cadence = "weekly"
	Sprint Cadence = "sprint"
)

// Rule says when a task is due within a period. Its JSON form is the snapshot
// stored in the log: {"at","tz","weekday"?,"day"?}.
type Rule struct {
	At      civil.TimeOfDay
	TZ      zone.Zone
	Weekday *time.Weekday // weekly rules only
	Day     *int          // sprint rules only; 1 is the period's first civil date
}

// Resolution is a rule resolved against a period.
type Resolution struct {
	Instant time.Time  // the due instant, in UTC
	Date    civil.Date // the civil date in the rule's zone on which it falls
}

// NoMatch is returned by Resolve when a well-formed rule matches no civil date
// of its period, for example a sprint day past the sprint's end. The task is
// then not materialized, and Reason says why.
type NoMatch struct{ Reason string }

func (n *NoMatch) Error() string { return n.Reason }

// weekdayNames are the wire names of the weekdays, indexed by time.Weekday.
var weekdayNames = [...]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

func weekdayName(w time.Weekday) string { return weekdayNames[w] }

// Validate checks that the rule carries exactly the fields its cadence takes
// and that each is in range. It does not check the rule against a period.
func (r Rule) Validate(c Cadence) error {
	if r.TZ.Name() == "" {
		return errors.New("a due rule MUST name its time zone")
	}
	if r.At.Hour < 0 || r.At.Hour > 23 || r.At.Minute < 0 || r.At.Minute > 59 {
		return fmt.Errorf("due time %02d:%02d is out of range (00:00 to 23:59)", r.At.Hour, r.At.Minute)
	}
	if r.Weekday != nil && (*r.Weekday < time.Sunday || *r.Weekday > time.Saturday) {
		return fmt.Errorf("due weekday %d is not a day of the week", int(*r.Weekday))
	}
	switch c {
	case Daily:
		if r.Weekday != nil || r.Day != nil {
			return errors.New("a daily due rule takes only at and tz")
		}
	case Weekly:
		if r.Weekday == nil {
			return errors.New("a weekly due rule requires weekday")
		}
		if r.Day != nil {
			return errors.New("a weekly due rule takes at, tz and weekday, not day")
		}
	case Sprint:
		if r.Day == nil {
			return errors.New("a sprint due rule requires day")
		}
		if r.Weekday != nil {
			return errors.New("a sprint due rule takes at, tz and day, not weekday")
		}
		if *r.Day < 1 {
			return fmt.Errorf("due day %d is less than 1", *r.Day)
		}
	default:
		return fmt.Errorf("cadence %q is not one of daily, weekly, sprint", string(c))
	}
	return nil
}

// Resolve returns the instant at which the rule falls in the period that runs
// from start to end, both inclusive (end equals start for a daily period).
//
// A weekly rule resolves to the first occurrence of its weekday in the period.
// A sprint rule's day counts calendar days from start, with 1 the first. A
// rule that matches no civil date of the period returns a *NoMatch. A rule
// that does not fit its cadence, or a period that is not well-formed, returns
// another error. The date resolves in the rule's own zone, so a civil time
// that does not exist or occurs twice follows the zone package's rules.
func Resolve(c Cadence, r Rule, start, end civil.Date) (Resolution, error) {
	if err := r.Validate(c); err != nil {
		return Resolution{}, err
	}
	days := start.DaysUntil(end) + 1
	if days < 1 {
		return Resolution{}, fmt.Errorf("period end %s is before its start %s", end, start)
	}
	if c == Daily && days != 1 {
		return Resolution{}, fmt.Errorf("a daily period is one day, not %s to %s", start, end)
	}

	var date civil.Date
	switch c {
	case Daily:
		date = start
	case Weekly:
		found := false
		for i := 0; i < days && i < 7; i++ {
			if d := start.AddDays(i); d.Weekday() == *r.Weekday {
				date, found = d, true
				break
			}
		}
		if !found {
			return Resolution{}, &NoMatch{Reason: fmt.Sprintf("the period %s to %s has no %s", start, end, weekdayName(*r.Weekday))}
		}
	case Sprint:
		if *r.Day > days {
			return Resolution{}, &NoMatch{Reason: fmt.Sprintf("day %d is after the period's end %s (the period has %d days)", *r.Day, end, days)}
		}
		date = start.AddDays(*r.Day - 1)
	}
	return Resolution{Instant: zone.ResolveCivil(r.TZ, date, r.At).UTC(), Date: date}, nil
}

// wireRule is the snapshot form. Pointers tell a missing field from a zero
// one.
type wireRule struct {
	At      *civil.TimeOfDay `json:"at"`
	TZ      *string          `json:"tz"`
	Weekday *string          `json:"weekday,omitempty"`
	Day     *int             `json:"day,omitempty"`
}

// MarshalJSON writes the snapshot form. It refuses a rule that names no zone
// or whose fields are out of range; it cannot check the fields against a
// cadence, which is Validate's job.
func (r Rule) MarshalJSON() ([]byte, error) {
	if r.TZ.Name() == "" {
		return nil, errors.New("a due rule MUST name its time zone")
	}
	if r.At.Hour < 0 || r.At.Hour > 23 || r.At.Minute < 0 || r.At.Minute > 59 {
		return nil, fmt.Errorf("due time %02d:%02d is out of range (00:00 to 23:59)", r.At.Hour, r.At.Minute)
	}
	w := wireRule{At: &r.At, TZ: new(string), Day: r.Day}
	*w.TZ = r.TZ.Name()
	if r.Weekday != nil {
		if *r.Weekday < time.Sunday || *r.Weekday > time.Saturday {
			return nil, fmt.Errorf("due weekday %d is not a day of the week", int(*r.Weekday))
		}
		name := weekdayName(*r.Weekday)
		w.Weekday = &name
	}
	return json.Marshal(w)
}

// UnmarshalJSON reads the snapshot form strictly: at and tz are required, an
// unknown field is an error, and the zone and weekday MUST be valid. Whether
// the field set fits a cadence is checked by Validate.
func (r *Rule) UnmarshalJSON(data []byte) error {
	var w wireRule
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return fmt.Errorf("due rule: %w", err)
	}
	if dec.More() {
		return errors.New("due rule: unexpected data after the object")
	}
	if w.At == nil {
		return errors.New("due rule: at is required")
	}
	if w.TZ == nil {
		return errors.New("due rule: tz is required")
	}
	z, err := zone.Load(*w.TZ)
	if err != nil {
		return fmt.Errorf("due rule: %w", err)
	}
	out := Rule{At: *w.At, TZ: z, Day: w.Day}
	if w.Weekday != nil {
		wd, err := civil.ParseWeekday(*w.Weekday)
		if err != nil {
			return fmt.Errorf("due rule: %w", err)
		}
		out.Weekday = &wd
	}
	*r = out
	return nil
}
