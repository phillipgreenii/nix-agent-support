package calendarapi

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/osx-bridge-api/internal/wire"
)

// fakeProvider is the FAKE/stub calendar provider AC #1 requires ("EventKit/
// TCC cannot be exercised in an automated CI runner on this kind of
// personal-account machine"). It implements Provider entirely in memory.
type fakeProvider struct {
	calendars    []Calendar
	events       []Event
	calendarsErr error
	eventsErr    error

	lastEventsQuery EventsQuery
}

func (f *fakeProvider) Calendars() ([]Calendar, error) {
	if f.calendarsErr != nil {
		return nil, f.calendarsErr
	}
	return f.calendars, nil
}

func (f *fakeProvider) Events(q EventsQuery) ([]Event, error) {
	f.lastEventsQuery = q
	if f.eventsErr != nil {
		return nil, f.eventsErr
	}
	return f.events, nil
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return tm
}

func TestHandleCalendars(t *testing.T) {
	fp := &fakeProvider{
		calendars: []Calendar{
			{ID: "cal-1", Title: "phillipg@ziprecruiter.com", Type: "caldav", Source: "Google", ReadOnly: false},
			{ID: "cal-2", Title: "Holidays in United States", Type: "subscription", Source: "Google", ReadOnly: true},
		},
	}
	svc := New(fp)

	raw, err := svc.Handle(OpCalendars, nil)
	if err != nil {
		t.Fatalf("Handle(calendars) returned error: %v", err)
	}

	var got calendarsResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(got.Calendars) != 2 {
		t.Fatalf("got %d calendars, want 2", len(got.Calendars))
	}
	if got.Calendars[0].ID != "cal-1" || got.Calendars[1].ID != "cal-2" {
		t.Errorf("calendars not returned in provider order: %+v", got.Calendars)
	}
	if !got.Calendars[1].ReadOnly {
		t.Errorf("cal-2 ReadOnly not preserved through the envelope")
	}
}

func TestHandleCalendarsProviderError(t *testing.T) {
	fp := &fakeProvider{calendarsErr: wire.WrapError(wire.ErrUnavailable, "eventkit: not authorized")}
	svc := New(fp)

	_, err := svc.Handle(OpCalendars, nil)
	if err == nil {
		t.Fatal("Handle(calendars) returned no error for a failing provider")
	}
	if !errors.Is(err, wire.ErrUnavailable) {
		t.Errorf("error does not classify as wire.ErrUnavailable: %v", err)
	}
}

func TestHandleEvents(t *testing.T) {
	start := mustTime(t, "2026-09-17T00:00:00Z")
	end := mustTime(t, "2026-09-24T00:00:00Z")

	fp := &fakeProvider{
		events: []Event{
			{ID: "evt-1", Title: "FinDev Standup", Start: start, End: start.Add(30 * time.Minute), CalendarID: "cal-1"},
		},
	}
	svc := New(fp)

	args, _ := json.Marshal(EventsQuery{Start: start, End: end, CalendarIDs: []string{"cal-1"}, Search: "standup"})
	raw, err := svc.Handle(OpEvents, args)
	if err != nil {
		t.Fatalf("Handle(events) returned error: %v", err)
	}

	// The query the Provider actually received round-tripped through the
	// wire envelope correctly (JSON envelope, request half).
	if !fp.lastEventsQuery.Start.Equal(start) || !fp.lastEventsQuery.End.Equal(end) {
		t.Errorf("provider did not receive the requested time range: got %+v", fp.lastEventsQuery)
	}
	if fp.lastEventsQuery.Search != "standup" {
		t.Errorf("provider did not receive the search filter: got %q", fp.lastEventsQuery.Search)
	}

	var got eventsResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(got.Events) != 1 || got.Events[0].ID != "evt-1" {
		t.Errorf("unexpected events result: %+v", got.Events)
	}
}

// TestHandleEventsDedupesByID proves the design's dedupe-by-event-ID
// invariant ("The SAME underlying event can appear under multiple
// calendars... Any aggregation across multiple configured calendars MUST
// dedupe by event ID") — the exact "JVM Working Group" scenario observed
// live, reproduced here against the fake provider.
func TestHandleEventsDedupesByID(t *testing.T) {
	start := mustTime(t, "2026-09-17T00:00:00Z")
	end := mustTime(t, "2026-09-24T00:00:00Z")

	fp := &fakeProvider{
		events: []Event{
			{ID: "evt-jvm", Title: "JVM Working Group", CalendarID: "cal-personal", Start: start, End: start.Add(time.Hour)},
			{ID: "evt-jvm", Title: "JVM Working Group", CalendarID: "cal-guilds", Start: start, End: start.Add(time.Hour)},
			{ID: "evt-other", Title: "Something else", CalendarID: "cal-personal", Start: start, End: start.Add(time.Hour)},
		},
	}
	svc := New(fp)

	args, _ := json.Marshal(EventsQuery{Start: start, End: end})
	raw, err := svc.Handle(OpEvents, args)
	if err != nil {
		t.Fatalf("Handle(events) returned error: %v", err)
	}

	var got eventsResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(got.Events) != 2 {
		t.Fatalf("got %d events, want 2 (duplicate evt-jvm collapsed): %+v", len(got.Events), got.Events)
	}
	ids := map[string]bool{}
	for _, e := range got.Events {
		if ids[e.ID] {
			t.Errorf("event ID %q appeared more than once in deduped result", e.ID)
		}
		ids[e.ID] = true
	}
	if !ids["evt-jvm"] || !ids["evt-other"] {
		t.Errorf("deduped result missing an expected event ID: %+v", got.Events)
	}
}

func TestHandleEventsProviderError(t *testing.T) {
	start := mustTime(t, "2026-09-17T00:00:00Z")
	end := mustTime(t, "2026-09-24T00:00:00Z")
	fp := &fakeProvider{eventsErr: wire.WrapError(wire.ErrUnavailable, "eventkit: no access")}
	svc := New(fp)

	args, _ := json.Marshal(EventsQuery{Start: start, End: end})
	_, err := svc.Handle(OpEvents, args)
	if !errors.Is(err, wire.ErrUnavailable) {
		t.Errorf("error does not classify as wire.ErrUnavailable: %v", err)
	}
}

func TestHandleEventsRejectsMissingStartEnd(t *testing.T) {
	svc := New(&fakeProvider{})

	cases := []struct {
		name string
		args string
	}{
		{"no args", `{}`},
		{"zero start", `{"end":"2026-09-24T00:00:00Z"}`},
		{"zero end", `{"start":"2026-09-17T00:00:00Z"}`},
		{"end before start", `{"start":"2026-09-24T00:00:00Z","end":"2026-09-17T00:00:00Z"}`},
		{"malformed json", `{not json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Handle(OpEvents, json.RawMessage(tc.args))
			if err == nil {
				t.Fatalf("Handle(events) with %s returned no error", tc.name)
			}
			if !errors.Is(err, wire.ErrInvalidArgument) {
				t.Errorf("error does not classify as wire.ErrInvalidArgument: %v", err)
			}
		})
	}
}

func TestHandleUnknownOp(t *testing.T) {
	svc := New(&fakeProvider{})

	_, err := svc.Handle("not-a-real-op", nil)
	if !errors.Is(err, wire.ErrUnknownOp) {
		t.Errorf("error does not classify as wire.ErrUnknownOp: %v", err)
	}
}

func TestSchemaVersion(t *testing.T) {
	svc := New(&fakeProvider{})
	if got := svc.SchemaVersion(); got != SchemaVersion {
		t.Errorf("SchemaVersion() = %d, want %d", got, SchemaVersion)
	}
}
