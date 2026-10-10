package internal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// stubTransport answers canned replies and remembers what it was asked.
type stubTransport struct {
	cal    apiCalendar
	att    apiAttention
	err    error
	gotURL string
	gotFrm time.Time
	gotTo  time.Time
	gotCal string
	calls  int
}

func (s *stubTransport) Calendar(_ context.Context, baseURL string, from, to time.Time, calendar string) (apiCalendar, error) {
	s.calls++
	s.gotURL, s.gotFrm, s.gotTo, s.gotCal = baseURL, from, to, calendar
	return s.cal, s.err
}

func (s *stubTransport) Attention(_ context.Context, baseURL string) (apiAttention, error) {
	s.calls++
	s.gotURL = baseURL
	return s.att, s.err
}

var fixedNow = time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC)

func newBackend(t Transport, env map[string]string) *Backend {
	b := New(t)
	b.now = func() time.Time { return fixedNow }
	b.getenv = func(k string) string { return env[k] }
	return b
}

func withConfig(raw string) context.Context {
	return scriptout.WithConfig(context.Background(), json.RawMessage(raw))
}

func code(err error) string { return scriptout.CodeForError(err) }

func twoSegments() apiCalendar {
	return apiCalendar{
		AsOf: "2026-10-10T15:00:00.000Z", CalendarID: "focus-cycles", Calendar: "Focus cycles",
		Events: []apiSegment{
			{ID: "ev-1", Title: "Deep work", Start: "2026-10-10T14:00:00.000Z", End: "2026-10-10T14:30:00.000Z", Notes: "cycle_type: deep-work\n---\nfirst"},
			{ID: "ev-2", Title: "Deep work", Start: "2026-10-10T14:40:00.000Z", End: "2026-10-10T15:00:00.000Z", Notes: "cycle_type: deep-work\n---\nfirst"},
		},
	}
}

func TestListEvents_MapsEverySegmentToACalendarEvent(t *testing.T) {
	st := &stubTransport{cal: twoSegments()}
	b := newBackend(st, nil)
	start := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)

	res, err := b.ListEvents(withConfig(`{"base_url":"http://127.0.0.1:5000/"}`), start, start.Add(24*time.Hour), "focus-cycles")
	if err != nil {
		t.Fatal(err)
	}
	if st.gotURL != "http://127.0.0.1:5000" || st.gotCal != "focus-cycles" || !st.gotFrm.Equal(start) || !st.gotTo.Equal(start.Add(24*time.Hour)) {
		t.Errorf("transport got url %q calendar %q window %v..%v", st.gotURL, st.gotCal, st.gotFrm, st.gotTo)
	}
	if len(res.Entities) != 2 || len(res.PresentIDs) != 2 || res.PresentIDs[0] != "ev-1" || res.PresentIDs[1] != "ev-2" {
		t.Fatalf("result = %+v", res)
	}
	e := res.Entities[0]
	want := schema.CalendarEvent{
		ID: "ev-1", Title: "Deep work", Start: "2026-10-10T14:00:00.000Z", End: "2026-10-10T14:30:00.000Z",
		CalendarID: "focus-cycles", Calendar: "Focus cycles", Notes: "cycle_type: deep-work\n---\nfirst",
		AsOf: "2026-10-10T15:00:00.000Z", Stale: false,
	}
	if e.ID != want.ID || e.Title != want.Title || e.Start != want.Start || e.End != want.End ||
		e.CalendarID != want.CalendarID || e.Calendar != want.Calendar || e.Notes != want.Notes ||
		e.AsOf != want.AsOf || e.Stale != want.Stale {
		t.Errorf("event = %+v, want %+v", e, want)
	}
	if res.Truncated || res.Cursor != nil {
		t.Errorf("a complete read must be untruncated with no cursor: %+v", res)
	}
}

// The notes block is a contract with the work tracker: it MUST reach the
// consumer byte for byte, including a note that itself contains a line equal
// to the separator.
func TestListEvents_NotesAreCopiedByteForByte(t *testing.T) {
	notes := "cycle_type: deep-work\nticket: ABC-1\nticket: ABC-2\n---\nReviewed it.\n---\nand a second separator line\n"
	st := &stubTransport{cal: apiCalendar{AsOf: "t", CalendarID: "focus-cycles", Events: []apiSegment{{ID: "e", Title: "T", Start: "s", End: "e", Notes: notes}}}}
	res, err := newBackend(st, nil).ListEvents(context.Background(), fixedNow, fixedNow.Add(time.Hour), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Entities[0].Notes != notes {
		t.Errorf("notes = %q, want %q", res.Entities[0].Notes, notes)
	}
}

func TestListEvents_EmptyAnswerIsAnEmptyNonNilResult(t *testing.T) {
	st := &stubTransport{cal: apiCalendar{AsOf: "t", CalendarID: "focus-cycles"}}
	res, err := newBackend(st, nil).ListEvents(context.Background(), fixedNow, fixedNow.Add(time.Hour), "other")
	if err != nil {
		t.Fatal(err)
	}
	if res.Entities == nil || res.PresentIDs == nil || len(res.Entities) != 0 {
		t.Errorf("result = %+v, want empty non-nil slices (a successful empty read, not an unknown one)", res)
	}
	if st.gotCal != "other" {
		t.Errorf("calendar = %q, want the caller's name passed through for the daemon to filter on", st.gotCal)
	}
}

func TestListEvents_EmptyWindowIsInvalidArgumentWithoutCallingTheDaemon(t *testing.T) {
	st := &stubTransport{}
	_, err := newBackend(st, nil).ListEvents(context.Background(), fixedNow, fixedNow, "")
	if code(err) != "invalid_argument" || st.calls != 0 {
		t.Errorf("code %q calls %d, want invalid_argument and no request", code(err), st.calls)
	}
}

func TestListEvents_StaleWhenNoAsOf(t *testing.T) {
	st := &stubTransport{cal: apiCalendar{CalendarID: "focus-cycles", Events: []apiSegment{{ID: "e", Title: "T", Start: "s", End: "e"}}}}
	res, _ := newBackend(st, nil).ListEvents(context.Background(), fixedNow, fixedNow.Add(time.Hour), "")
	if !res.Entities[0].Stale {
		t.Error("an event with no as-of time MUST be stale rather than carry a meaningless timestamp")
	}
}

func TestListEvents_MalformedSegmentIsUnavailable(t *testing.T) {
	st := &stubTransport{cal: apiCalendar{AsOf: "t", Events: []apiSegment{{ID: "", Title: "T", Start: "s", End: "e"}}}}
	_, err := newBackend(st, nil).ListEvents(context.Background(), fixedNow, fixedNow.Add(time.Hour), "")
	if code(err) != "unavailable" {
		t.Errorf("code = %q, want unavailable", code(err))
	}
}

func TestList_LargestDurationWindowStartingNow(t *testing.T) {
	st := &stubTransport{cal: twoSegments()}
	res, err := newBackend(st, nil).List(context.Background(), schema.QueryExpr{"1h", "72h", "30m"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !st.gotFrm.Equal(fixedNow) || !st.gotTo.Equal(fixedNow.Add(72*time.Hour)) {
		t.Errorf("window = %v..%v, want [now, now+72h)", st.gotFrm, st.gotTo)
	}
	if st.gotCal != "" {
		t.Errorf("calendar = %q, want every calendar", st.gotCal)
	}
	if len(res.Entities) != 2 {
		t.Errorf("entities = %d", len(res.Entities))
	}
}

func TestList_IDsOnlyOmitsEntitiesButKeepsIDs(t *testing.T) {
	st := &stubTransport{cal: twoSegments()}
	res, err := newBackend(st, nil).List(context.Background(), schema.QueryExpr{"1h"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entities != nil || len(res.PresentIDs) != 2 {
		t.Errorf("result = %+v, want ids only", res)
	}
}

func TestList_BadQueryIsInvalidArgument(t *testing.T) {
	for name, q := range map[string]schema.QueryExpr{
		"malformed": {"tomorrow"},
		"day unit":  {"1d"},
		"zero":      {"0s"},
		"negative":  {"-1h"},
		"empty":     {},
		"blank":     {"  "},
	} {
		t.Run(name, func(t *testing.T) {
			st := &stubTransport{}
			_, err := newBackend(st, nil).List(context.Background(), q, false)
			if code(err) != "invalid_argument" || st.calls != 0 {
				t.Errorf("code %q calls %d, want invalid_argument and no request", code(err), st.calls)
			}
		})
	}
}

func TestDaemonFailuresMapOntoTheTaxonomy(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"refused window", &DaemonError{Status: 400, Reason: "invalid_request", Detail: "empty"}, "invalid_argument"},
		{"daemon down", &DaemonError{Status: 0, Err: errors.New("connection refused")}, "unavailable"},
		{"not ready", &DaemonError{Status: 503, Reason: "not_ready"}, "unavailable"},
		{"daemon error", &DaemonError{Status: 500}, "unavailable"},
		{"undecodable", &DaemonError{Status: 200, Err: errors.New("malformed")}, "unavailable"},
		{"not a daemon error", errors.New("boom"), "unavailable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newBackend(&stubTransport{err: c.err}, nil)
			_, err := b.ListEvents(context.Background(), fixedNow, fixedNow.Add(time.Hour), "")
			if code(err) != c.want {
				t.Errorf("ListEvents code = %q, want %q", code(err), c.want)
			}
			if c.want == "unavailable" {
				// Attention has no caller-supplied input to refuse.
				_, err = b.ListAttention(context.Background())
				if code(err) != "unavailable" {
					t.Errorf("ListAttention code = %q, want unavailable", code(err))
				}
			}
			if !strings.Contains(err.Error(), "calendar-task-focus") {
				t.Errorf("message %q does not name the backend", err)
			}
		})
	}
}

func TestBaseURLResolution(t *testing.T) {
	cases := []struct {
		name    string
		config  string
		env     map[string]string
		want    string
		wantErr bool
	}{
		{"default", "", nil, "http://127.0.0.1:49210", false},
		{"env host:port", "", map[string]string{EnvAddr: "127.0.0.1:50000"}, "http://127.0.0.1:50000", false},
		{"config wins over env", `{"base_url":"https://focus.example.test"}`, map[string]string{EnvAddr: "127.0.0.1:50000"}, "https://focus.example.test", false},
		{"config trims", `{"base_url":" http://127.0.0.1:1/ "}`, nil, "http://127.0.0.1:1", false},
		{"empty config block", `{}`, nil, "http://127.0.0.1:49210", false},
		{"not a url", `{"base_url":"127.0.0.1:1"}`, nil, "", true},
		{"unknown scheme", `{"base_url":"ftp://x"}`, nil, "", true},
		{"undecodable config", `[1]`, nil, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			if c.config != "" {
				ctx = withConfig(c.config)
			}
			got, err := newBackend(&stubTransport{}, c.env).baseURL(ctx)
			if c.wantErr {
				if code(err) != "invalid_argument" {
					t.Errorf("err = %v, want invalid_argument", err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Errorf("baseURL = %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

func TestListAttention_MapsTheDaemonsFeed(t *testing.T) {
	st := &stubTransport{att: apiAttention{AsOf: "t", Items: []apiAttentionItem{
		{Type: "task", ID: "day:2026-10-10:plan", Summary: "Plan the day", Severity: "high", Group: apiAttentionGroup{Key: "daily", Label: "Today"}, URL: "https://focus.example.test/#/tasks/day:2026-10-10:plan"},
		{Type: "cycle", ID: "c1", Summary: "Deep work: 12 min over", Severity: "medium", Group: apiAttentionGroup{Key: "cycles", Label: "Cycles"}},
		{Type: "cycle", ID: "c2", Summary: "Review paused 50 min", Severity: "low", Group: apiAttentionGroup{Key: "cycles", Label: "Cycles"}},
	}}}
	items, err := newBackend(st, nil).ListAttention(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d", len(items))
	}
	a := items[0]
	if a.Type != "task" || a.ID != "day:2026-10-10:plan" || a.Summary != "Plan the day" || a.Severity != schema.SeverityHigh ||
		a.Group == nil || a.Group.Key != "daily" || a.Group.Label != "Today" || a.URL != "https://focus.example.test/#/tasks/day:2026-10-10:plan" {
		t.Errorf("task item = %+v", a)
	}
	if items[1].Severity != schema.SeverityMedium || items[1].URL != "" || items[2].Severity != schema.SeverityLow {
		t.Errorf("cycle items = %+v %+v", items[1], items[2])
	}
}

func TestListAttention_NothingNeedingAttentionIsAnEmptyNonNilFeed(t *testing.T) {
	items, err := newBackend(&stubTransport{att: apiAttention{AsOf: "t"}}, nil).ListAttention(context.Background())
	if err != nil || items == nil || len(items) != 0 {
		t.Errorf("items = %#v, err = %v, want an empty non-nil slice", items, err)
	}
}

// Severity is the daemon's closed table; a value outside it is omitted, never
// defaulted (INV-ALERT-4's rule, shared by every backend that maps severity).
func TestListAttention_UnrecognizedSeverityIsOmitted(t *testing.T) {
	st := &stubTransport{att: apiAttention{Items: []apiAttentionItem{
		{Type: "task", ID: "a", Summary: "A", Severity: "urgent"},
		{Type: "task", ID: "b", Summary: "B"},
	}}}
	items, err := newBackend(st, nil).ListAttention(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Severity != "" || items[1].Severity != "" || items[0].Group != nil {
		t.Errorf("items = %+v", items)
	}
}

func TestListAttention_ItemWithoutTypeOrIDIsMalformed(t *testing.T) {
	st := &stubTransport{att: apiAttention{Items: []apiAttentionItem{{Type: "task", Summary: "no id"}}}}
	_, err := newBackend(st, nil).ListAttention(context.Background())
	if code(err) != "unavailable" {
		t.Errorf("code = %q, want unavailable", code(err))
	}
}

// Epic ruling 7 (2026-10-08): while the daemon reports its store read-only the
// backend emits a HIGH-severity item that says so and names the restart as the
// recovery.
func TestListAttention_ReadOnlyStoreItemIsHighAndNamesTheRestart(t *testing.T) {
	cases := map[string]string{
		"daemon summary without recovery": "pg-task-focus is read-only: the append write failed",
		"trailing period":                 "pg-task-focus is read-only: the append write failed.",
		"empty summary":                   "",
	}
	for name, summary := range cases {
		t.Run(name, func(t *testing.T) {
			st := &stubTransport{att: apiAttention{Items: []apiAttentionItem{
				{Type: "store", ID: "store", Summary: summary, Severity: "high", Group: apiAttentionGroup{Key: "system", Label: "System"}},
			}}}
			items, err := newBackend(st, nil).ListAttention(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			it := items[0]
			if it.Severity != schema.SeverityHigh || it.Type != "store" || it.ID != "store" {
				t.Errorf("item = %+v", it)
			}
			if !strings.Contains(it.Summary, "read-only") || !strings.HasSuffix(it.Summary, "Restart pg-task-focus to recover") {
				t.Errorf("summary = %q, want it to say read-only and end by naming the restart", it.Summary)
			}
			if strings.Contains(it.Summary, "..") || strings.Contains(it.Summary, ". .") {
				t.Errorf("summary = %q has doubled punctuation", it.Summary)
			}
		})
	}
}

func TestListAttention_ReadOnlySummaryAlreadyNamingRestartIsLeftAlone(t *testing.T) {
	const s = "READ-ONLY: the append write failed. Restart pg-task-focus to recover"
	st := &stubTransport{att: apiAttention{Items: []apiAttentionItem{{Type: "store", ID: "store", Summary: s, Severity: "high"}}}}
	items, _ := newBackend(st, nil).ListAttention(context.Background())
	if items[0].Summary != s {
		t.Errorf("summary = %q, want it untouched", items[0].Summary)
	}
}

// A non-store item that happens to mention restarting is not the read-only item
// and MUST NOT be rewritten.
func TestListAttention_OnlyTheStoreItemIsRewritten(t *testing.T) {
	st := &stubTransport{att: apiAttention{Items: []apiAttentionItem{{Type: "task", ID: "t", Summary: "Do the thing", Severity: "low"}}}}
	items, _ := newBackend(st, nil).ListAttention(context.Background())
	if items[0].Summary != "Do the thing" {
		t.Errorf("summary = %q", items[0].Summary)
	}
}
