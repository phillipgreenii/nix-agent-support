// backend.go: Backend implements pkg/provider/calendar.Provider and
// pkg/provider/attention.Provider over the pg-task-focus daemon's two
// connector reads (GET /api/v1/calendar, GET /api/v1/attention), bead
// pg2-t7me1.4. The daemon already derives both views from its projection (one
// segment per running interval of a cycle; an attention feed that is a pure
// function of the state), so this backend is an adapter: it translates the
// daemon's JSON into pkg/schema's wire types and the daemon's failures into
// the wire error taxonomy, and decides nothing about focus guidance itself.
//
// Binding decisions:
//
//   - Launched per call. The umbrella spawns a Tier-2 backend once per op,
//     writes one request to its stdin and reads one response from its stdout
//     (pkg/scriptout.ServeLoop is exactly one round trip), so a resident
//     backend would need a second, undefined transport. The daemon is already
//     the resident process; keeping this backend stateless per call also keeps
//     "the feed is stateless" (an attention item exists while its condition
//     holds) true end to end. Consequence: the backend exposes no metrics of
//     its own (Prometheus cannot scrape a one-shot process); its use is visible
//     in the daemon's http_requests_total{client="connector"}, and its own
//     start and final rows per call live in its event log (package eventlog).
//   - Segments, not cycles (calendar). A calendar event is wall-clock
//     occupancy, so a pause across lunch is not work: one event per running
//     segment, the id of the event that opened it (immutable), the cycle
//     type's title, the segment's bounds (an open segment ends at the daemon's
//     read time), and the notes block the daemon builds (the reserved
//     cycle_type pair first, operator pairs, a line that is exactly "---", then
//     the free-text note). Notes is copied byte for byte: the daemon owns the
//     format and its reference parser, and a consumer parses it with the same
//     rules.
//   - Fixed calendar. The daemon exposes one calendar, "focus-cycles"; it
//     filters on it (an empty name means all, any other name returns no
//     events) and the backend passes the caller's name through unchanged.
//   - List takes look-ahead durations (INV-CAL list convention: each element
//     a time.ParseDuration string, the largest wins). Nothing exists in the
//     future, so the window [now, now+d) yields at most the open segment of the
//     running cycle.
//   - No cache fallback. Stale is the daemon's own answer (always false on a
//     successful read) and a daemon that cannot be read answers unavailable, so
//     the umbrella's fan-out reports a degraded source and never an empty one.
//   - Failure mapping. An HTTP 400 from the daemon (a refused window) is
//     invalid_argument; every other failure (daemon down, not ready during
//     replay, an HTTP error status, a body that does not decode) is
//     unavailable. There is nothing to authenticate, so there is no
//     AuthChecker and `auth status` reports "disabled: not applicable" through
//     the wire-level unknown_op sentinel.
//   - Read-only mode is loud. While the daemon reports its store read-only it
//     serves a high-severity attention item of type store. The backend
//     guarantees that item names the restart as the recovery (operator ruling
//     2026-10-08, "make sure it is obvious that we are in read-only mode"),
//     appending the sentence when the daemon's summary does not already say it.
//   - Attention severity is the daemon's own closed table (low, medium, high).
//     A missing or unrecognized severity is omitted and never defaulted.
//
// The daemon's address is the backend config key base_url, else the
// PG_TASK_FOCUS_ADDR environment variable the daemon's own CLI reads (host:port),
// else the daemon's documented default 127.0.0.1:49210. A deployment that sets
// the daemon's listen port MUST set base_url to match.
package internal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/calendar"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// EnvAddr is the environment variable that names the daemon's host:port; the
// daemon's own CLI reads the same one.
const EnvAddr = "PG_TASK_FOCUS_ADDR"

// defaultBaseURL is the daemon's documented default listen address.
const defaultBaseURL = "http://127.0.0.1:49210"

// storeItemType is the attention item type the daemon uses for read-only mode.
const storeItemType = "store"

// readOnlyRecovery is the sentence that names the only way out of read-only
// mode.
const readOnlyRecovery = "Restart pg-task-focus to recover"

// Backend is pg-connector-calendar-task-focus's concrete provider.
type Backend struct {
	transport Transport
	getenv    func(string) string
	now       func() time.Time
}

// New returns a Backend over t. Production wiring passes NewHTTPClient();
// tests inject a stub.
func New(t Transport) *Backend {
	return &Backend{transport: t, getenv: os.Getenv, now: time.Now}
}

var (
	_ calendar.Provider  = (*Backend)(nil)
	_ attention.Provider = (*Backend)(nil)
)

// Deliberately NO `var _ provider.AuthChecker = (*Backend)(nil)`: the daemon is
// loopback only and takes no credential.

// backendConfig is the per-backend config block decoded from
// scriptout.ConfigFromContext on every call.
type backendConfig struct {
	BaseURL string `json:"base_url"`
}

// baseURL resolves the daemon's base URL (see the package doc comment).
func (b *Backend) baseURL(ctx context.Context) (string, error) {
	var cfg backendConfig
	if raw := scriptout.ConfigFromContext(ctx); len(raw) > 0 {
		if err := scriptout.Decode(raw, &cfg); err != nil {
			return "", scriptout.WrapError(scriptout.ErrInvalidArgument, "calendar-task-focus: decode config: "+err.Error())
		}
	}
	u := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if u == "" {
		if addr := strings.TrimSpace(b.getenv(EnvAddr)); addr != "" {
			u = "http://" + addr
		}
	}
	if u == "" {
		return defaultBaseURL, nil
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return "", scriptout.WrapError(scriptout.ErrInvalidArgument, "calendar-task-focus: the daemon address must be an http(s) URL (config.base_url) or host:port ("+EnvAddr+")")
	}
	return u, nil
}

// wrapDaemonError maps a transport failure onto the wire taxonomy: a refused
// window (HTTP 400) is the caller's fault, everything else is the daemon being
// unavailable.
func wrapDaemonError(err error) error {
	var de *DaemonError
	if errors.As(err, &de) && de.Status == 400 {
		return scriptout.WrapError(scriptout.ErrInvalidArgument, "calendar-task-focus: "+err.Error())
	}
	return scriptout.WrapError(scriptout.ErrUnavailable, "calendar-task-focus: "+err.Error())
}

// largestDuration implements Provider.List's query-expression convention: each
// element is a positive time.ParseDuration string and the largest wins. A
// malformed or non-positive element, or an empty query, is invalid_argument.
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
				fmt.Sprintf("calendar-task-focus: query element %q is not a valid time.ParseDuration string: %v", el, err))
		}
		if d <= 0 {
			return 0, scriptout.WrapError(scriptout.ErrInvalidArgument,
				fmt.Sprintf("calendar-task-focus: query element %q must be a positive duration", el))
		}
		if !found || d > max {
			max, found = d, true
		}
	}
	if !found {
		return 0, scriptout.WrapError(scriptout.ErrInvalidArgument, "calendar-task-focus: query must contain at least one time.ParseDuration element")
	}
	return max, nil
}

// List implements calendar.Provider.List: the segments overlapping
// [now, now+largest duration), which is at most the running cycle's open
// segment.
func (b *Backend) List(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.CalendarListResult, error) {
	d, err := largestDuration(query)
	if err != nil {
		return nil, err
	}
	now := b.now().UTC()
	return b.listEvents(ctx, now, now.Add(d), "", idsOnly)
}

// ListEvents implements calendar.Provider.ListEvents.
func (b *Backend) ListEvents(ctx context.Context, start, end time.Time, calendarName string) (*schema.CalendarListResult, error) {
	return b.listEvents(ctx, start, end, calendarName, false)
}

func (b *Backend) listEvents(ctx context.Context, start, end time.Time, calendarName string, idsOnly bool) (*schema.CalendarListResult, error) {
	if !end.After(start) {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument,
			fmt.Sprintf("calendar-task-focus: the window is empty: end (%s) is not after start (%s)", end.UTC().Format(time.RFC3339), start.UTC().Format(time.RFC3339)))
	}
	base, err := b.baseURL(ctx)
	if err != nil {
		return nil, err
	}
	cal, err := b.transport.Calendar(ctx, base, start, end, calendarName)
	if err != nil {
		return nil, wrapDaemonError(err)
	}

	entities := make([]schema.CalendarEvent, 0, len(cal.Events))
	ids := make([]string, 0, len(cal.Events))
	for _, s := range cal.Events {
		if s.ID == "" || s.Start == "" || s.End == "" {
			return nil, scriptout.WrapError(scriptout.ErrUnavailable, "calendar-task-focus: malformed daemon response: a segment lacks its id, start or end")
		}
		entities = append(entities, schema.CalendarEvent{
			ID:         s.ID,
			Title:      s.Title,
			Start:      s.Start,
			End:        s.End,
			CalendarID: cal.CalendarID,
			Calendar:   cal.Calendar,
			Notes:      s.Notes,
			AsOf:       cal.AsOf,
			Stale:      cal.Stale || cal.AsOf == "",
		})
		ids = append(ids, s.ID)
	}
	res := &schema.CalendarListResult{Entities: entities, PresentIDs: ids, Cursor: nil, Truncated: false}
	if idsOnly {
		res.Entities = nil
	}
	return res, nil
}

// ListAttention implements attention.Provider.
func (b *Backend) ListAttention(ctx context.Context) ([]schema.AttentionItem, error) {
	base, err := b.baseURL(ctx)
	if err != nil {
		return nil, err
	}
	att, err := b.transport.Attention(ctx, base)
	if err != nil {
		return nil, wrapDaemonError(err)
	}
	items := make([]schema.AttentionItem, 0, len(att.Items))
	for _, it := range att.Items {
		if it.Type == "" || it.ID == "" {
			return nil, scriptout.WrapError(scriptout.ErrUnavailable, "calendar-task-focus: malformed daemon response: an attention item lacks its type or id")
		}
		out := schema.AttentionItem{Type: it.Type, ID: it.ID, Summary: it.Summary, URL: it.URL}
		if sev := schema.Severity(it.Severity); sev.IsValid() {
			out.Severity = sev
		}
		if it.Group.Key != "" {
			out.Group = &schema.AttentionGroup{Key: it.Group.Key, Label: it.Group.Label}
		}
		if it.Type == storeItemType {
			out.Summary = nameTheRecovery(out.Summary)
		}
		items = append(items, out)
	}
	return items, nil
}

// nameTheRecovery makes a read-only store item's summary name the restart as
// the recovery, leaving a summary that already says so untouched.
func nameTheRecovery(summary string) string {
	if strings.Contains(strings.ToLower(summary), "restart") {
		return summary
	}
	summary = strings.TrimRight(strings.TrimSpace(summary), ". ")
	if summary == "" {
		return "pg-task-focus is read-only. " + readOnlyRecovery
	}
	return summary + ". " + readOnlyRecovery
}
