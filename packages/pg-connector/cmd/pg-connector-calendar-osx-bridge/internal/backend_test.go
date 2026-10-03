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
// ListAttention: time-based ramp (bead pg2-pf1rb; INV-CAL-2..INV-CAL-6)
// ----------------------------------------------------------------------

// rampNow is the fixed clock every ramp test uses.
var rampNow = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

// attentionBackend builds a Backend with the fixed rampNow clock over one
// calendar "Cal" (id cal-1) holding evs, and returns it with a context
// carrying cfg (its Calendars defaulted to [{Name: "Cal"}] when empty).
func attentionBackend(t *testing.T, cfg backendConfig, evs ...apiEvent) (*Backend, *fakeTransport, context.Context) {
	t.Helper()
	ft := &fakeTransport{
		calendars: []apiCalendar{{ID: "cal-1", Title: "Cal"}},
		events:    map[string][]apiEvent{"cal-1": evs},
	}
	b := New(ft)
	b.now = func() time.Time { return rampNow }
	if len(cfg.Calendars) == 0 {
		cfg.Calendars = []calendarConfig{{Name: "Cal"}}
	}
	return b, ft, ctxWithConfig(t, cfg)
}

func attnEvent(id string, start, end time.Time) apiEvent {
	return apiEvent{ID: id, Title: id, Start: start, End: end, CalendarID: "cal-1", SelfStatus: "accepted"}
}

func severityByID(t *testing.T, b *Backend, ctx context.Context) map[string]schema.Severity {
	t.Helper()
	items, err := b.ListAttention(ctx)
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	out := map[string]schema.Severity{}
	for _, it := range items {
		out[it.ID] = it.Severity
	}
	return out
}

// TestBackend_ListAttention_TierBoundaries pins every tier boundary at the
// fixed clock with the default 15m lead time and 24h window.
func TestBackend_ListAttention_TierBoundaries(t *testing.T) {
	const lead = 15 * time.Minute
	hour := time.Hour
	cases := []struct {
		name string
		ev   apiEvent
		want schema.Severity // "" == absent
	}{
		{"lead+1s before start -> low", attnEvent("e", rampNow.Add(lead+time.Second), rampNow.Add(hour)), schema.SeverityLow},
		{"exactly lead before start -> high", attnEvent("e", rampNow.Add(lead), rampNow.Add(hour)), schema.SeverityHigh},
		{"1s before start -> high", attnEvent("e", rampNow.Add(time.Second), rampNow.Add(hour)), schema.SeverityHigh},
		{"start == now -> critical", attnEvent("e", rampNow, rampNow.Add(hour)), schema.SeverityCritical},
		{"started earlier, still running -> critical", attnEvent("e", rampNow.Add(-30*time.Minute), rampNow.Add(time.Second)), schema.SeverityCritical},
		{"end == now -> absent", attnEvent("e", rampNow.Add(-hour), rampNow), ""},
		{"ended 1s ago -> absent", attnEvent("e", rampNow.Add(-hour), rampNow.Add(-time.Second)), ""},
		{"last second inside window -> low", attnEvent("e", rampNow.Add(24*time.Hour-time.Second), rampNow.Add(26*time.Hour)), schema.SeverityLow},
		{"start == window end -> absent", attnEvent("e", rampNow.Add(24*time.Hour), rampNow.Add(26*time.Hour)), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, _, ctx := attentionBackend(t, backendConfig{}, tc.ev)
			got, present := severityByID(t, b, ctx)["e"]
			if tc.want == "" {
				if present {
					t.Fatalf("event present at %q, want absent", got)
				}
				return
			}
			if !present || got != tc.want {
				t.Fatalf("severity = %q (present=%v), want %q", got, present, tc.want)
			}
		})
	}
}

// TestBackend_ListAttention_RampIsMonotoneOverTime walks one event across
// the whole ramp for every modifier combination and requires severity to
// be non-decreasing as time passes (INV-CAL-4: modifiers never invert it).
func TestBackend_ListAttention_RampIsMonotoneOverTime(t *testing.T) {
	start := rampNow
	end := start.Add(time.Hour)
	for _, prio := range []string{"", "low", "medium", "high"} {
		for _, important := range []bool{false, true} {
			for _, status := range []string{"accepted", "tentative"} {
				ev := attnEvent("e", start, end)
				ev.SelfStatus = status
				if important {
					ev.Attendees = []apiAttendee{{Email: "boss@example.com"}}
				}
				cfg := backendConfig{Calendars: []calendarConfig{{Name: "Cal", Priority: prio}}, ImportantPeople: []string{"boss@example.com"}}
				b, _, ctx := attentionBackend(t, cfg, ev)
				prev := -1
				for _, off := range []time.Duration{-3 * time.Hour, -time.Hour, -16 * time.Minute, -15 * time.Minute, -time.Second, 0, 30 * time.Minute, time.Hour - time.Second} {
					clock := start.Add(off)
					b.now = func() time.Time { return clock }
					sev, present := severityByID(t, b, ctx)["e"]
					if !present {
						t.Fatalf("prio=%q important=%v status=%s off=%v: absent", prio, important, status, off)
					}
					if r := sev.Rank(); r < prev {
						t.Fatalf("prio=%q important=%v status=%s off=%v: severity %q fell below previous rank %d", prio, important, status, off, sev, prev)
					} else {
						prev = r
					}
				}
			}
		}
	}
}

// CAL-RAMP-6 design: each modifier shifts the time tier by exactly one
// level (capped); fixtures use the "soon" tier (base high) for the
// downward shift and the "far" tier (base low) for the upward shift so the
// clamp at the enum's ends does not hide the effect.
func TestBackend_ListAttention_CalendarPriorityShiftsTier(t *testing.T) {
	far := attnEvent("ev", rampNow.Add(time.Hour), rampNow.Add(2*time.Hour))
	b, _, ctx := attentionBackend(t, backendConfig{Calendars: []calendarConfig{{Name: "Cal", Priority: "high"}}}, far)
	if got := severityByID(t, b, ctx)["ev"]; got != schema.SeverityMedium {
		t.Errorf("far + high priority = %q, want medium (low shifted up one)", got)
	}
	b, _, ctx = attentionBackend(t, backendConfig{Calendars: []calendarConfig{{Name: "Cal", Priority: "low"}}}, far)
	if got := severityByID(t, b, ctx)["ev"]; got != schema.SeverityLow {
		t.Errorf("far + low priority = %q, want low", got)
	}
}

func TestBackend_ListAttention_ImportantPeopleShiftsTier(t *testing.T) {
	matched := attnEvent("matched", rampNow.Add(time.Hour), rampNow.Add(2*time.Hour))
	matched.Attendees = []apiAttendee{{Name: "Boss", Email: "boss@example.com"}}
	unmatched := attnEvent("unmatched", rampNow.Add(time.Hour), rampNow.Add(2*time.Hour))
	unmatched.Attendees = []apiAttendee{{Name: "Nobody", Email: "nobody@example.com"}}
	b, _, ctx := attentionBackend(t, backendConfig{ImportantPeople: []string{"BOSS@example.com"}}, matched, unmatched)
	sev := severityByID(t, b, ctx)
	if sev["matched"] != schema.SeverityMedium || sev["unmatched"] != schema.SeverityLow {
		t.Fatalf("matched=%q unmatched=%q, want medium/low", sev["matched"], sev["unmatched"])
	}
}

func TestBackend_ListAttention_TentativeShiftsTierDown(t *testing.T) {
	tent := attnEvent("tent", rampNow.Add(10*time.Minute), rampNow.Add(time.Hour))
	tent.SelfStatus = "tentative"
	conf := attnEvent("conf", rampNow.Add(10*time.Minute), rampNow.Add(time.Hour))
	running := attnEvent("running", rampNow.Add(-time.Minute), rampNow.Add(time.Hour))
	running.SelfStatus = "tentative"
	yes := true
	b, _, ctx := attentionBackend(t, backendConfig{Calendars: []calendarConfig{{Name: "Cal", IncludeTentative: &yes}}}, tent, conf, running)
	sev := severityByID(t, b, ctx)
	if sev["tent"] != schema.SeverityMedium || sev["conf"] != schema.SeverityHigh {
		t.Errorf("tent=%q conf=%q, want medium/high", sev["tent"], sev["conf"])
	}
	if sev["running"] != schema.SeverityHigh {
		t.Errorf("tentative in-progress = %q, want high (critical shifted down one)", sev["running"])
	}
}

func TestBackend_ListAttention_ModifierShiftIsCappedAtOneLevel(t *testing.T) {
	// high priority AND an important attendee: +2 uncapped, +1 capped.
	soon := attnEvent("soon", rampNow.Add(5*time.Minute), rampNow.Add(time.Hour))
	soon.Attendees = []apiAttendee{{Email: "boss@example.com"}}
	far := attnEvent("far", rampNow.Add(time.Hour), rampNow.Add(2*time.Hour))
	far.Attendees = []apiAttendee{{Email: "boss@example.com"}}
	cfg := backendConfig{Calendars: []calendarConfig{{Name: "Cal", Priority: "high"}}, ImportantPeople: []string{"boss@example.com"}}
	b, _, ctx := attentionBackend(t, cfg, soon, far)
	sev := severityByID(t, b, ctx)
	if sev["soon"] != schema.SeverityCritical {
		t.Errorf("soon + two boosts = %q, want critical (high +1, capped)", sev["soon"])
	}
	if sev["far"] != schema.SeverityMedium {
		t.Errorf("far + two boosts = %q, want medium (low +1, capped)", sev["far"])
	}
}

// CAL-RAMP-5 design: all-day events are reported at low, never ramped and
// never raised by a modifier, for the whole day.
func TestBackend_ListAttention_AllDayStaysLow(t *testing.T) {
	dayStart := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	running := attnEvent("allday-running", dayStart, dayStart.Add(24*time.Hour-time.Second))
	running.AllDay = true
	running.Attendees = []apiAttendee{{Email: "boss@example.com"}}
	tomorrow := attnEvent("allday-tomorrow", dayStart.Add(24*time.Hour), dayStart.Add(48*time.Hour-time.Second))
	tomorrow.AllDay = true
	ended := attnEvent("allday-ended", dayStart.Add(-24*time.Hour), dayStart.Add(-time.Second))
	ended.AllDay = true
	cfg := backendConfig{Calendars: []calendarConfig{{Name: "Cal", Priority: "high"}}, ImportantPeople: []string{"boss@example.com"}}
	b, _, ctx := attentionBackend(t, cfg, running, tomorrow, ended)
	sev := severityByID(t, b, ctx)
	if sev["allday-running"] != schema.SeverityLow || sev["allday-tomorrow"] != schema.SeverityLow {
		t.Errorf("all-day severities = %+v, want low", sev)
	}
	if _, ok := sev["allday-ended"]; ok {
		t.Errorf("ended all-day event present")
	}
}

// CAL-RAMP-7: a declined event is never raised, in any tier.
func TestBackend_ListAttention_DeclinedIsOmitted(t *testing.T) {
	var evs []apiEvent
	for id, start := range map[string]time.Time{
		"far": rampNow.Add(time.Hour), "soon": rampNow.Add(time.Minute), "running": rampNow.Add(-time.Minute),
	} {
		e := attnEvent(id, start, start.Add(time.Hour))
		e.SelfStatus = "Declined"
		evs = append(evs, e)
	}
	b, _, ctx := attentionBackend(t, backendConfig{}, evs...)
	if sev := severityByID(t, b, ctx); len(sev) != 0 {
		t.Fatalf("declined events reported: %+v", sev)
	}
}

// CAL-RAMP-4: lead time and window are per-backend config keys.
func TestBackend_ListAttention_ConfigurableLeadAndWindow(t *testing.T) {
	e30 := attnEvent("in30m", rampNow.Add(30*time.Minute), rampNow.Add(time.Hour))
	e3h := attnEvent("in3h", rampNow.Add(3*time.Hour), rampNow.Add(4*time.Hour))
	b, ft, ctx := attentionBackend(t, backendConfig{AttentionLeadTime: "30m", AttentionWindow: "2h"}, e30, e3h)
	sev := severityByID(t, b, ctx)
	if sev["in30m"] != schema.SeverityHigh {
		t.Errorf("in30m with lead 30m = %q, want high", sev["in30m"])
	}
	if _, ok := sev["in3h"]; ok {
		t.Errorf("in3h reported beyond a 2h window")
	}
	// The query end follows attention_window.
	if got := ft.eventsCalls[0].End; !got.Equal(rampNow.Add(2 * time.Hour)) {
		t.Errorf("query end = %v, want now+2h", got)
	}
}

func TestBackend_ListAttention_InvalidTimingConfig(t *testing.T) {
	for name, cfg := range map[string]backendConfig{
		"malformed lead":   {AttentionLeadTime: "soon"},
		"negative lead":    {AttentionLeadTime: "-5m"},
		"malformed window": {AttentionWindow: "forever"},
		"zero window":      {AttentionWindow: "0s"},
	} {
		t.Run(name, func(t *testing.T) {
			b, _, ctx := attentionBackend(t, cfg)
			if _, err := b.ListAttention(ctx); !errors.Is(err, scriptout.ErrInvalidArgument) {
				t.Fatalf("err = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

// A zero lead time disables the "soon" tier entirely (only an event
// starting at exactly now is not "far"; it is already in progress).
func TestBackend_ListAttention_ZeroLeadDisablesSoonTier(t *testing.T) {
	b, _, ctx := attentionBackend(t, backendConfig{AttentionLeadTime: "0s"}, attnEvent("e", rampNow.Add(time.Second), rampNow.Add(time.Hour)))
	if got := severityByID(t, b, ctx)["e"]; got != schema.SeverityLow {
		t.Fatalf("severity = %q, want low", got)
	}
}

// Verify-don't-assume: the events query is widened into the past so an
// in-progress event is returned even by a non-overlapping range predicate,
// and a long-ended event returned by the widened range is filtered out.
func TestBackend_ListAttention_QueryLooksBackAndFiltersEnded(t *testing.T) {
	ended := attnEvent("ended", rampNow.Add(-3*time.Hour), rampNow.Add(-2*time.Hour))
	running := attnEvent("running", rampNow.Add(-2*time.Hour), rampNow.Add(time.Hour))
	b, ft, ctx := attentionBackend(t, backendConfig{}, ended, running)
	sev := severityByID(t, b, ctx)
	if len(sev) != 1 || sev["running"] != schema.SeverityCritical {
		t.Fatalf("severities = %+v, want only running=critical", sev)
	}
	q := ft.eventsCalls[0]
	if !q.Start.Equal(rampNow.Add(-attentionLookback)) || !q.End.Equal(rampNow.Add(defaultAttentionWindow)) {
		t.Errorf("query = [%v, %v), want [now-lookback, now+24h)", q.Start, q.End)
	}
}

// Regression (bead pg2-vmhgr): occurrences of one recurring series share
// an event ID, so the dedup key MUST distinguish occurrences — yesterday's
// ended occurrence must not shadow tomorrow's upcoming one.
func TestBackend_ListAttention_RecurringOccurrencesNotCollapsedByID(t *testing.T) {
	yesterday := attnEvent("daily", rampNow.Add(-24*time.Hour), rampNow.Add(-23*time.Hour))
	tomorrow := attnEvent("daily", rampNow.Add(20*time.Hour), rampNow.Add(21*time.Hour)) // inside the default 24h window
	b, _, ctx := attentionBackend(t, backendConfig{}, yesterday, tomorrow)
	items, err := b.ListAttention(ctx)
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if len(items) != 1 || items[0].ID != "daily" || items[0].Severity != schema.SeverityLow {
		t.Fatalf("items = %+v, want exactly the upcoming occurrence of daily graded low", items)
	}
}

func TestBackend_ListEvents_RecurringOccurrencesNotCollapsedByID(t *testing.T) {
	yesterday := attnEvent("daily", rampNow.Add(-24*time.Hour), rampNow.Add(-23*time.Hour))
	tomorrow := attnEvent("daily", rampNow.Add(24*time.Hour), rampNow.Add(25*time.Hour))
	b, _, ctx := attentionBackend(t, backendConfig{}, yesterday, tomorrow)
	res, err := b.ListEvents(ctx, rampNow.Add(-48*time.Hour), rampNow.Add(48*time.Hour), "")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(res.Entities) != 2 {
		t.Fatalf("len(Entities) = %d, want 2 distinct occurrences of daily: %+v", len(res.Entities), res.Entities)
	}
	starts := map[string]bool{}
	for _, e := range res.Entities {
		starts[e.Start] = true
	}
	if !starts[rfc3339(yesterday.Start)] || !starts[rfc3339(tomorrow.Start)] {
		t.Errorf("starts = %v, want both yesterday's and tomorrow's occurrence", starts)
	}
}

// The cross-calendar tie-break still applies per occurrence: the SAME
// occurrence under two calendars collapses to the higher-priority
// calendar's copy, while a different occurrence of the same id survives.
func TestBackend_ListEvents_CrossCalendarTieBreakPerOccurrence(t *testing.T) {
	d1 := attnEvent("daily", rampNow.Add(-24*time.Hour), rampNow.Add(-23*time.Hour))
	d2 := attnEvent("daily", rampNow.Add(24*time.Hour), rampNow.Add(25*time.Hour))
	ft := &fakeTransport{
		calendars: []apiCalendar{{ID: "cal-w", Title: "Work"}, {ID: "cal-p", Title: "Personal"}},
		events: map[string][]apiEvent{
			"cal-p": {d1, d2},
			"cal-w": {d2},
		},
	}
	b := New(ft)
	cfg := backendConfig{Calendars: []calendarConfig{
		{Name: "Personal", Priority: "low"},
		{Name: "Work", Priority: "high"},
	}}
	res, err := b.ListEvents(ctxWithConfig(t, cfg), rampNow.Add(-48*time.Hour), rampNow.Add(48*time.Hour), "")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(res.Entities) != 2 {
		t.Fatalf("len(Entities) = %d, want 2 (d2 deduped across calendars, d1 kept): %+v", len(res.Entities), res.Entities)
	}
	for _, e := range res.Entities {
		want := "low"
		if e.Start == rfc3339(d2.Start) {
			want = "high"
		}
		if e.CalendarPriority != want {
			t.Errorf("occurrence at %s CalendarPriority = %q, want %q", e.Start, e.CalendarPriority, want)
		}
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
