// backend.go: Backend implements pkg/provider/calendar.Provider by
// talking ONLY to pg-osx-bridge-api's local Unix-domain socket via a
// Transport (client.go) — no direct EventKit/go-eventkit import anywhere
// in this package [design: "no direct EventKit/go-eventkit import in this
// package"]. It additionally implements the OPTIONAL cross-cutting
// attention.Provider/search.Provider interfaces, asserted via type-check
// exactly like pg-connector-pr-github/pg-connector-issue-beads/
// pg-connector-issue-jira already do. It implements NO
// pkg/provider.AuthChecker (Binding decision): pg-osx-bridge-api requires no
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
//     calendar's own "events" results and, when the SAME occurrence (event ID
//     AND occurrence start — a recurring series' occurrences share one ID
//     and MUST NOT shadow one another, bead pg2-vmhgr) appears
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
//   - ListAttention's severity is a TIME-BASED RAMP with bounded modifiers
//     (bead pg2-pf1rb; INV-CAL-2..INV-CAL-6 in
//     packages/pg-connector/docs/behavior/invariants.md): an event not yet
//     started and starting later than attention_lead_time is "low"; one
//     starting within the lead time is "high"; one in progress
//     (start <= now < end) is "critical" and drops out once it ends.
//     Declined events are never reported (INV-CAL-6).
//
//   - CAL-RAMP-5 design choice (all-day events): an all-day event is
//     reported at "low" for as long as it overlaps the window, never
//     ramped and never raised by a modifier. Reasoning: an all-day event
//     has no meaningful start instant (it is typically a holiday, OOO
//     marker, birthday, or deadline, not a meeting needing attendance), so
//     "critical for the whole day" would pin the menubar's worst severity
//     for 24h; omitting it would silently lose information the operator
//     may want to see (consumers can already filter low severity out).
//
//   - CAL-RAMP-6 design choice (modifiers): the existing modifiers
//     (calendar priority, important_people, tentative) are KEPT as a
//     one-level shift of the time tier, capped at one level and clamped to
//     the [low, critical] enum: delta = clamp(+1 if the calendar priority
//     is "high", +1 if any attendee matches important_people, -1 if
//     SelfStatus is tentative; -1..+1). Reasoning: dropping them would
//     leave important_people (used by nothing else) and calendar_priority
//     as dead config, while an uncapped additive score would let a
//     modifier swallow the ramp (a far-off event outranking a running
//     one). Because the shift is the same constant at every tier and the
//     clamp is monotone, one event's severity is NON-DECREASING over time
//     (far <= soon <= in progress) for every modifier combination: the
//     ramp is never inverted. Priority "medium"/"normal" no longer
//     differs from "low"/unset for attention (it still feeds the dedup
//     tie-break priorityWeight).
//
//   - Look-back: ListAttention queries [now - attentionLookback,
//     now + attention_window) and filters locally to events that have not
//     ended (end > now) and start before the window closes. The daemon
//     delegates to EventKit's predicateForEventsWithStartDate:endDate:
//     calendars: + eventsMatchingPredicate: with NO local start/end
//     filtering (pg-osx-bridge-api internal/eventkitprovider and
//     go-eventkit v0.15.0 bridge_darwin.m ek_cal_fetch_events). EventKit
//     is generally understood to return events OVERLAPPING the range, but
//     nothing in the repos pins or tests that, so a query starting at "now"
//     cannot be PROVEN to return an in-progress event. Rather than assume,
//     the query start is widened by attentionLookback and the result is
//     filtered, which is correct whether or not the predicate overlaps.
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

// Defaults for ListAttention's per-backend config keys (INV-CAL-5):
//
//   - defaultAttentionWindow bounds the look-ahead: events starting within
//     the next 24h are reported — mirrors
//     cmd/pg-connector-issue-jira/internal.defaultAttentionThreshold's
//     "one day's notice" default.
//   - defaultAttentionLeadTime is the "starting soon" tier boundary.
//
// attentionLookback widens the events query start into the past so an
// in-progress event is returned even if the daemon's range predicate does
// not return overlapping events (see this file's package doc comment); it
// is a safety margin, not a config knob, and MUST exceed the longest
// non-all-day meeting expected.
const (
	defaultAttentionWindow   = 24 * time.Hour
	defaultAttentionLeadTime = 15 * time.Minute
	attentionLookback        = 24 * time.Hour
)

// Backend is pg-connector-calendar-osx-bridge's concrete calendar.Provider
// implementation.
type Backend struct {
	transport Transport
	// now is the clock; tests inject a fixed one. Production uses time.Now.
	now func() time.Time
}

// New returns a Backend wrapping the given Transport. Production wiring
// passes NewSocketClient(); tests inject a stub.
func New(t Transport) *Backend {
	return &Backend{transport: t, now: time.Now}
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
	// AttentionLeadTime / AttentionWindow are time.ParseDuration strings
	// (INV-CAL-5); empty/absent means the default.
	AttentionLeadTime string `json:"attention_lead_time,omitempty"`
	AttentionWindow   string `json:"attention_window,omitempty"`
}

// attentionTiming resolves cfg's attention_lead_time / attention_window to
// durations, applying defaults for absent keys. A malformed duration, a
// negative lead time, or a non-positive window answers
// scriptout.ErrInvalidArgument (INV-CAL-5).
func (cfg backendConfig) attentionTiming() (lead, window time.Duration, err error) {
	lead, window = defaultAttentionLeadTime, defaultAttentionWindow
	if v := strings.TrimSpace(cfg.AttentionLeadTime); v != "" {
		lead, err = time.ParseDuration(v)
		if err != nil || lead < 0 {
			return 0, 0, scriptout.WrapError(scriptout.ErrInvalidArgument,
				fmt.Sprintf("calendar: attention_lead_time %q must be a non-negative time.ParseDuration string", v))
		}
	}
	if v := strings.TrimSpace(cfg.AttentionWindow); v != "" {
		window, err = time.ParseDuration(v)
		if err != nil || window <= 0 {
			return 0, 0, scriptout.WrapError(scriptout.ErrInvalidArgument,
				fmt.Sprintf("calendar: attention_window %q must be a positive time.ParseDuration string", v))
		}
	}
	return lead, window, nil
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

// occurrenceKey identifies one OCCURRENCE of an event for dedup: the event
// ID plus the occurrence's own start instant. Occurrences of a plain
// recurring series share one event ID, so the ID alone would collapse a
// whole series in the queried range to its first occurrence (bead
// pg2-vmhgr); the same occurrence seen under two configured calendars
// shares both ID and start and so still collapses.
type occurrenceKey struct {
	id    string
	start int64 // apiEvent.Start as UnixNano
}

func keyOf(e apiEvent) occurrenceKey {
	return occurrenceKey{id: e.ID, start: e.Start.UnixNano()}
}

// fetchOccurrences resolves cfg's own configured calendars (optionally
// narrowed to exactly one by name via calendarFilter — Provider.List's/
// ListEvents'/Search's own "calendar" or "narrow to one" parameter),
// queries each resolved calendar's own "events" op for [start, end) (with
// search, when non-empty, passed straight through as
// apiEventsQuery.Search), drops a tentative occurrence whose own calendar
// is configured include_tentative:false, and dedupes the survivors by
// occurrence (event ID plus occurrence start; see occurrenceKey) using the
// tie-break rule this file's package doc comment documents. Returns the deduped occurrences in first-seen (config) order.
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

	winners := make(map[occurrenceKey]occurrence, len(resolved))
	order := make([]occurrenceKey, 0, len(resolved))
	for _, rc := range resolved {
		events, err := b.transport.Events(ctx, apiEventsQuery{Start: start, End: end, CalendarIDs: []string{rc.ID}, Search: search})
		if err != nil {
			return nil, err
		}
		for _, e := range events {
			if !rc.IncludeTentative && isTentative(e.SelfStatus) {
				continue
			}
			key := keyOf(e)
			cur, seen := winners[key]
			if !seen {
				winners[key] = occurrence{event: e, cal: rc}
				order = append(order, key)
				continue
			}
			if priorityWeight(rc.Priority) > priorityWeight(cur.cal.Priority) {
				winners[key] = occurrence{event: e, cal: rc}
			}
		}
	}

	out := make([]occurrence, 0, len(order))
	for _, key := range order {
		out = append(out, winners[key])
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

// severityLevels is the ramp's ladder in ascending rank (the
// schema.ValidSeverities order), indexed by level.
var severityLevels = []schema.Severity{schema.SeverityLow, schema.SeverityMedium, schema.SeverityHigh, schema.SeverityCritical}

// Time tiers' base levels in severityLevels (INV-CAL-2).
const (
	levelFar        = 0 // low:      not started, starts later than the lead time
	levelSoon       = 2 // high:     not started, starts within the lead time
	levelInProgress = 3 // critical: start <= now < end
)

// modifierDelta is the one-level, capped shift the existing modifiers
// apply to the time tier (INV-CAL-4): +1 for a "high" calendar priority,
// +1 when any attendee matches important_people, -1 when SelfStatus is
// tentative, summed and clamped to [-1, +1].
func modifierDelta(priority string, importantMatch bool, selfStatus string) int {
	d := 0
	if priorityWeight(priority) >= 2 {
		d++
	}
	if importantMatch {
		d++
	}
	if isTentative(selfStatus) {
		d--
	}
	if d > 1 {
		return 1
	}
	if d < -1 {
		return -1
	}
	return d
}

// computeSeverity maps one event's timing onto the attention ramp
// (INV-CAL-2..INV-CAL-4). ok is false when the event MUST NOT be reported
// (already ended, starts at/after the window's end, or declined). now is
// the call's clock reading; lead/window come from attentionTiming.
func computeSeverity(now time.Time, ev apiEvent, lead, window time.Duration, priority string, importantMatch bool) (sev schema.Severity, ok bool) {
	if isDeclined(ev.SelfStatus) {
		return "", false // INV-CAL-6
	}
	if !ev.End.After(now) {
		return "", false // ended (end <= now)
	}
	inProgress := !ev.Start.After(now) // start <= now < end
	if !inProgress && !ev.Start.Before(now.Add(window)) {
		return "", false // starts at/after the look-ahead window's end
	}
	if ev.AllDay {
		return schema.SeverityLow, true // INV-CAL-3: never ramped, never raised
	}
	level := levelFar
	switch {
	case inProgress:
		level = levelInProgress
	case ev.Start.Sub(now) <= lead:
		level = levelSoon
	}
	level += modifierDelta(priority, importantMatch, ev.SelfStatus)
	if level < 0 {
		level = 0
	}
	if level >= len(severityLevels) {
		level = len(severityLevels) - 1
	}
	return severityLevels[level], true
}

// isDeclined reports whether selfStatus is the declined RSVP state,
// case-insensitively.
func isDeclined(selfStatus string) bool {
	return strings.EqualFold(strings.TrimSpace(selfStatus), "declined")
}

// ListAttention implements the attention capability's attention.Provider
// as a time-based ramp (see this file's package doc comment and
// INV-CAL-2..INV-CAL-6): the events query is widened to
// [now - attentionLookback, now + attention_window) and each surviving
// occurrence is filtered and graded by computeSeverity.
func (b *Backend) ListAttention(ctx context.Context) ([]schema.AttentionItem, error) {
	cfg, err := decodeBackendConfig(scriptout.ConfigFromContext(ctx))
	if err != nil {
		return nil, err
	}
	lead, window, err := cfg.attentionTiming()
	if err != nil {
		return nil, err
	}
	important := normalizeImportantPeople(cfg.ImportantPeople)

	now := b.now().UTC()
	occs, err := b.fetchOccurrences(ctx, cfg, now.Add(-attentionLookback), now.Add(window), "", "")
	if err != nil {
		return nil, err
	}

	items := make([]schema.AttentionItem, 0, len(occs))
	for _, occ := range occs {
		matched := attendeesMatchImportantPeople(occ.event.Attendees, important)
		severity, ok := computeSeverity(now, occ.event, lead, window, occ.cal.Priority, matched)
		if !ok {
			continue
		}
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
