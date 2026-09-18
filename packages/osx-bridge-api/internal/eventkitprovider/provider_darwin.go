//go:build darwin

// Package eventkitprovider is the REAL calendar provider: a thin adapter
// from go-eventkit's calendar package (cgo + Objective-C bindings to
// EventKit — design: "Confirmed API surface... NOT Swift-exclusive") onto
// calendarapi.Provider. It is deliberately build-tag-gated to darwin only
// — EventKit exists nowhere else, and go-eventkit's own darwin
// implementation (bridge_darwin.go) is gated the same way, assuming cgo is
// available whenever darwin is — so every other package in this module
// (calendarapi, socketserver, wire) stays importable, testable, and
// buildable on any platform/toolchain, and AC #1's unit tests never need
// this package or a live EventKit session at all. provider_other.go is
// this file's non-darwin counterpart, mirroring go-eventkit's own
// bridge_darwin.go/bridge_other.go split.
//
// AC #2-4 (a real TCC consent prompt firing, matching Calendar.app's own
// data, surviving a clean rebuild + launchctl kickstart) are exactly the
// behaviors this file wires up but cannot itself prove: they require a
// human clicking "Allow" on a real macOS dialog and comparing results
// against Calendar.app, which is why this bead's own acceptance
// criteria route that verification to a human, not to this package's own
// (nonexistent, by construction) unit tests.
package eventkitprovider

import (
	"fmt"

	"github.com/BRO3886/go-eventkit/calendar"

	"github.com/phillipgreenii/osx-bridge-api/internal/calendarapi"
	"github.com/phillipgreenii/osx-bridge-api/internal/wire"
)

// Provider adapts a go-eventkit calendar.Client to calendarapi.Provider.
type Provider struct {
	client *calendar.Client
}

// New creates the EventKit-backed Provider. On first call, macOS presents
// the TCC consent prompt for kTCCServiceCalendar (design: "a process
// launched directly by launchd... is its OWN responsible identity and is
// NOT subject to" the terminal-hosted block) — the live, human-observed
// half of AC #2 this daemon exists to make possible.
func New() (*Provider, error) {
	client, err := calendar.New()
	if err != nil {
		if err == calendar.ErrAccessDenied {
			return nil, wire.WrapError(wire.ErrUnauthenticated, "calendar access denied via macOS TCC prompt")
		}
		return nil, wire.WrapError(wire.ErrUnavailable, fmt.Sprintf("eventkit: %v", err))
	}
	return &Provider{client: client}, nil
}

// Calendars implements calendarapi.Provider.
func (p *Provider) Calendars() ([]calendarapi.Calendar, error) {
	cals, err := p.client.Calendars()
	if err != nil {
		return nil, wire.WrapError(wire.ErrUnavailable, fmt.Sprintf("eventkit: calendars: %v", err))
	}
	out := make([]calendarapi.Calendar, 0, len(cals))
	for _, c := range cals {
		out = append(out, calendarapi.Calendar{
			ID:       c.ID,
			Title:    c.Title,
			Type:     c.Type.String(),
			Color:    c.Color,
			Source:   c.Source,
			ReadOnly: c.ReadOnly,
		})
	}
	return out, nil
}

// Events implements calendarapi.Provider. go-eventkit's WithCalendarID
// filters by exactly one calendar ID per call (there is no plural-ID
// ListOption — design: "ListOptions: WithCalendar(name), WithCalendars
// (names), WithCalendarID(id), WithSearch(query)"), so a request naming
// more than one CalendarID issues one Events() call per ID and merges the
// results; calendarapi.Service.Handle dedupes the merged (and any
// unfiltered) result by event ID afterward.
func (p *Provider) Events(q calendarapi.EventsQuery) ([]calendarapi.Event, error) {
	baseOpts := make([]calendar.ListOption, 0, 1)
	if q.Search != "" {
		baseOpts = append(baseOpts, calendar.WithSearch(q.Search))
	}

	idFilters := q.CalendarIDs
	if len(idFilters) == 0 {
		idFilters = []string{""} // one unfiltered call
	}

	var out []calendarapi.Event
	for _, id := range idFilters {
		opts := baseOpts
		if id != "" {
			opts = append(opts, calendar.WithCalendarID(id))
		}
		events, err := p.client.Events(q.Start, q.End, opts...)
		if err != nil {
			return nil, wire.WrapError(wire.ErrUnavailable, fmt.Sprintf("eventkit: events: %v", err))
		}
		for _, e := range events {
			out = append(out, toAPIEvent(e))
		}
	}
	return out, nil
}

func toAPIEvent(e calendar.Event) calendarapi.Event {
	attendees := make([]calendarapi.Attendee, 0, len(e.Attendees))
	for _, a := range e.Attendees {
		attendees = append(attendees, calendarapi.Attendee{
			Name:   a.Name,
			Email:  a.Email,
			Status: a.Status.String(),
		})
	}
	return calendarapi.Event{
		ID:            e.ID,
		Title:         e.Title,
		Start:         e.StartDate,
		End:           e.EndDate,
		AllDay:        e.AllDay,
		CalendarID:    e.CalendarID,
		Calendar:      e.Calendar,
		Location:      e.Location,
		Notes:         e.Notes,
		ConferenceURL: e.ConferenceURL,
		Attendees:     attendees,
		SelfStatus:    e.SelfStatus.String(),
		Recurring:     e.Recurring,
		IsDetached:    e.IsDetached,
	}
}
