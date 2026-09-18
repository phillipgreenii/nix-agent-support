package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCalendarEvent_JSONRoundTrip mirrors TestThread_JSONRoundTrip: every
// field round-trips through JSON with its documented wire name.
func TestCalendarEvent_JSONRoundTrip(t *testing.T) {
	in := CalendarEvent{
		ID:            "event-1",
		Title:         "Weekly sync",
		Start:         "2026-09-18T15:00:00Z",
		End:           "2026-09-18T15:30:00Z",
		AllDay:        false,
		CalendarID:    "cal-1",
		Calendar:      "Work",
		Location:      "Zoom",
		Notes:         "bring notes",
		ConferenceURL: "https://example.invalid/room/1",
		Attendees: []CalendarAttendee{
			{Name: "Alice", Email: "alice@example.invalid", Status: "accepted"},
		},
		SelfStatus:       "accepted",
		Recurring:        true,
		IsDetached:       false,
		CalendarPriority: "high",
		AsOf:             "2026-09-18T00:00:00Z",
		Stale:            false,
	}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out CalendarEvent
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if out.ID != in.ID || out.Title != in.Title || out.Start != in.Start || out.End != in.End ||
		out.AllDay != in.AllDay || out.CalendarID != in.CalendarID || out.Calendar != in.Calendar ||
		out.Location != in.Location || out.Notes != in.Notes || out.ConferenceURL != in.ConferenceURL ||
		out.SelfStatus != in.SelfStatus || out.Recurring != in.Recurring || out.IsDetached != in.IsDetached ||
		out.CalendarPriority != in.CalendarPriority || out.AsOf != in.AsOf || out.Stale != in.Stale {
		t.Fatalf("round-trip mismatch (scalars): got %+v, want %+v", out, in)
	}
	if len(out.Attendees) != len(in.Attendees) {
		t.Fatalf("attendees round-trip mismatch: got %+v, want %+v", out.Attendees, in.Attendees)
	}
	for i := range in.Attendees {
		if out.Attendees[i] != in.Attendees[i] {
			t.Fatalf("attendees round-trip mismatch: got %+v, want %+v", out.Attendees, in.Attendees)
		}
	}
}

// TestCalendarEvent_OptionalFieldsOmittedWhenEmpty mirrors
// TestThread_OptionalFieldsOmittedWhenEmpty: only id/title/start/end/
// calendar_id/as_of/stale always appear; every other field is omitempty.
func TestCalendarEvent_OptionalFieldsOmittedWhenEmpty(t *testing.T) {
	raw, err := json.Marshal(CalendarEvent{
		ID:         "event-1",
		Title:      "Weekly sync",
		Start:      "2026-09-18T15:00:00Z",
		End:        "2026-09-18T15:30:00Z",
		CalendarID: "cal-1",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"id":"event-1","title":"Weekly sync","start":"2026-09-18T15:00:00Z","end":"2026-09-18T15:30:00Z","calendar_id":"cal-1","as_of":"","stale":false}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
	for _, key := range []string{
		"all_day", "calendar", "location", "notes", "conference_url", "attendees",
		"self_status", "recurring", "is_detached", "calendar_priority",
	} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("expected %q omitted when empty, got %s", key, raw)
		}
	}
}

// TestCalendarEvent_AsOfAndStale_AlwaysPresentInJSON mirrors
// TestThread_AsOfAndStale_AlwaysPresentInJSON: as_of/stale must always be
// present, since Stale=false is itself informative.
func TestCalendarEvent_AsOfAndStale_AlwaysPresentInJSON(t *testing.T) {
	raw, err := json.Marshal(CalendarEvent{ID: "event-1", AsOf: "2026-09-18T00:00:00Z", Stale: false})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := out["as_of"]; !ok {
		t.Fatalf("as_of missing from %s", raw)
	}
	staleVal, ok := out["stale"]
	if !ok {
		t.Fatalf("stale missing from %s", raw)
	}
	if staleVal != false {
		t.Fatalf("stale = %v, want false", staleVal)
	}
}

// TestCalendarEvent_IDIsString mirrors TestThread_IDIsString: a
// compile-time assertion that CalendarEvent.ID is string-typed.
func TestCalendarEvent_IDIsString(t *testing.T) {
	var _ string = CalendarEvent{}.ID //nolint:staticcheck // QF1011: explicit type IS the assertion; omitting it would infer from the field and defeat the check.
}

// TestCalendarEvent_CalendarPriority_RoundTripsEndToEnd is this packet's own
// load-bearing assertion (flagged by the 2026-09-17 review as previously
// missing its own bullet): calendar_priority — this docket's own static
// classification tag, with no counterpart on calendarapi.Event at all —
// MUST round-trip end to end onto the wire schema, since it is the
// load-bearing mechanism for the "no dynamic filtering, static tags only"
// architectural boundary.
func TestCalendarEvent_CalendarPriority_RoundTripsEndToEnd(t *testing.T) {
	in := CalendarEvent{ID: "event-1", CalendarPriority: "high"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"calendar_priority":"high"`) {
		t.Fatalf("calendar_priority missing from wire shape: %s", raw)
	}
	var out CalendarEvent
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.CalendarPriority != "high" {
		t.Fatalf("calendar_priority = %q, want %q", out.CalendarPriority, "high")
	}
}

// TestCalendarListResult_TruncatedUnconditionallyFalse_WireShape locks in
// the binding decision this packet's own Contract states: unlike
// ThreadListResult's "always true," a calendar "list"/"list_events" reply's
// wire shape carries truncated:false — calendarapi.Provider.Events has no
// pagination/limit concept at all, so a calendar backend can always
// confirm it returned the complete match set. This is a schema-level shape
// lock only; the backend-level "always set Truncated false" behavior is a
// concrete Provider implementation's own concern (packet 2).
func TestCalendarListResult_TruncatedUnconditionallyFalse_WireShape(t *testing.T) {
	raw, err := json.Marshal(CalendarListResult{
		Entities:   []CalendarEvent{{ID: "event-1"}},
		PresentIDs: []string{"event-1"},
		Cursor:     nil,
		Truncated:  false,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["truncated"] != false {
		t.Fatalf("truncated = %v, want false", out["truncated"])
	}
	if out["cursor"] != nil {
		t.Fatalf("cursor = %v, want null (this capability has no incremental-listing backend)", out["cursor"])
	}
}
