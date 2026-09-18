package calendarapi

import (
	"encoding/json"
	"fmt"

	"github.com/phillipgreenii/osx-bridge-api/internal/wire"
)

// SchemaVersion is this service's own current schema version (stamped onto
// every wire.Response the socketserver routes to this service), independent
// of wire.ProtocolVersion — see this package's and internal/wire's doc
// comments.
const SchemaVersion = 1

// ServiceName is the wire.Request.Service value this service answers to.
const ServiceName = "calendar"

// Op names this service implements.
const (
	OpCalendars = "calendars"
	OpEvents    = "events"
)

// calendarsResult is the "calendars" op's result payload.
type calendarsResult struct {
	Calendars []Calendar `json:"calendars"`
}

// eventsResult is the "events" op's result payload.
type eventsResult struct {
	Events []Event `json:"events"`
}

// Service implements socketserver.Service for the calendar service,
// delegating actual calendar access to a Provider.
type Service struct {
	Provider Provider
}

// New returns a calendar Service backed by p.
func New(p Provider) *Service {
	return &Service{Provider: p}
}

// SchemaVersion implements socketserver.Service.
func (s *Service) SchemaVersion() int { return SchemaVersion }

// Handle implements socketserver.Service, dispatching op to this service's
// own handlers.
func (s *Service) Handle(op string, args json.RawMessage) (json.RawMessage, error) {
	switch op {
	case OpCalendars:
		return s.handleCalendars()
	case OpEvents:
		return s.handleEvents(args)
	default:
		return nil, wire.WrapError(wire.ErrUnknownOp, fmt.Sprintf("calendar: unknown op %q", op))
	}
}

func (s *Service) handleCalendars() (json.RawMessage, error) {
	cals, err := s.Provider.Calendars()
	if err != nil {
		// Passed through unchanged: a Provider MAY do its own wire.Err*
		// classification (e.g. wire.ErrUnavailable when EventKit access was
		// never granted); an unclassified error falls back to unavailable
		// via wire.ErrorResponse's own codeForError, so no re-wrapping is
		// needed here.
		return nil, err
	}
	return json.Marshal(calendarsResult{Calendars: cals})
}

func (s *Service) handleEvents(args json.RawMessage) (json.RawMessage, error) {
	var q EventsQuery
	if err := wire.Decode(args, &q); err != nil {
		return nil, wire.WrapError(wire.ErrInvalidArgument, fmt.Sprintf("calendar: invalid events args: %v", err))
	}
	if q.Start.IsZero() {
		return nil, wire.WrapError(wire.ErrInvalidArgument, "calendar: events requires a non-zero start")
	}
	if q.End.IsZero() {
		return nil, wire.WrapError(wire.ErrInvalidArgument, "calendar: events requires a non-zero end")
	}
	if q.End.Before(q.Start) {
		return nil, wire.WrapError(wire.ErrInvalidArgument, "calendar: end must not be before start")
	}

	events, err := s.Provider.Events(q)
	if err != nil {
		return nil, err
	}
	// Design ("Calendar service"): "The SAME underlying event can appear
	// under multiple calendars... Any aggregation across multiple
	// configured calendars MUST dedupe by event ID." Applied unconditionally
	// (not just when multiple CalendarIDs were requested): an unfiltered
	// Events() call already spans every calendar EventKit sees, so the same
	// duplication can occur with no filter at all.
	return json.Marshal(eventsResult{Events: dedupeByID(events)})
}

// dedupeByID returns events with duplicate IDs collapsed to their first
// occurrence, preserving the original relative order of the first
// occurrence of each ID.
func dedupeByID(events []Event) []Event {
	seen := make(map[string]bool, len(events))
	out := make([]Event, 0, len(events))
	for _, e := range events {
		if seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		out = append(out, e)
	}
	return out
}
