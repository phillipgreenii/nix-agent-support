package query

import (
	"context"
	"testing"
	"time"
)

func nyLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func calTimer(t *testing.T, expiresIn time.Duration) TimerQuery {
	t.Helper()
	return TimerQuery{
		Meta: Meta{EmitTypes: []string{"scan.due"}, Trig: CalendarTrigger{
			At: ClockTime{Hour: 2}, Loc: nyLoc(t), OnMissed: "run-now", ExpiresIn: expiresIn,
		}},
		Metadata: map[string]string{"lens": "security"},
	}
}

func TestRunSlot_shape(t *testing.T) {
	q := calTimer(t, 0)
	slot := time.Date(2027, 6, 10, 2, 0, 0, 0, nyLoc(t))
	now := slot.Add(7 * time.Second)
	evts, err := q.RunSlot(context.Background(), Env{}, Slot{Name: "weekly", Time: slot, Now: now})
	if err != nil || len(evts) != 1 {
		t.Fatalf("evts=%v err=%v", evts, err)
	}
	e := evts[0]
	if e.ID != "calendar:weekly:2027-06-10T06:00:00Z" {
		t.Fatalf("event id (UTC) = %q", e.ID)
	}
	if e.Item.ID != "2027-06-10T02:00:00-04:00" {
		t.Fatalf("Item.ID (trigger zone offset) = %q", e.Item.ID)
	}
	if e.Item.Metadata["scheduled_for"] != "2027-06-10T02:00:00-04:00" || e.Item.Metadata["lens"] != "security" {
		t.Fatalf("metadata = %v", e.Item.Metadata)
	}
	// An on-time tick OMITS on_missed; it does not carry "false".
	if _, has := e.Item.Metadata["on_missed"]; has {
		t.Fatalf("on-time tick must omit on_missed: %v", e.Item.Metadata)
	}
	if e.Item.Type != "timer" || e.Item.Title != "timer tick" {
		t.Fatalf("type/title = %q/%q", e.Item.Type, e.Item.Title)
	}
	if at := e.Attributes["at"].(time.Time); !at.Equal(slot) {
		t.Fatalf("at = %v", at)
	}
	// Default expiry is the next slot.
	if exp := e.Attributes["expiresAt"].(time.Time); !exp.Equal(slot.AddDate(0, 0, 1)) {
		t.Fatalf("expiresAt = %v, want the next slot", exp)
	}
}

func TestRunSlot_catchUpAndExpiresIn(t *testing.T) {
	q := calTimer(t, 6*time.Hour)
	slot := time.Date(2027, 6, 10, 2, 0, 0, 0, nyLoc(t))
	now := slot.Add(3 * time.Hour)
	evts, err := q.RunSlot(context.Background(), Env{}, Slot{Name: "weekly", Time: slot, OnMissed: true, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	e := evts[0]
	if e.Item.Metadata["on_missed"] != "true" {
		t.Fatalf("catch-up must carry on_missed=true: %v", e.Item.Metadata)
	}
	if exp := e.Attributes["expiresAt"].(time.Time); !exp.Equal(now.Add(6 * time.Hour)) {
		t.Fatalf("expiresAt = %v, want now + expires_in", exp)
	}
}

func TestRunSlot_metadataNotAliased(t *testing.T) {
	q := calTimer(t, 0)
	slot := time.Date(2027, 6, 10, 2, 0, 0, 0, nyLoc(t))
	evts, _ := q.RunSlot(context.Background(), Env{}, Slot{Name: "w", Time: slot, Now: slot})
	evts[0].Item.Metadata["lens"] = "mutated"
	if q.Metadata["lens"] != "security" {
		t.Fatal("tick metadata aliased the query's own map")
	}
}

// The calendar smoke tick (Run, slot = now) carries scheduled_for, at and
// expiresAt.
func TestRun_calendarSmokeTick(t *testing.T) {
	q := calTimer(t, 0)
	before := time.Now()
	evts, err := q.Run(context.Background(), Env{})
	if err != nil || len(evts) != 1 {
		t.Fatalf("evts=%v err=%v", evts, err)
	}
	e := evts[0]
	if _, ok := e.Item.Metadata["scheduled_for"].(string); !ok {
		t.Fatalf("smoke tick lacks scheduled_for: %v", e.Item.Metadata)
	}
	at, ok := e.Attributes["at"].(time.Time)
	if !ok || at.Before(before.Add(-2*time.Second)) {
		t.Fatalf("at = %v", e.Attributes["at"])
	}
	exp, ok := e.Attributes["expiresAt"].(time.Time)
	if !ok || !exp.After(at) {
		t.Fatalf("expiresAt = %v", e.Attributes["expiresAt"])
	}
	if e.Item.Metadata["lens"] != "security" {
		t.Fatalf("declared metadata missing: %v", e.Item.Metadata)
	}
}

func TestRunSlot_requiresCalendarTrigger(t *testing.T) {
	q := TimerQuery{Meta: Meta{EmitTypes: []string{"x"}}}
	if _, err := q.RunSlot(context.Background(), Env{}, Slot{}); err == nil {
		t.Fatal("RunSlot on a period timer must error")
	}
}
