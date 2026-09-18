package internal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/search"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeTransport is a stubbed Transport double — no live socket, no live
// EventKit [design: acceptance criterion 1] — keyed by calendar ID so a
// test can script per-configured-calendar event lists independently.
type fakeTransport struct {
	calendars    []apiCalendar
	calendarsErr error
	events       map[string][]apiEvent
	eventsErr    error
	eventsCalls  []apiEventsQuery
}

func (f *fakeTransport) Calendars(context.Context) ([]apiCalendar, error) {
	if f.calendarsErr != nil {
		return nil, f.calendarsErr
	}
	return f.calendars, nil
}

func (f *fakeTransport) Events(_ context.Context, q apiEventsQuery) ([]apiEvent, error) {
	f.eventsCalls = append(f.eventsCalls, q)
	if f.eventsErr != nil {
		return nil, f.eventsErr
	}
	if len(q.CalendarIDs) != 1 {
		return nil, errors.New("fakeTransport: expected exactly one calendar id per Events call")
	}
	return f.events[q.CalendarIDs[0]], nil
}

var _ Transport = (*fakeTransport)(nil)

// ctxWithConfig builds a context carrying cfg as the request's own opaque
// config block, via scriptout.WithConfig — the same mechanism serveLoop
// uses on every real request.
func ctxWithConfig(t *testing.T, cfg any) context.Context {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return scriptout.WithConfig(context.Background(), raw)
}

func rfc3339(t time.Time) string { return t.Format(time.RFC3339) }

// ----------------------------------------------------------------------
// Compile-time / type-check wiring
// ----------------------------------------------------------------------

func TestBackend_ImplementsOptionalCapabilities(t *testing.T) {
	var _ attention.Provider = New(&fakeTransport{})
	var _ search.Provider = New(&fakeTransport{})
}

// ----------------------------------------------------------------------
// Translation + dedup + calendar_priority round-trip
// ----------------------------------------------------------------------

func TestBackend_ListEvents_TranslatesAndDedupesAcrossCalendars(t *testing.T) {
	start := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	ft := &fakeTransport{
		calendars: []apiCalendar{
			{ID: "cal-work", Title: "Work"},
			{ID: "cal-personal", Title: "Personal"},
		},
		events: map[string][]apiEvent{
			// ev-1 appears under BOTH configured calendars (the exact
			// live-observed duplication this docket's design calls out),
			// with different priority tiers configured for each.
			"cal-work": {
				{
					ID: "ev-1", Title: "Standup", Start: start, End: end, CalendarID: "cal-work",
					Attendees: []apiAttendee{{Name: "Alice", Email: "alice@example.com"}},
				},
			},
			"cal-personal": {
				{ID: "ev-1", Title: "Standup", Start: start, End: end, CalendarID: "cal-personal"},
				{ID: "ev-2", Title: "Dentist", Start: start, End: end, CalendarID: "cal-personal"},
			},
		},
	}
	b := New(ft)
	cfg := backendConfig{Calendars: []calendarConfig{
		{Name: "Work", Priority: "high"},
		{Name: "Personal", Priority: "low"},
	}}
	ctx := ctxWithConfig(t, cfg)

	res, err := b.ListEvents(ctx, start, end, "")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(res.Entities) != 2 {
		t.Fatalf("len(Entities) = %d, want 2 (ev-1 deduped, ev-2 present): %+v", len(res.Entities), res.Entities)
	}
	byID := map[string]schema.CalendarEvent{}
	for _, e := range res.Entities {
		byID[e.ID] = e
	}
	ev1, ok := byID["ev-1"]
	if !ok {
		t.Fatalf("ev-1 missing: %+v", res.Entities)
	}
	// The Work occurrence (priority "high") must win the tie-break over
	// Personal's (priority "low") — see this package's dedup Binding
	// decision.
	if ev1.CalendarPriority != "high" {
		t.Errorf("ev-1 CalendarPriority = %q, want %q (higher-priority calendar should win the dedup tie-break)", ev1.CalendarPriority, "high")
	}
	if ev1.Title != "Standup" || ev1.Start != rfc3339(start) || ev1.End != rfc3339(end) {
		t.Errorf("ev-1 translation mismatch: %+v", ev1)
	}
	if len(ev1.Attendees) != 1 || ev1.Attendees[0].Email != "alice@example.com" {
		t.Errorf("ev-1 Attendees not translated: %+v", ev1.Attendees)
	}
	if ev1.AsOf == "" || ev1.Stale {
		t.Errorf("ev-1 AsOf/Stale = %q/%v, want a populated AsOf and Stale=false", ev1.AsOf, ev1.Stale)
	}

	ev2, ok := byID["ev-2"]
	if !ok {
		t.Fatalf("ev-2 missing: %+v", res.Entities)
	}
	if ev2.CalendarPriority != "low" {
		t.Errorf("ev-2 CalendarPriority = %q, want %q", ev2.CalendarPriority, "low")
	}

	if len(res.PresentIDs) != 2 {
		t.Errorf("PresentIDs = %v, want 2 entries", res.PresentIDs)
	}
	if res.Truncated {
		t.Error("Truncated = true, want false (unconditional per this capability's own binding decision)")
	}
}

func TestBackend_ListEvents_IDsOnly_OmitsEntities(t *testing.T) {
	start := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	ft := &fakeTransport{
		calendars: []apiCalendar{{ID: "cal-work", Title: "Work"}},
		events: map[string][]apiEvent{
			"cal-work": {{ID: "ev-1", Title: "Standup", Start: start, End: end, CalendarID: "cal-work"}},
		},
	}
	b := New(ft)
	cfg := backendConfig{Calendars: []calendarConfig{{Name: "Work"}}}
	ctx := ctxWithConfig(t, cfg)

	res, err := b.List(ctx, schema.QueryExpr{"1h"}, true)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if res.Entities != nil {
		t.Errorf("Entities = %+v, want nil when idsOnly=true", res.Entities)
	}
	if len(res.PresentIDs) != 1 || res.PresentIDs[0] != "ev-1" {
		t.Errorf("PresentIDs = %v", res.PresentIDs)
	}
}

// ----------------------------------------------------------------------
// Graceful degradation on an unresolved configured calendar name
// ----------------------------------------------------------------------

func TestBackend_UnresolvedCalendarName_DegradesGracefully(t *testing.T) {
	start := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	ft := &fakeTransport{
		calendars: []apiCalendar{{ID: "cal-work", Title: "Work"}},
		events: map[string][]apiEvent{
			"cal-work": {{ID: "ev-1", Title: "Standup", Start: start, End: end, CalendarID: "cal-work"}},
		},
	}
	b := New(ft)
	cfg := backendConfig{Calendars: []calendarConfig{
		{Name: "Work", Priority: "high"},
		{Name: "Ghost Calendar", Priority: "low"}, // no matching daemon Calendar.Title
	}}
	ctx := ctxWithConfig(t, cfg)

	res, err := b.ListEvents(ctx, start, end, "")
	if err != nil {
		t.Fatalf("ListEvents must not fail the whole call for an unresolvable configured calendar: %v", err)
	}
	if len(res.Entities) != 1 || res.Entities[0].ID != "ev-1" {
		t.Fatalf("Entities = %+v, want exactly ev-1 from the resolvable calendar", res.Entities)
	}
}

// ----------------------------------------------------------------------
// List's query-expression handling
// ----------------------------------------------------------------------

func TestBackend_List_PicksLargestDurationAmongElements(t *testing.T) {
	ft := &fakeTransport{
		calendars: []apiCalendar{{ID: "cal-work", Title: "Work"}},
		events:    map[string][]apiEvent{},
	}
	b := New(ft)
	cfg := backendConfig{Calendars: []calendarConfig{{Name: "Work"}}}
	ctx := ctxWithConfig(t, cfg)

	if _, err := b.List(ctx, schema.QueryExpr{"1h", "72h", "30m"}, false); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ft.eventsCalls) != 1 {
		t.Fatalf("len(eventsCalls) = %d, want 1", len(ft.eventsCalls))
	}
	got := ft.eventsCalls[0].End.Sub(ft.eventsCalls[0].Start)
	if got < 71*time.Hour || got > 73*time.Hour {
		t.Fatalf("resolved window = %v, want ~72h (the largest element)", got)
	}
}

func TestBackend_List_MalformedElement_InvalidArgument(t *testing.T) {
	b := New(&fakeTransport{})
	ctx := ctxWithConfig(t, backendConfig{})

	_, err := b.List(ctx, schema.QueryExpr{"not-a-duration"}, false)
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestBackend_List_NoElements_InvalidArgument(t *testing.T) {
	b := New(&fakeTransport{})
	ctx := ctxWithConfig(t, backendConfig{})

	_, err := b.List(ctx, schema.QueryExpr{}, false)
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

// ----------------------------------------------------------------------
// include_tentative
// ----------------------------------------------------------------------

func TestBackend_ListEvents_ExcludesTentativeWhenConfiguredFalse(t *testing.T) {
	start := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	ft := &fakeTransport{
		calendars: []apiCalendar{{ID: "cal-work", Title: "Work"}},
		events: map[string][]apiEvent{
			"cal-work": {
				{ID: "ev-tentative", Title: "Maybe", Start: start, End: end, CalendarID: "cal-work", SelfStatus: "tentative"},
				{ID: "ev-confirmed", Title: "Definitely", Start: start, End: end, CalendarID: "cal-work", SelfStatus: "accepted"},
			},
		},
	}
	no := false
	b := New(ft)
	cfg := backendConfig{Calendars: []calendarConfig{{Name: "Work", IncludeTentative: &no}}}
	ctx := ctxWithConfig(t, cfg)

	res, err := b.ListEvents(ctx, start, end, "")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(res.Entities) != 1 || res.Entities[0].ID != "ev-confirmed" {
		t.Fatalf("Entities = %+v, want only ev-confirmed", res.Entities)
	}
}

func TestBackend_ListEvents_IncludesTentativeByDefault(t *testing.T) {
	start := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	ft := &fakeTransport{
		calendars: []apiCalendar{{ID: "cal-work", Title: "Work"}},
		events: map[string][]apiEvent{
			"cal-work": {{ID: "ev-tentative", Title: "Maybe", Start: start, End: end, CalendarID: "cal-work", SelfStatus: "tentative"}},
		},
	}
	b := New(ft)
	// No include_tentative key at all — unset means "include" (see
	// calendarConfig's doc comment).
	cfg := backendConfig{Calendars: []calendarConfig{{Name: "Work"}}}
	ctx := ctxWithConfig(t, cfg)

	res, err := b.ListEvents(ctx, start, end, "")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(res.Entities) != 1 || res.Entities[0].ID != "ev-tentative" {
		t.Fatalf("Entities = %+v, want ev-tentative included by default", res.Entities)
	}
}

// ----------------------------------------------------------------------
// ListAttention: three independent severity-input fixture pairs
// ----------------------------------------------------------------------

func TestBackend_ListAttention_CalendarPriorityChangesSeverity(t *testing.T) {
	now := time.Now().UTC()
	ft := &fakeTransport{
		calendars: []apiCalendar{{ID: "cal-high", Title: "High"}, {ID: "cal-low", Title: "Low"}},
		events: map[string][]apiEvent{
			"cal-high": {{ID: "ev-high", Title: "A", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), CalendarID: "cal-high", SelfStatus: "accepted"}},
			"cal-low":  {{ID: "ev-low", Title: "B", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), CalendarID: "cal-low", SelfStatus: "accepted"}},
		},
	}
	b := New(ft)
	cfg := backendConfig{Calendars: []calendarConfig{
		{Name: "High", Priority: "high"},
		{Name: "Low", Priority: "low"},
	}}
	ctx := ctxWithConfig(t, cfg)

	items, err := b.ListAttention(ctx)
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	sev := map[string]schema.Severity{}
	for _, it := range items {
		sev[it.ID] = it.Severity
	}
	if sev["ev-high"] == sev["ev-low"] {
		t.Fatalf("calendar_priority did not change severity: high=%q low=%q", sev["ev-high"], sev["ev-low"])
	}
	if sev["ev-high"].Rank() <= sev["ev-low"].Rank() {
		t.Errorf("ev-high severity (%q) should outrank ev-low's (%q)", sev["ev-high"], sev["ev-low"])
	}
}

func TestBackend_ListAttention_ImportantPeopleMatchChangesSeverity(t *testing.T) {
	now := time.Now().UTC()
	ft := &fakeTransport{
		calendars: []apiCalendar{{ID: "cal-low", Title: "Low"}},
		events: map[string][]apiEvent{
			"cal-low": {
				{
					ID: "ev-matched", Title: "A", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), CalendarID: "cal-low", SelfStatus: "accepted",
					Attendees: []apiAttendee{{Name: "Boss", Email: "boss@example.com"}},
				},
				{
					ID: "ev-unmatched", Title: "B", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), CalendarID: "cal-low", SelfStatus: "accepted",
					Attendees: []apiAttendee{{Name: "Nobody", Email: "nobody@example.com"}},
				},
			},
		},
	}
	b := New(ft)
	cfg := backendConfig{
		Calendars:       []calendarConfig{{Name: "Low", Priority: "low"}},
		ImportantPeople: []string{"boss@example.com"},
	}
	ctx := ctxWithConfig(t, cfg)

	items, err := b.ListAttention(ctx)
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	sev := map[string]schema.Severity{}
	for _, it := range items {
		sev[it.ID] = it.Severity
	}
	if sev["ev-matched"] == sev["ev-unmatched"] {
		t.Fatalf("important_people match did not change severity: matched=%q unmatched=%q", sev["ev-matched"], sev["ev-unmatched"])
	}
	if sev["ev-matched"].Rank() <= sev["ev-unmatched"].Rank() {
		t.Errorf("ev-matched severity (%q) should outrank ev-unmatched's (%q)", sev["ev-matched"], sev["ev-unmatched"])
	}
}

func TestBackend_ListAttention_SelfStatusTentativeChangesSeverity(t *testing.T) {
	now := time.Now().UTC()
	yes := true
	ft := &fakeTransport{
		calendars: []apiCalendar{{ID: "cal-medium", Title: "Medium"}},
		events: map[string][]apiEvent{
			"cal-medium": {
				{ID: "ev-tentative", Title: "A", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), CalendarID: "cal-medium", SelfStatus: "tentative"},
				{ID: "ev-confirmed", Title: "B", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), CalendarID: "cal-medium", SelfStatus: "accepted"},
			},
		},
	}
	b := New(ft)
	cfg := backendConfig{Calendars: []calendarConfig{{Name: "Medium", Priority: "medium", IncludeTentative: &yes}}}
	ctx := ctxWithConfig(t, cfg)

	items, err := b.ListAttention(ctx)
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	sev := map[string]schema.Severity{}
	for _, it := range items {
		sev[it.ID] = it.Severity
	}
	if sev["ev-tentative"] == sev["ev-confirmed"] {
		t.Fatalf("SelfStatus tentative did not change severity: tentative=%q confirmed=%q", sev["ev-tentative"], sev["ev-confirmed"])
	}
	if sev["ev-tentative"].Rank() >= sev["ev-confirmed"].Rank() {
		t.Errorf("ev-tentative severity (%q) should rank BELOW ev-confirmed's (%q)", sev["ev-tentative"], sev["ev-confirmed"])
	}
}

// ----------------------------------------------------------------------
// Search
// ----------------------------------------------------------------------

func TestBackend_Search_ReturnsResults(t *testing.T) {
	now := time.Now().UTC()
	ft := &fakeTransport{
		calendars: []apiCalendar{{ID: "cal-work", Title: "Work"}},
		events: map[string][]apiEvent{
			"cal-work": {{ID: "ev-1", Title: "Budget review", Start: now, End: now.Add(time.Hour), CalendarID: "cal-work"}},
		},
	}
	b := New(ft)
	cfg := backendConfig{Calendars: []calendarConfig{{Name: "Work"}}}
	ctx := ctxWithConfig(t, cfg)

	results, err := b.Search(ctx, "budget", nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].ID != "ev-1" || results[0].Title != "Budget review" || results[0].Source != searchSourceName {
		t.Fatalf("results = %+v", results)
	}
	if len(ft.eventsCalls) != 1 || ft.eventsCalls[0].Search != "budget" {
		t.Fatalf("eventsCalls = %+v, want Search=%q", ft.eventsCalls, "budget")
	}
}

func TestBackend_Search_EmptyQuery_InvalidArgument(t *testing.T) {
	b := New(&fakeTransport{})
	ctx := ctxWithConfig(t, backendConfig{})
	_, err := b.Search(ctx, "   ", nil)
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

// ----------------------------------------------------------------------
// Config decode / important_people matching (INV-CAL-1) unit coverage
// ----------------------------------------------------------------------

func TestImportantPersonMatches_EmailCaseInsensitiveNoDomainNormalization(t *testing.T) {
	a := apiAttendee{Email: "Alice@Example.com"}
	if !importantPersonMatches("alice@example.com", a) {
		t.Error("expected case-insensitive email match")
	}
	if importantPersonMatches("alice@sub.example.com", a) {
		t.Error("expected no match across different domains")
	}
}

func TestImportantPersonMatches_NameCaseInsensitiveExact(t *testing.T) {
	a := apiAttendee{Name: "Bob Jones"}
	if !importantPersonMatches("bob jones", a) {
		t.Error("expected case-insensitive exact name match")
	}
	if importantPersonMatches("bob", a) {
		t.Error("expected no substring/fuzzy match")
	}
}

func TestDecodeBackendConfig_MalformedJSON_InvalidArgument(t *testing.T) {
	_, err := decodeBackendConfig(json.RawMessage(`{not json`))
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestDecodeBackendConfig_Empty_ZeroValue(t *testing.T) {
	cfg, err := decodeBackendConfig(nil)
	if err != nil {
		t.Fatalf("decodeBackendConfig(nil): %v", err)
	}
	if len(cfg.Calendars) != 0 || len(cfg.ImportantPeople) != 0 {
		t.Fatalf("cfg = %+v, want zero value", cfg)
	}
}
