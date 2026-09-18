// calendar.go: the calendar entity/capability's shared JSON wire shape,
// built by this docket's ("pg-connector-calendar-osx-bridge: calendar
// Tier-2 backend", pg2-o2dmu) first code-producing packet, using
// pkg/schema/thread.go's own field/doc-comment conventions as its
// structural precedent — this is a NEW entity-type capability, not a new
// backend on an existing one.
//
// schema.CalendarEvent is an INDEPENDENTLY-defined type: it is not a
// re-export or wrapper of osx-bridge-api's internal/calendarapi.Event
// (packages/osx-bridge-api is a separate Go module, and everything under
// it is internal/-scoped) — its field set is read for FIELD-SHAPE
// REFERENCE only. It carries every fact calendarapi.Event has that has a
// calendar-domain meaning, plus a NEW calendar_priority field this docket
// adds that has no counterpart on calendarapi.Event at all: this backend's
// own static classification tag (never a dynamic, work-context-aware
// relevance judgment — that stays the consuming layer's job).
package schema

// CalendarSchemaVersion is the calendar capability's own schema version,
// populated into the wire envelope's schemaVersion field by the calendar
// capability's dispatch-table entries
// (pkg/provider/calendar.NewDispatchTable) — independent of every other
// capability's own schema version (each entity type/capability versions
// its own schema separately, INV-VER-1) and of
// pkg/scriptout.ProtocolVersion. This is the calendar capability's first
// version: it has no prior shape to have bumped from. It is also
// registered into CurrentSchemaVersions in versions.go, so a future
// calendar-capability schema-version mismatch is detectable rather than
// silently skipped by cmd/pg-connector/config_validate.go's
// checkSchemaVersions — mirrors ThreadSchemaVersion's identical precedent.
const CalendarSchemaVersion = 1

// CalendarAttendee mirrors osx-bridge-api's internal/calendarapi.Attendee
// field set (field-shape reference only — see this file's own package doc
// comment for why this is an independently-defined type, not a re-export).
// Attendees are read-only from EventKit; there is no write path for this
// field in this capability, in this round or otherwise.
type CalendarAttendee struct {
	Name   string `json:"name,omitempty"`
	Email  string `json:"email,omitempty"`
	Status string `json:"status,omitempty"`
}

// CalendarEvent is the calendar capability's shared JSON wire shape,
// returned by "list_events" and one element of "list"'s
// CalendarListResult.Entities. The field set carries over every fact
// calendarapi.Event [landed: pg2-p9ap3] has that has a calendar-domain
// meaning, plus this capability's own calendar_priority field (see below)
// and the standard AsOf/Stale pair every existing schema type carries
// (mirrors schema.Thread's own AsOf/Stale doc comment).
type CalendarEvent struct {
	// ID is the event's identity, in whatever form the backend's own
	// system uses (calendarapi.Event.ID carries a "/RID=<recurrenceID>"
	// suffix for an individually-detached occurrence of a recurring
	// event) — a string, per every other capability's own "ID is a
	// string" convention (Issue.ID's doc comment).
	ID string `json:"id"`

	// Title is the event's own title/summary.
	Title string `json:"title"`

	// Start and End are the event's own start/end times (RFC3339, in
	// whatever timezone the backend's own system reports them). Always
	// populated for an event — mirrors this capability's own
	// list_events wire-envelope args keys (start/end), which are also
	// RFC3339 strings.
	Start string `json:"start"`
	End   string `json:"end"`

	// AllDay reports whether this is an all-day event.
	AllDay bool `json:"all_day,omitempty"`

	// CalendarID is the stable, backend-native identifier of the calendar
	// this event belongs to — never a display name a user can rename at
	// any time (mirrors calendarapi.EventsQuery's own CalendarIDs
	// filtering-by-id convention).
	CalendarID string `json:"calendar_id"`

	// Calendar is the calendar's own display name, as reported by the
	// backend's own system. Empty when the backend does not supply one.
	Calendar string `json:"calendar,omitempty"`

	Location      string `json:"location,omitempty"`
	Notes         string `json:"notes,omitempty"`
	ConferenceURL string `json:"conference_url,omitempty"`

	// Attendees lists every attendee the backend's own system reports for
	// this event. Empty when the backend does not supply this or the
	// event has no attendees.
	//
	// calendarapi.Event [landed: pg2-p9ap3] carries no separate Organizer
	// field — only this Attendees list, with no "is-organizer" marker at
	// all — so "organizer matching" against a configured important_people
	// list (INV-CAL-1, packages/pg-connector/docs/behavior/invariants.md)
	// necessarily degrades to attendee-list matching only, unless and
	// until osx-bridge-api's own wire shape gains a distinct Organizer
	// field. A concrete Provider implementation MUST NOT paper over this:
	// it is a real, verified constraint of the underlying system, not an
	// oversight in this schema.
	Attendees []CalendarAttendee `json:"attendees,omitempty"`

	// SelfStatus is the configured account's own RSVP status for this
	// event (mirrors calendarapi.Event.SelfStatus's own doc comment: "the
	// correct field for 'have I confirmed this event' ... distinct from
	// and cleaner than scanning Attendees[] for your own email").
	SelfStatus string `json:"self_status,omitempty"`

	// Recurring reports whether this event is part of a recurring series.
	Recurring bool `json:"recurring,omitempty"`

	// IsDetached marks an individually-detached occurrence of a recurring
	// event (mirrors calendarapi.Event.IsDetached's own doc comment).
	IsDetached bool `json:"is_detached,omitempty"`

	// CalendarPriority is this docket's own static-classification field —
	// e.g. "high"/"low", read from this backend's own per-calendar config
	// (this docket's design: "calendar priority tier must be a field in
	// the wire schema itself, e.g. calendar_priority, not just an internal
	// input to this backend's own attention math"). It has no counterpart
	// on calendarapi.Event at all — a Provider populates it from its own
	// configured per-calendar priority tier, never from any fact EventKit
	// itself reports. Empty when the backend has no configured priority
	// tier for this event's own calendar.
	CalendarPriority string `json:"calendar_priority,omitempty"`

	// AsOf is this read's own as-of time (RFC3339, UTC), mirroring
	// Issue.AsOf/Thread.AsOf's identical INV-ASOF-1 contract: "every
	// acted-on read seam MUST carry its own as-of time." Empty only when
	// this backend has no usable as-of time for this read, which MUST
	// pair with Stale true rather than a plausible-looking but
	// meaningless timestamp.
	AsOf string `json:"as_of"`

	// Stale is this backend's own as-of/stale determination for this read
	// (INV-ASOF-2), mirroring every other current backend's identical
	// precedent (Thread.Stale's own doc comment).
	Stale bool `json:"stale"`
}

// CalendarListResult is the "list"/"list_events" ops' shared wire result
// payload for the calendar capability — the same generic shape
// ThreadListResult documents in full (see its own doc comment for
// Cursor/Entities/PresentIDs/ids_only semantics, which apply identically
// here with CalendarEvent in place of Thread), with one binding-decision
// departure: Truncated is UNCONDITIONALLY false for every calendar reply
// (Binding decision, not the design's explicit text) — unlike
// ThreadListResult's own "always true" convention (which exists because
// ITS backend, claude -p, genuinely cannot confirm completeness), this
// capability's own landed dependency, calendarapi.Provider.Events
// [landed: pg2-p9ap3], has no pagination/limit concept at all
// (EventsQuery carries no page/limit field, and the underlying go-eventkit
// Events() call is a direct, complete-range query), so a calendar backend
// can always confirm it returned the complete match set. Cursor is always
// nil: this capability has no incremental-listing backend at this phase
// (mirrors ThreadListResult's own no-cursor-parameter shape).
type CalendarListResult struct {
	Entities   []CalendarEvent `json:"entities"`
	PresentIDs []string        `json:"present_ids"`
	Cursor     *string         `json:"cursor"`
	Truncated  bool            `json:"truncated"`
}
