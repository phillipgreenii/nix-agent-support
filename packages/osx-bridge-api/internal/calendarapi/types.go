// Package calendarapi is osx-bridge-api's calendar service (design:
// "Calendar service (v1; only service required for this bead)"). It
// exposes calendar data in go-eventkit's own natural vocabulary (design:
// "Architecture" → "Data shape": "osx-bridge-api exposes each service's
// OWN natural vocabulary... For calendar, this mirrors go-eventkit's own
// Event/Attendee/Calendar structs directly"), decoupled from go-eventkit's
// actual Go types so this package — and its Provider interface — can be
// satisfied by either the real EventKit-backed provider
// (internal/eventkitprovider, cgo/darwin-only) or a fake/stub provider in
// tests, without either side importing go-eventkit.
//
// v1 scope is deliberately narrow, matching the two go-eventkit calls the
// design calls out as this bead's own requirement: Client.Calendars() and
// Client.Events() (the "primary time-range query"). Everything else in
// go-eventkit's confirmed API surface (single-event lookup, the write ops,
// WatchChanges) is explicitly out of v1 scope per the design ("v1 can be
// served by polling Events() on a timer... worth keeping in mind as a
// later enhancement") and is left for a later bead to add alongside its
// own real need, rather than speculatively stubbed here.
package calendarapi

import "time"

// Calendar mirrors go-eventkit calendar.Calendar's field set.
type Calendar struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Type     string `json:"type"`
	Color    string `json:"color,omitempty"`
	Source   string `json:"source,omitempty"`
	ReadOnly bool   `json:"readOnly"`
}

// Attendee mirrors go-eventkit calendar.Attendee's field set. Per the
// design ("Confirmed API surface"), attendees are read-only from EventKit —
// there is no write path for this field, in v1 or otherwise.
type Attendee struct {
	Name   string `json:"name,omitempty"`
	Email  string `json:"email,omitempty"`
	Status string `json:"status,omitempty"`
}

// Event mirrors go-eventkit calendar.Event's field set, restricted to the
// fields v1's read-only calendar/events ops need. Deliberately excluded,
// per the design: an Attachments field of any kind ("Event has NO
// Attachments field of any kind... Calendar attachments are out of scope
// for this daemon, full stop — not deferred, not 'later,' because there is
// no supported API to ever add it").
type Event struct {
	ID            string     `json:"id"`
	Title         string     `json:"title"`
	Start         time.Time  `json:"start"`
	End           time.Time  `json:"end"`
	AllDay        bool       `json:"allDay,omitempty"`
	CalendarID    string     `json:"calendarId"`
	Calendar      string     `json:"calendar,omitempty"`
	Location      string     `json:"location,omitempty"`
	Notes         string     `json:"notes,omitempty"`
	ConferenceURL string     `json:"conferenceUrl,omitempty"`
	Attendees     []Attendee `json:"attendees,omitempty"`
	// SelfStatus is the current user's own RSVP status (design: "the
	// correct field for 'have I confirmed this event' ... distinct from and
	// cleaner than scanning Attendees[] for your own email").
	SelfStatus string `json:"selfStatus,omitempty"`
	Recurring  bool   `json:"recurring,omitempty"`
	// IsDetached marks an individually-detached occurrence of a recurring
	// event (design: event IDs carry a "/RID=<recurrenceID> suffix for
	// individually-detached occurrences").
	IsDetached bool `json:"isDetached,omitempty"`
}

// EventsQuery is the "events" op's argument shape: a time range plus
// optional filters, mirroring go-eventkit's own ListOption set
// (WithCalendarID, WithSearch) minus WithCalendar(name)/WithCalendars(names)
// — v1 filters by calendar ID, the stable identifier, never by the
// display name a user can rename at any time.
type EventsQuery struct {
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	CalendarIDs []string  `json:"calendarIds,omitempty"`
	Search      string    `json:"search,omitempty"`
}

// Provider is what a calendar backend implements: the real EventKit-backed
// provider (internal/eventkitprovider) in production, or a fake/stub
// provider in tests (AC #1 — "against a FAKE/stub calendar provider" —
// EventKit/TCC cannot be exercised in an automated CI runner on this kind
// of personal-account machine).
type Provider interface {
	// Calendars returns every calendar EventKit currently sees.
	Calendars() ([]Calendar, error)
	// Events returns events matching q. The SAME underlying event can be
	// returned once per calendar it appears under (design: "observed live:
	// 'JVM Working Group' appeared once under phillipg@ziprecruiter.com and
	// once under ZipRecruiter Guilds, same event ID"); Provider
	// implementations are NOT required to dedupe — Service.Handle does
	// that once, centrally, after merging every Provider call it makes for
	// one request (design: "Any aggregation across multiple configured
	// calendars MUST dedupe by event ID").
	Events(q EventsQuery) ([]Event, error)
}
