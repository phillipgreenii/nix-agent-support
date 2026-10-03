package eventlog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	evlog "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// memSink collects events in memory.
type memSink struct {
	mu     sync.Mutex
	events []Event
}

func (m *memSink) Write(ev Event) {
	m.mu.Lock()
	m.events = append(m.events, ev)
	m.mu.Unlock()
}

// fakeClock returns t, then t+step on every later call.
func fakeClock(t time.Time, step time.Duration) func() time.Time {
	var mu sync.Mutex
	cur := t
	first := true
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		if first {
			first = false
			return cur
		}
		cur = cur.Add(step)
		return cur
	}
}

func table(h func(ctx context.Context, args json.RawMessage) (any, error)) scriptout.DispatchTable {
	return scriptout.DispatchTable{"show": scriptout.OpHandler{SchemaVersion: 1, Handle: h}}
}

func call(t *testing.T, tb scriptout.DispatchTable) (any, error) {
	t.Helper()
	return tb["show"].Handle(context.Background(), nil)
}

func TestInstrument_SuccessEventShape(t *testing.T) {
	sink := &memSink{}
	tb := Instrument(table(func(ctx context.Context, _ json.RawMessage) (any, error) {
		RecordPjiraCall(ctx)
		return "res", nil
	}), sink, "v1.2.3", fakeClock(time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), 250*time.Millisecond))

	res, err := call(t, tb)
	if err != nil || res != "res" {
		t.Fatalf("handler result altered: %v, %v", res, err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("events = %d, want 1", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Level != "info" || ev.Op != "show" || ev.Msg != "show ok" || ev.Service != "pg-connector-issue-jira" {
		t.Errorf("unexpected event: %+v", ev)
	}
	if ev.ErrorCode != "" || ev.Error != "" || ev.AuthState != "" || ev.FailureClass != "" {
		t.Errorf("success event carries a failure: %+v", ev)
	}
	if ev.DurationMS != 250 || ev.PjiraCalls != 1 {
		t.Errorf("duration_ms/pjira_calls = %d/%d, want 250/1", ev.DurationMS, ev.PjiraCalls)
	}
	if ev.Version != "v1.2.3" || ev.PID != os.Getpid() {
		t.Errorf("version/pid = %q/%d", ev.Version, ev.PID)
	}
	if ev.Time != "2026-10-03T12:00:00.250Z" {
		t.Errorf("time = %q, want 2026-10-03T12:00:00.250Z", ev.Time)
	}
}

// failing runs one failing call whose handler records pjira text, and returns
// the resulting event.
func failing(t *testing.T, wireErr error, pjiraText string) Event {
	t.Helper()
	sink := &memSink{}
	tb := Instrument(table(func(ctx context.Context, _ json.RawMessage) (any, error) {
		RecordPjiraCall(ctx)
		RecordPjiraFailure(ctx, pjiraText)
		return nil, scriptout.WrapError(wireErr, pjiraText)
	}), sink, "", fakeClock(time.Now(), time.Millisecond))
	if _, err := call(t, tb); !errors.Is(err, wireErr) {
		t.Fatalf("error altered: %v", err)
	}
	return sink.events[0]
}

func TestInstrument_UnauthenticatedIsTaggedAuth(t *testing.T) {
	ev := failing(t, scriptout.ErrUnauthenticated, "pjira issue -- X-1: exit status 1: pjira: get issue X-1: status 401 Unauthorized")
	if ev.Level != "error" || ev.ErrorCode != "unauthenticated" || ev.Msg != "show failed: unauthenticated" {
		t.Errorf("event = %+v", ev)
	}
	if ev.FailureClass != ClassAuth {
		t.Errorf("failure_class = %q, want %q", ev.FailureClass, ClassAuth)
	}
	if !strings.Contains(ev.Error, "401") {
		t.Errorf("error not carried: %q", ev.Error)
	}
}

func TestInstrument_Throttle429IsUnavailableTaggedRateLimited(t *testing.T) {
	ev := failing(t, scriptout.ErrUnavailable, "pjira search: exit status 1: pjira: search: status 429 Too Many Requests")
	if ev.Level != "error" || ev.ErrorCode != "unavailable" {
		t.Errorf("event = %+v (the wire code must stay unavailable)", ev)
	}
	if ev.FailureClass != ClassRateLimited {
		t.Errorf("failure_class = %q, want %q", ev.FailureClass, ClassRateLimited)
	}
}

func TestInstrument_PlainUnavailableHasNoFailureClass(t *testing.T) {
	ev := failing(t, scriptout.ErrUnavailable, "pjira issue -- X-1: executable file not found in $PATH (is pjira on PATH?)")
	if ev.ErrorCode != "unavailable" || ev.FailureClass != "" {
		t.Errorf("event = %+v", ev)
	}
}

func TestInstrument_RateLimitTextOnASuccessfulCallIsDropped(t *testing.T) {
	sink := &memSink{}
	tb := Instrument(table(func(ctx context.Context, _ json.RawMessage) (any, error) {
		RecordPjiraFailure(ctx, "status 429 Too Many Requests")
		return "ok", nil
	}), sink, "", fakeClock(time.Now(), time.Millisecond))
	_, _ = call(t, tb)
	if ev := sink.events[0]; ev.FailureClass != "" || ev.Level != "info" {
		t.Errorf("stale failure leaked into a successful call: %+v", ev)
	}
}

func TestInstrument_NotFoundIsAWarnWithNoFailureClass(t *testing.T) {
	ev := failing(t, scriptout.ErrNotFound, "pjira: issue X-1 not found")
	if ev.Level != "warn" || ev.ErrorCode != "not_found" || ev.FailureClass != "" {
		t.Errorf("not_found event = %+v", ev)
	}
}

func TestInstrument_AuthStatusStatesAreRecordedOnASuccessfulCall(t *testing.T) {
	for _, c := range []struct {
		state     string
		wantClass string
		wantLevel string
		wantMsg   string
	}{
		{"OK", "", "info", "show ok"},
		{"MISSING", ClassAuth, "error", "show failed: MISSING"},
		{"UNAUTHENTICATED", ClassAuth, "error", "show failed: UNAUTHENTICATED"},
		{"FORBIDDEN", ClassAuth, "error", "show failed: FORBIDDEN"},
		{"ERROR", "", "warn", "show failed: ERROR"},
	} {
		t.Run(c.state, func(t *testing.T) {
			sink := &memSink{}
			tb := Instrument(table(func(ctx context.Context, _ json.RawMessage) (any, error) {
				RecordAuthState(ctx, c.state)
				return "answered", nil // auth_status never errors on the wire
			}), sink, "", fakeClock(time.Now(), time.Millisecond))
			_, _ = call(t, tb)
			ev := sink.events[0]
			if ev.AuthState != c.state || ev.FailureClass != c.wantClass || ev.Level != c.wantLevel || ev.Msg != c.wantMsg {
				t.Errorf("event = %+v, want class=%q level=%q msg=%q", ev, c.wantClass, c.wantLevel, c.wantMsg)
			}
			if ev.ErrorCode != "" {
				t.Errorf("error_code = %q: auth_status must stay a well-formed wire answer", ev.ErrorCode)
			}
		})
	}
}

func TestInstrument_JSONLineIsJSONLStandardAndOmitsUnsetFields(t *testing.T) {
	sink := &memSink{}
	tb := Instrument(table(func(context.Context, json.RawMessage) (any, error) { return nil, nil }),
		sink, "", fakeClock(time.Now(), time.Millisecond))
	_, _ = call(t, tb)

	line, err := Line(sink.events[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatalf("line is not JSON: %v", err)
	}
	for _, k := range []string{"time", "level", "msg"} { // ADR 0038 required fields
		if s, _ := m[k].(string); s == "" {
			t.Errorf("required field %q missing/empty in %s", k, line)
		}
	}
	if _, err := time.Parse(time.RFC3339, m["time"].(string)); err != nil {
		t.Errorf("time is not RFC3339: %v", err)
	}
	for _, k := range []string{"error_code", "error", "pjira_calls", "auth_state", "failure_class", "version"} {
		if _, ok := m[k]; ok {
			t.Errorf("unset field %q should be omitted: %s", k, line)
		}
	}
}

// TestLine_GoldenWireShape pins the exact bytes of an event line, field order
// included: this is the shape the alert rules and Loki depend on.
func TestLine_GoldenWireShape(t *testing.T) {
	line, err := Line(Event{
		Base: evlog.Base{
			Time: "2026-10-03T12:00:00.250Z", Level: "error", Msg: "list failed: unavailable",
			Service: ServiceName, Version: "v1", PID: 42, Op: "list", DurationMS: 250,
			ErrorCode: "unavailable", Error: "boom",
		},
		PjiraCalls: 2, AuthState: "ERROR", FailureClass: ClassRateLimited,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"time":"2026-10-03T12:00:00.250Z","level":"error","msg":"list failed: unavailable",` +
		`"service":"pg-connector-issue-jira","version":"v1","pid":42,"op":"list","duration_ms":250,` +
		`"error_code":"unavailable","error":"boom","pjira_calls":2,"auth_state":"ERROR",` +
		`"failure_class":"rate_limited"}` + "\n"
	if string(line) != want {
		t.Errorf("wire shape changed:\n got %s\nwant %s", line, want)
	}
}

func TestRecorders_AreNoOpsWithoutARecorderOnTheContext(t *testing.T) {
	RecordPjiraCall(context.Background())
	RecordPjiraFailure(context.Background(), "status 429")
	RecordAuthState(context.Background(), "OK")
}

func TestInstrument_RecorderIsPerCall(t *testing.T) {
	sink := &memSink{}
	n := 0
	tb := Instrument(table(func(ctx context.Context, _ json.RawMessage) (any, error) {
		n++
		RecordPjiraCall(ctx)
		if n == 1 {
			RecordPjiraFailure(ctx, "status 429 Too Many Requests")
			return nil, scriptout.WrapError(scriptout.ErrUnavailable, "x")
		}
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "y")
	}), sink, "", fakeClock(time.Now(), time.Millisecond))
	_, _ = call(t, tb)
	_, _ = call(t, tb)
	if sink.events[1].FailureClass != "" || sink.events[1].PjiraCalls != 1 {
		t.Errorf("second call inherited the first call's state: %+v", sink.events[1])
	}
}

func TestInstrument_NilSinkReturnsTableUnchanged(t *testing.T) {
	tb := table(func(context.Context, json.RawMessage) (any, error) { return 1, nil })
	got := Instrument(tb, nil, "", time.Now)
	if len(got) != 1 {
		t.Fatalf("table changed: %v", got)
	}
}

func TestClassifyFailure(t *testing.T) {
	for text, want := range map[string]string{
		"pjira: search: status 429 Too Many Requests":        ClassRateLimited,
		"pjira: get issue X-1: status 429":                   ClassRateLimited,
		"HTTP Too Many Requests":                             ClassRateLimited,
		"pjira: issue PROJ-429 not found":                    "", // an issue key is not a throttle
		"pjira: get issue PROJ-429: status 500 Server Error": "",
		"pjira: search: status 503 Service Unavailable":      "",
		"pjira: get issue X-1: status 401 Unauthorized":      "", // auth is tagged from the wire code
		"executable file not found in $PATH":                 "",
		"":                                                   "",
	} {
		if got := ClassifyFailure(text); got != want {
			t.Errorf("ClassifyFailure(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestNormalizeAuthState(t *testing.T) {
	for _, c := range []struct {
		out    string
		failed bool
		want   string
	}{
		{"OK\n", false, "OK"},
		{"MISSING\n", true, "MISSING"},
		{"UNAUTHENTICATED\n", true, "UNAUTHENTICATED"},
		{" forbidden \n", true, "FORBIDDEN"},
		{"ERROR\n", true, "ERROR"},
		{"", true, "ERROR"},                 // pjira not runnable / printed nothing
		{"garbage", true, "ERROR"},          // failed run with an unrecognised line
		{"SOMETHING\n", false, "SOMETHING"}, // a successful run's own state is kept
	} {
		if got := NormalizeAuthState(c.out, c.failed); got != c.want {
			t.Errorf("NormalizeAuthState(%q, %v) = %q, want %q", c.out, c.failed, got, c.want)
		}
	}
}

func TestFileSink_AppendsOneLinePerEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", FileName)
	s := FileSink{Path: path, MaxBytes: 1 << 20}
	s.Write(Event{Base: evlog.Base{Time: "t1", Level: "info", Msg: "one", Op: "show"}})
	s.Write(Event{Base: evlog.Base{Time: "t2", Level: "error", Msg: "two", Op: "list", ErrorCode: "unavailable"}, FailureClass: ClassRateLimited})

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2: %q", len(lines), raw)
	}
	var second Event
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatal(err)
	}
	if second.ErrorCode != "unavailable" || second.FailureClass != ClassRateLimited {
		t.Errorf("round-trip mismatch: %+v", second)
	}
}

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestPath(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"explicit override", map[string]string{EnvPath: "/x/e.jsonl", "XDG_STATE_HOME": "/s"}, "/x/e.jsonl"},
		{"xdg state home", map[string]string{"XDG_STATE_HOME": "/s"}, "/s/pg-connector-issue-jira/events.jsonl"},
		{"home fallback", map[string]string{"HOME": "/h"}, "/h/.local/state/pg-connector-issue-jira/events.jsonl"},
		{"nothing", map[string]string{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Path(env(c.env)); got != c.want {
				t.Errorf("Path = %q, want %q", got, c.want)
			}
		})
	}
}

func TestPath_DefaultMatchesRegisteredLogSourceGlob(t *testing.T) {
	// darwin/modules/pg-connector-issue-jira registers the attribute name
	// ServiceName with the default glob ${XDG_STATE_HOME}/<name>/*.jsonl.
	got := Path(env(map[string]string{"XDG_STATE_HOME": "/s"}))
	if want := filepath.Join("/s", ServiceName); filepath.Dir(got) != want {
		t.Errorf("dir = %q, want %q", filepath.Dir(got), want)
	}
	if ok, _ := filepath.Match("*.jsonl", filepath.Base(got)); !ok {
		t.Errorf("base %q does not match *.jsonl", filepath.Base(got))
	}
	for _, n := range []string{FileName + ".1", FileName + ".lock"} {
		if ok, _ := filepath.Match("*.jsonl", n); ok {
			t.Errorf("%q matches *.jsonl", n)
		}
	}
}

func TestEnvPathIsTheBackendsOwnVariable(t *testing.T) {
	if EnvPath != "PG_CONNECTOR_ISSUE_JIRA_EVENTS_FILE" {
		t.Errorf("EnvPath = %q", EnvPath)
	}
}

func TestSinkFromEnv(t *testing.T) {
	if SinkFromEnv(env(map[string]string{EnvPath: "off"})) != nil {
		t.Error("off should disable the sink")
	}
	if SinkFromEnv(env(map[string]string{})) != nil {
		t.Error("no home should yield no sink")
	}
	s, ok := SinkFromEnv(env(map[string]string{EnvPath: "/x/e.jsonl"})).(FileSink)
	if !ok || s.Path != "/x/e.jsonl" || s.MaxBytes != MaxBytes {
		t.Errorf("sink = %+v", s)
	}
}
