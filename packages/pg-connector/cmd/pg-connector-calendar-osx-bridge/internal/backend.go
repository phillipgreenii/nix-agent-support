// backend.go: Backend implements pkg/provider/calendar.Provider by
// talking ONLY to osx-bridge-api's local Unix-domain socket via a
// Transport (client.go) — no direct EventKit/go-eventkit import anywhere
// in this package [design: "no direct EventKit/go-eventkit import in this
// package"]. It additionally implements the OPTIONAL cross-cutting
// attention.Provider/search.Provider interfaces, asserted via type-check
// exactly like pg-connector-pr-github/pg-connector-issue-beads/
// pg-connector-issue-jira already do. It implements NO
// pkg/provider.AuthChecker (Binding decision): osx-bridge-api requires no
// credential from its own client — TCC consent is the daemon's own
// concern, already handled [landed: pg2-p9ap3, pg2-tk57n] — mirroring
// pg-connector-thread-slack's identical "resolves no credential of its
// own at all" precedent.
//
// This backend does NOT read a config file of its own at all: like every
// other Tier-2 backend, it receives its own opaque config block per
// request, as raw JSON, via scriptout.ConfigFromContext — see
// decodeBackendConfig below.
//
// Binding decisions this file makes explicit (freedom boundaries the
// design leaves open):
//
//   - Calendar-name-to-ID resolution: this backend calls the "calendars"
//     op once per List/ListEvents/ListAttention/Search call (the
//     "implementer's freedom boundary" between "at least once per process
//     lifetime" and "per call" — this file picks the simpler, always-fresh
//     "per call" option, trading a little chattiness for never serving a
//     stale calendar-ID mapping) and matches each configured name against
//     the daemon's own Calendar.Title, case-sensitively (calendar names are
//     exact display names, unlike important_people's own case-INSENSITIVE
//     matching below — a different invariant governs each). A configured
//     name with no matching Calendar.Title degrades gracefully: it is
//     simply skipped (resolveCalendars below) rather than failing the
//     whole call — CalendarListResult carries no field to report a partial
//     resolution failure on, so this is a silent, best-effort skip
//     [freedom boundary].
//
//   - Dedup tie-break: fetchOccurrences aggregates every resolved
//     calendar's own "events" results and, when the SAME event ID appears
//     under two configured calendars, keeps the occurrence whose
//     configured calendar_priority ranks HIGHEST (priorityWeight below);
//     a tie (equal or both-unrecognized priority values) keeps whichever
//     occurrence was seen FIRST, which — because fetchOccurrences walks
//     resolved calendars in the order they appear in the backend's own
//     config.calendars list — means the EARLIEST-CONFIGURED calendar wins
//     a tie. This is the "highest configured priority wins" rule this
//     packet's own Contract asks to be documented here, since a later
//     reader (and pg-connector-mail-osx-bridge) will need to know it.
//
//   - include_tentative's dual role: a calendar configured
//     include_tentative:false (a) excludes a tentative-SelfStatus event
//     from that calendar's own List/ListEvents/ListAttention results
//     ENTIRELY (fetchOccurrences drops it before dedup even runs), and (b)
//     — moot once (a) excludes it — is also a ListAttention severity input
//     for an event that DOES survive (i.e. one whose own calendar has
//     include_tentative:true and is itself tentative): computeSeverity
//     lowers its score by one tier relative to an otherwise-identical
//     confirmed event.
//
//   - important_people matching implements INV-CAL-1
//     (packages/pg-connector/docs/behavior/invariants.md) exactly:
//     case-insensitive; an "@"-containing entry matches only an attendee's
//     own email (case-insensitive, no domain normalization); a
//     non-"@"-containing entry matches only an attendee's own name
//     (case-insensitive, exact match). "Organizer matching" is attendee-list
//     matching only — apiEvent (mirroring calendarapi.Event [landed:
//     pg2-p9ap3]) carries no separate Organizer field.
//
//   - ListAttention's combining formula (a freedom boundary the design
//     does not specify one exact rule for) is computeSeverity below: an
//     additive score — priorityWeight(cal.Priority) [high=2, medium/
//     normal=1, low/unrecognized/empty=0], +1 when any attendee matches
//     important_people, -1 when the event's own SelfStatus is tentative —
//     mapped to Severity by score (>=3 critical, 2 high, 1 medium, else
//     low). Each of the three named inputs (calendar_priority,
//     important_people match, SelfStatus/include_tentative) demonstrably
//     changes the result in isolation — see backend_test.go's three
//     dedicated fixture pairs.
//
//   - Search has no time-range parameter of its own (search.Provider.Search
//     takes only query/fields), but calendarapi.EventsQuery requires a
//     non-zero [start,end) range [landed: pg2-p9ap3, calendarapi/service.go's
//     own validation]. This backend searches a fixed, generous window —
//     searchWindowPast before now to searchWindowFuture after now — rather
//     than an unbounded one [freedom boundary].
package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/calendar"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/search"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// searchSourceName is this backend's own Search.Source value (mirrors
// cmd/pg-connector-issue-jira/internal.Backend.Search's identical
// "Source: <this binary's own name>" convention).
const searchSourceName = "pg-connector-calendar-osx-bridge"

// searchWindowPast/searchWindowFuture bound Search's own EventsQuery time
// range (see this file's package doc comment) — one year in each
// direction, a pragmatic, generous default with no config knob of its own
// [freedom boundary].
const (
	searchWindowPast   = 365 * 24 * time.Hour
	searchWindowFuture = 365 * 24 * time.Hour
)

// calendarAttentionWindow bounds ListAttention's own event fetch: events
// starting within the next 24h from now — mirrors
// cmd/pg-connector-issue-jira/internal.defaultAttentionThreshold's
// identical "one day's notice" default, with no config knob of its own
// (this packet's own Contract enumerates only calendars/important_people
// as this backend's recognized config keys) [freedom boundary].
const calendarAttentionWindow = 24 * time.Hour

// Backend is pg-connector-calendar-osx-bridge's concrete calendar.Provider
// implementation.
type Backend struct {
	transport Transport
}

// New returns a Backend wrapping the given Transport. Production wiring
// passes NewSocketClient(); tests inject a stub.
func New(t Transport) *Backend {
	return &Backend{transport: t}
}

// Compile-time check that Backend satisfies the calendar capability's
// Provider interface.
var _ calendar.Provider = (*Backend)(nil)

// Compile-time check that Backend also satisfies the attention
// capability's own Provider interface (see this file's package doc
// comment for the combining formula).
var _ attention.Provider = (*Backend)(nil)

// Compile-time check that Backend also satisfies the search capability's
// own Provider interface.
var _ search.Provider = (*Backend)(nil)

// Deliberately NO `var _ provider.AuthChecker = (*Backend)(nil)`: this
// backend resolves no credential of its own at all (see this file's
// package doc comment).

// calendarConfig is one element of this backend's own "calendars" config
// key [design: "Config" section — calendars: [{name, priority,
// include_tentative}]]. IncludeTentative is a pointer so an absent key can
// be distinguished from an explicit `false`: unset means "include" (the
// permissive default), matching every calendar that never mentions the
// key at all continuing to behave as it always did before this key was
// introduced [freedom boundary — the design does not state a default].
type calendarConfig struct {
	Name             string `json:"name"`
	Priority         string `json:"priority,omitempty"`
	IncludeTentative *bool  `json:"include_tentative,omitempty"`
}

// backendConfig is the {"calendars": [...], "important_people": [...]}
// shape this backend decodes from its own opaque per-backend config block
// on every call, via scriptout.ConfigFromContext — never a file read,
// never an env var, never a package-level cache (see this file's package
// doc comment).
type backendConfig struct {
	Calendars       []calendarConfig `json:"calendars,omitempty"`
	ImportantPeople []string         `json:"important_people,omitempty"`
}

// decodeBackendConfig decodes raw (scriptout.ConfigFromContext's return
// value) into a backendConfig. An empty/nil raw (no config block at all)
// decodes to the zero value — no calendars configured, no important
// people configured — rather than an error.
func decodeBackendConfig(raw json.RawMessage) (backendConfig, error) {
	var cfg backendConfig
	if len(raw) == 0 {
		return cfg, nil
	}
	if err := scriptout.Decode(raw, &cfg); err != nil {
		return backendConfig{}, scriptout.WrapError(scriptout.ErrInvalidArgument, "calendar: decode config: "+err.Error())
	}
	return cfg, nil
}

// resolvedCalendar is one of this backend's own configured calendars,
// resolved to the daemon's own stable calendar ID.
type resolvedCalendar struct {
	ID               string
	Name             string
	Priority         string
	IncludeTentative bool
}

// includeTentative resolves calendarConfig.IncludeTentative's own
// pointer-or-unset shape to a plain bool (see calendarConfig's doc
// comment for the unset-means-include default).
func includeTentative(p *bool) bool {
	if p == nil {
		return true
	}
	return *p
}

// resolveCalendars calls the daemon's "calendars" op once and matches
// each of cfg's own configured calendar names against the result's own
// Calendar.Title, case-sensitively. A configured name with no match is
// skipped (see this file's package doc comment's first Binding decision).
// No calendars configured at all short-circuits without ever calling the
// daemon.
func (b *Backend) resolveCalendars(ctx context.Context, cfg backendConfig) ([]resolvedCalendar, error) {
	if len(cfg.Calendars) == 0 {
		return nil, nil
	}
	cals, err := b.transport.Calendars(ctx)
	if err != nil {
		return nil, err
	}
	byTitle := make(map[string]apiCalendar, len(cals))
	for _, c := range cals {
		byTitle[c.Title] = c
	}

	resolved := make([]resolvedCalendar, 0, len(cfg.Calendars))
	for _, cc := range cfg.Calendars {
		cal, ok := byTitle[cc.Name]
		if !ok {
			continue
		}
		resolved = append(resolved, resolvedCalendar{
			ID:               cal.ID,
			Name:             cc.Name,
			Priority:         cc.Priority,
			IncludeTentative: includeTentative(cc.IncludeTentative),
		})
	}
	return resolved, nil
}

// priorityWeight ranks a configured calendar_priority string, used both
// as fetchOccurrences' own dedup tie-break weight and as one of
// computeSeverity's three additive inputs (see this file's package doc
// comment). Recognized tiers, highest first: "high" (2) > "medium"/
// "normal" (1) > "low"/anything else, including empty (0) — case-
// insensitive.
func priorityWeight(priority string) int {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "high":
		return 2
	case "medium", "normal":
		return 1
	default:
		return 0
	}
}

// isTentative reports whether selfStatus (apiEvent.SelfStatus, mirroring
// calendarapi.Event.SelfStatus [landed: pg2-p9ap3]) is the tentative RSVP
// state, case-insensitively.
func isTentative(selfStatus string) bool {
	return strings.EqualFold(strings.TrimSpace(selfStatus), "tentative")
}

// occurrence pairs one fetched apiEvent with the resolvedCalendar it was
// fetched under — the calendar whose own configured priority/
// include_tentative applied when this occurrence was fetched, and (after
// dedup) the calendar whose priority ultimately wins for this event.
type occurrence struct {
	event apiEvent
	cal   resolvedCalendar
}

// fetchOccurrences resolves cfg's own configured calendars (optionally
// narrowed to exactly one by name via calendarFilter — Provider.List's/
// ListEvents'/Search's own "calendar" or "narrow to one" parameter),
// queries each resolved calendar's own "events" op for [start, end) (with
// search, when non-empty, passed straight through as
// apiEventsQuery.Search), drops a tentative occurrence whose own calendar
// is configured include_tentative:false, and dedupes the survivors by
// event ID using the tie-break rule this file's package doc comment
// documents. Returns the deduped occurrences in first-seen (config) order.
func (b *Backend) fetchOccurrences(ctx context.Context, cfg backendConfig, start, end time.Time, calendarFilter, search string) ([]occurrence, error) {
	resolved, err := b.resolveCalendars(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if calendarFilter != "" {
		filtered := make([]resolvedCalendar, 0, len(resolved))
		for _, rc := range resolved {
			if rc.Name == calendarFilter {
				filtered = append(filtered, rc)
			}
		}
		resolved = filtered
	}

	winners := make(map[string]occurrence, len(resolved))
	order := make([]string, 0, len(resolved))
	for _, rc := range resolved {
		events, err := b.transport.Events(ctx, apiEventsQuery{Start: start, End: end, CalendarIDs: []string{rc.ID}, Search: search})
		if err != nil {
			return nil, err
		}
		for _, e := range events {
			if !rc.IncludeTentative && isTentative(e.SelfStatus) {
				continue
			}
			cur, seen := winners[e.ID]
			if !seen {
				winners[e.ID] = occurrence{event: e, cal: rc}
				order = append(order, e.ID)
				continue
			}
			if priorityWeight(rc.Priority) > priorityWeight(cur.cal.Priority) {
				winners[e.ID] = occurrence{event: e, cal: rc}
			}
		}
	}

	out := make([]occurrence, 0, len(order))
	for _, id := range order {
		out = append(out, winners[id])
	}
	return out, nil
}

// toSchemaCalendarEvent maps occ onto the calendar capability's shared
// wire shape. asOf is this call's own completion time — every call fetches
// fresh via the socket with no local cache, so Stale is always false.
func toSchemaCalendarEvent(occ occurrence, asOf string) schema.CalendarEvent {
	e := occ.event
	var attendees []schema.CalendarAttendee
	if len(e.Attendees) > 0 {
		attendees = make([]schema.CalendarAttendee, 0, len(e.Attendees))
		for _, a := range e.Attendees {
			attendees = append(attendees, schema.CalendarAttendee{Name: a.Name, Email: a.Email, Status: a.Status})
		}
	}
	return schema.CalendarEvent{
		ID:               e.ID,
		Title:            e.Title,
		Start:            e.Start.Format(time.RFC3339),
		End:              e.End.Format(time.RFC3339),
		AllDay:           e.AllDay,
		CalendarID:       e.CalendarID,
		Calendar:         e.Calendar,
		Location:         e.Location,
		Notes:            e.Notes,
		ConferenceURL:    e.ConferenceURL,
		Attendees:        attendees,
		SelfStatus:       e.SelfStatus,
		Recurring:        e.Recurring,
		IsDetached:       e.IsDetached,
		CalendarPriority: occ.cal.Priority,
		AsOf:             asOf,
		Stale:            false,
	}
}

// listEvents is the shared core behind List and ListEvents: resolve
// config, fetch+dedupe occurrences over [start, end) (optionally narrowed
// to one calendar), and translate to a schema.CalendarListResult.
// Truncated is unconditionally false — this capability's landed
// dependency has no pagination/limit concept, so a calendar backend can
// always confirm it returned the complete match set (schema.go's own doc
// comment).
func (b *Backend) listEvents(ctx context.Context, start, end time.Time, calendarFilter string, idsOnly bool) (*schema.CalendarListResult, error) {
	cfg, err := decodeBackendConfig(scriptout.ConfigFromContext(ctx))
	if err != nil {
		return nil, err
	}
	occs, err := b.fetchOccurrences(ctx, cfg, start, end, calendarFilter, "")
	if err != nil {
		return nil, err
	}

	asOf := time.Now().UTC().Format(time.RFC3339)
	entities := make([]schema.CalendarEvent, 0, len(occs))
	ids := make([]string, 0, len(occs))
	for _, occ := range occs {
		entities = append(entities, toSchemaCalendarEvent(occ, asOf))
		ids = append(ids, occ.event.ID)
	}
	res := &schema.CalendarListResult{Entities: entities, PresentIDs: ids, Cursor: nil, Truncated: false}
	if idsOnly {
		res.Entities = nil
	}
	return res, nil
}

// largestDuration implements Provider.List's own query-expression
// convention exactly as packages/pg-connector/pkg/provider/calendar/
// iface.go's own doc comment states it: each element MUST be a Go
// time.ParseDuration-parseable duration string; when more than one
// element is given, the LARGEST parsed duration wins; a malformed element
// MUST answer scriptout.ErrInvalidArgument.
func largestDuration(query schema.QueryExpr) (time.Duration, error) {
	var (
		max   time.Duration
		found bool
	)
	for _, el := range query {
		el = strings.TrimSpace(el)
		if el == "" {
			continue
		}
		d, err := time.ParseDuration(el)
		if err != nil {
			return 0, scriptout.WrapError(scriptout.ErrInvalidArgument,
				fmt.Sprintf("calendar: query element %q is not a valid time.ParseDuration string: %v", el, err))
		}
		if !found || d > max {
			max = d
			found = true
		}
	}
	if !found {
		return 0, scriptout.WrapError(scriptout.ErrInvalidArgument, "calendar: query must contain at least one time.ParseDuration element")
	}
	return max, nil
}

// List implements calendar.Provider.List: resolves query to a
// [now, now+largest-duration) window via largestDuration, then delegates
// to the same underlying fetch listEvents uses, querying every configured
// calendar (calendar filter left empty).
func (b *Backend) List(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.CalendarListResult, error) {
	dur, err := largestDuration(query)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return b.listEvents(ctx, now, now.Add(dur), "", idsOnly)
}

// ListEvents implements calendar.Provider.ListEvents.
func (b *Backend) ListEvents(ctx context.Context, start, end time.Time, calendarName string) (*schema.CalendarListResult, error) {
	return b.listEvents(ctx, start, end, calendarName, false)
}

// normalizeImportantPeople trims cfg's own configured important_people
// entries, dropping any that are empty after trimming.
func normalizeImportantPeople(people []string) []string {
	out := make([]string, 0, len(people))
	for _, p := range people {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// importantPersonMatches implements INV-CAL-1's own disambiguation rule
// (packages/pg-connector/docs/behavior/invariants.md): an "@"-containing
// entry matches only attendee's own email (case-insensitive, no domain
// normalization — EqualFold does a full-string case-insensitive compare,
// never splitting or normalizing the domain part); a non-"@"-containing
// entry matches only attendee's own name (case-insensitive, exact string
// match, no fuzzy/substring matching).
func importantPersonMatches(person string, attendee apiAttendee) bool {
	if strings.Contains(person, "@") {
		return attendee.Email != "" && strings.EqualFold(attendee.Email, person)
	}
	return attendee.Name != "" && strings.EqualFold(attendee.Name, person)
}

// attendeesMatchImportantPeople reports whether any of attendees matches
// any of important (both per importantPersonMatches) — "organizer
// matching" degraded to attendee-list matching only, per INV-CAL-1 (see
// this file's package doc comment).
func attendeesMatchImportantPeople(attendees []apiAttendee, important []string) bool {
	for _, a := range attendees {
		for _, p := range important {
			if importantPersonMatches(p, a) {
				return true
			}
		}
	}
	return false
}

// computeSeverity implements this file's own combining formula (package
// doc comment) — the exact rule under which each of the three named
// attention-severity inputs demonstrably changes the result.
func computeSeverity(priority string, importantMatch bool, selfStatus string) schema.Severity {
	score := priorityWeight(priority)
	if importantMatch {
		score++
	}
	if isTentative(selfStatus) {
		score--
	}
	switch {
	case score >= 3:
		return schema.SeverityCritical
	case score == 2:
		return schema.SeverityHigh
	case score == 1:
		return schema.SeverityMedium
	default:
		return schema.SeverityLow
	}
}

// ListAttention implements the attention capability's attention.Provider:
// every surviving occurrence (see fetchOccurrences) starting within the
// next calendarAttentionWindow is reported as one AttentionItem, its
// Severity computed by computeSeverity from all three named inputs.
func (b *Backend) ListAttention(ctx context.Context) ([]schema.AttentionItem, error) {
	cfg, err := decodeBackendConfig(scriptout.ConfigFromContext(ctx))
	if err != nil {
		return nil, err
	}
	important := normalizeImportantPeople(cfg.ImportantPeople)

	now := time.Now().UTC()
	occs, err := b.fetchOccurrences(ctx, cfg, now, now.Add(calendarAttentionWindow), "", "")
	if err != nil {
		return nil, err
	}

	items := make([]schema.AttentionItem, 0, len(occs))
	for _, occ := range occs {
		matched := attendeesMatchImportantPeople(occ.event.Attendees, important)
		severity := computeSeverity(occ.cal.Priority, matched, occ.event.SelfStatus)
		items = append(items, schema.AttentionItem{
			// Type is a source-defined, generic string, not a closed enum
			// (schema.AttentionItem's own doc comment) [freedom boundary].
			Type:     "calendar_event",
			ID:       occ.event.ID,
			Summary:  fmt.Sprintf("%s (%s)", occ.event.Title, occ.cal.Name),
			Severity: severity,
		})
	}
	return items, nil
}

// Search implements the search capability's search.Provider: fields is
// unused — this backend populates no schema.SearchResult.Attributes
// beyond the core set [freedom boundary — apiEvent carries nothing this
// backend maps to Attributes today].
func (b *Backend) Search(ctx context.Context, query string, _ []string) ([]schema.SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "search: query required")
	}
	cfg, err := decodeBackendConfig(scriptout.ConfigFromContext(ctx))
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	occs, err := b.fetchOccurrences(ctx, cfg, now.Add(-searchWindowPast), now.Add(searchWindowFuture), "", query)
	if err != nil {
		return nil, err
	}

	results := make([]schema.SearchResult, 0, len(occs))
	for _, occ := range occs {
		results = append(results, schema.SearchResult{
			Type:   "calendar_event",
			ID:     occ.event.ID,
			Title:  occ.event.Title,
			Source: searchSourceName,
		})
	}
	return results, nil
}
