package query

import (
	"testing"
	"time"
)

func TestCalendarDescribeNextNAndDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	c := CalendarTrigger{At: ClockTime{Hour: 2, Minute: 30}, Days: []time.Weekday{time.Sunday, time.Monday}, Loc: loc, OnMissed: "run-now"}
	if got := c.Describe(); got != "calendar 02:30 sun,mon America/New_York" {
		t.Errorf("Describe = %q", got)
	}
	if got := (CalendarTrigger{At: ClockTime{Hour: 2}, Loc: time.UTC}).Describe(); got != "calendar 02:00 daily UTC" {
		t.Errorf("Describe = %q", got)
	}
	if got := (CalendarTrigger{At: ClockTime{Hour: 2}}).ZoneName(); got != "UTC" {
		t.Errorf("nil Loc zone = %q", got)
	}
	got := c.NextN(time.Date(2027, 3, 1, 0, 0, 0, 0, loc), 3)
	if len(got) != 3 || c.FormatSlot(got[0]) != "2027-03-01T02:30-05:00" || c.FormatSlot(got[1]) != "2027-03-07T02:30-05:00" {
		t.Errorf("NextN = %v", got)
	}
	// 2027-03-14 02:30 does not exist: the slot moves to 03:00 EDT.
	moved := c.Next(time.Date(2027, 3, 12, 0, 0, 0, 0, loc))
	if !moved.Equal(time.Date(2027, 3, 14, 3, 0, 0, 0, loc)) || !c.MovedByDST(moved) {
		t.Errorf("gap slot = %v moved=%v", moved, c.MovedByDST(moved))
	}
	if c.MovedByDST(got[0]) {
		t.Error("an ordinary slot is not moved")
	}
}
