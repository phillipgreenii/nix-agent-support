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
		RecordClaudeCall(ctx)
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
	if ev.Level != "info" || ev.Op != "show" || ev.Msg != "show ok" || ev.Service != "pg-connector-thread-slack" {
		t.Errorf("unexpected event: %+v", ev)
	}
	if ev.ErrorCode != "" || ev.Error != "" || ev.FailureStage != "" || ev.FailureClass != "" {
		t.Errorf("success event carries a failure: %+v", ev)
	}
	if ev.DurationMS != 250 || ev.ClaudeCalls != 1 {
		t.Errorf("duration_ms/claude_calls = %d/%d, want 250/1", ev.DurationMS, ev.ClaudeCalls)
	}
	if ev.Version != "v1.2.3" || ev.PID != os.Getpid() {
		t.Errorf("version/pid = %q/%d", ev.Version, ev.PID)
	}
	if ev.Time != "2026-10-03T12:00:00.250Z" {
		t.Errorf("time = %q, want 2026-10-03T12:00:00.250Z", ev.Time)
	}
}

func TestInstrument_FailureEventCarriesWireCodeStageAndClass(t *testing.T) {
	sink := &memSink{}
	tb := Instrument(table(func(ctx context.Context, _ json.RawMessage) (any, error) {
		RecordClaudeCall(ctx)
		RecordFailure(ctx, StageClaudeError, "Invalid API key - Please run /login")
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "claude -p reported is_error: Invalid API key")
	}), sink, "", fakeClock(time.Now(), time.Millisecond))

	if _, err := call(t, tb); !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("error altered: %v", err)
	}
	ev := sink.events[0]
	if ev.Level != "error" || ev.ErrorCode != "unavailable" || ev.Msg != "show failed: unavailable" {
		t.Errorf("failure event = %+v", ev)
	}
	if ev.FailureStage != StageClaudeError || ev.FailureClass != ClassAuth {
		t.Errorf("stage/class = %q/%q, want %q/%q", ev.FailureStage, ev.FailureClass, StageClaudeError, ClassAuth)
	}
	if !strings.Contains(ev.Error, "Invalid API key") {
		t.Errorf("error not carried: %q", ev.Error)
	}
}

func TestInstrument_FailureStageIsDroppedWhenTheCallSucceeds(t *testing.T) {
	sink := &memSink{}
	tb := Instrument(table(func(ctx context.Context, _ json.RawMessage) (any, error) {
		RecordFailure(ctx, StageExec, "unauthorized")
		return "ok", nil
	}), sink, "", fakeClock(time.Now(), time.Millisecond))
	_, _ = call(t, tb)
	if ev := sink.events[0]; ev.FailureStage != "" || ev.FailureClass != "" || ev.Level != "info" {
		t.Errorf("stale failure leaked into a successful call: %+v", ev)
	}
}

func TestInstrument_NotFoundIsAWarnWithNoFailureStage(t *testing.T) {
	sink := &memSink{}
	tb := Instrument(table(func(ctx context.Context, _ json.RawMessage) (any, error) {
		RecordClaudeCall(ctx)
		return nil, scriptout.WrapError(scriptout.ErrNotFound, "thread \"x\" not found")
	}), sink, "", fakeClock(time.Now(), time.Millisecond))
	_, _ = call(t, tb)
	ev := sink.events[0]
	if ev.Level != "warn" || ev.ErrorCode != "not_found" || ev.FailureStage != "" {
		t.Errorf("not_found event = %+v", ev)
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
	for _, k := range []string{"error_code", "error", "claude_calls", "failure_stage", "failure_class", "version"} {
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
		ClaudeCalls: 2, FailureStage: StageExec, FailureClass: ClassAuth,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"time":"2026-10-03T12:00:00.250Z","level":"error","msg":"list failed: unavailable",` +
		`"service":"pg-connector-thread-slack","version":"v1","pid":42,"op":"list","duration_ms":250,` +
		`"error_code":"unavailable","error":"boom","claude_calls":2,"failure_stage":"exec",` +
		`"failure_class":"auth"}` + "\n"
	if string(line) != want {
		t.Errorf("wire shape changed:\n got %s\nwant %s", line, want)
	}
}

func TestRecorders_AreNoOpsWithoutARecorderOnTheContext(t *testing.T) {
	RecordClaudeCall(context.Background())
	RecordFailure(context.Background(), StageExec, "x")
}

func TestInstrument_RecorderIsPerCall(t *testing.T) {
	sink := &memSink{}
	n := 0
	tb := Instrument(table(func(ctx context.Context, _ json.RawMessage) (any, error) {
		n++
		RecordClaudeCall(ctx)
		if n == 1 {
			RecordFailure(ctx, StageExec, "not logged in")
			return nil, scriptout.WrapError(scriptout.ErrUnavailable, "x")
		}
		return nil, nil
	}), sink, "", fakeClock(time.Now(), time.Millisecond))
	_, _ = call(t, tb)
	_, _ = call(t, tb)
	if sink.events[1].FailureStage != "" || sink.events[1].ClaudeCalls != 1 {
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
		"Not logged in - Please run /login":                    ClassAuth,
		"Invalid API key":                                      ClassAuth,
		"OAuth token has expired":                              ClassAuth,
		"API Error: 401 {\"type\":\"authentication_error\"}":   ClassAuth,
		"slack MCP: invalid_auth":                              ClassAuth,
		"mcp server slack needs authentication":                ClassAuth,
		"HTTP 401 Unauthorized":                                ClassAuth,
		"max turns exceeded":                                   "",
		"claude: executable file not found in $PATH":           "",
		"context deadline exceeded":                            "",
		"API Error: 529 overloaded_error":                      "",
		"":                                                     "",
		"invalid character 'I' looking for beginning of value": "",
		"port 4010 refused":                                    "",
	} {
		if got := ClassifyFailure(text); got != want {
			t.Errorf("ClassifyFailure(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestRecordFailure_ClassifiesOnlyClaudeReportedStages(t *testing.T) {
	for stage, want := range map[string]string{
		StageExec: ClassAuth, StageClaudeError: ClassAuth,
		StageEnvelope: "", StageReplyDecode: "", StageReplyIncomplete: "",
	} {
		sink := &memSink{}
		tb := Instrument(table(func(ctx context.Context, _ json.RawMessage) (any, error) {
			RecordFailure(ctx, stage, "unauthorized")
			return nil, scriptout.WrapError(scriptout.ErrUnavailable, "x")
		}), sink, "", fakeClock(time.Now(), time.Millisecond))
		_, _ = call(t, tb)
		if got := sink.events[0].FailureClass; got != want {
			t.Errorf("stage %s: class = %q, want %q", stage, got, want)
		}
	}
}

func TestFileSink_AppendsOneLinePerEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", FileName)
	s := FileSink{Path: path, MaxBytes: 1 << 20}
	s.Write(Event{Base: evlog.Base{Time: "t1", Level: "info", Msg: "one", Op: "show"}})
	s.Write(Event{Base: evlog.Base{Time: "t2", Level: "error", Msg: "two", Op: "list", ErrorCode: "unavailable"}, FailureStage: StageExec})

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
	if second.ErrorCode != "unavailable" || second.FailureStage != StageExec {
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
		{"xdg state home", map[string]string{"XDG_STATE_HOME": "/s"}, "/s/pg-connector-thread-slack/events.jsonl"},
		{"home fallback", map[string]string{"HOME": "/h"}, "/h/.local/state/pg-connector-thread-slack/events.jsonl"},
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
	// darwin/modules/pg-connector-thread-slack registers the attribute name
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
	if EnvPath != "PG_CONNECTOR_THREAD_SLACK_EVENTS_FILE" {
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
