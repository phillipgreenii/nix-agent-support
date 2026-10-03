package eventlog

import (
	"bytes"
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
	return scriptout.DispatchTable{"list": scriptout.OpHandler{SchemaVersion: 1, Handle: h}}
}

func call(t *testing.T, tb scriptout.DispatchTable, op string) (any, error) {
	t.Helper()
	return tb[op].Handle(context.Background(), nil)
}

func TestInstrument_SuccessEventShape(t *testing.T) {
	sink := &memSink{}
	tb := Instrument(table(func(context.Context, json.RawMessage) (any, error) { return "res", nil }),
		sink, "v1.2.3", fakeClock(time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), 250*time.Millisecond))

	res, err := call(t, tb, "list")
	if err != nil || res != "res" {
		t.Fatalf("handler result altered: %v, %v", res, err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("events = %d, want 1", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Level != "info" || ev.Op != "list" || ev.Msg != "list ok" || ev.Service != "pg-connector-pr-github" {
		t.Errorf("unexpected event: %+v", ev)
	}
	if ev.ErrorCode != "" || ev.Error != "" {
		t.Errorf("success event carries an error: %+v", ev)
	}
	if ev.DurationMS != 250 {
		t.Errorf("duration_ms = %d, want 250", ev.DurationMS)
	}
	if ev.Version != "v1.2.3" || ev.PID != os.Getpid() {
		t.Errorf("version/pid = %q/%d", ev.Version, ev.PID)
	}
	if ev.Time != "2026-10-03T12:00:00.250Z" {
		t.Errorf("time = %q, want 2026-10-03T12:00:00.250Z", ev.Time)
	}
	if ev.GraphQLRemaining != nil || ev.GraphQLResetAt != nil || ev.GraphQLReserve != nil || ev.GraphQLHeadroom != nil || ev.BelowReserve {
		t.Errorf("event with no rate-limit read carries graphql fields: %+v", ev)
	}
}

func TestInstrument_JSONLineIsJSONLStandardAndOmitsUnsetFields(t *testing.T) {
	sink := &memSink{}
	tb := Instrument(table(func(context.Context, json.RawMessage) (any, error) { return nil, nil }),
		sink, "", fakeClock(time.Now(), time.Millisecond))
	_, _ = call(t, tb, "list")

	line, err := Line(sink.events[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(line, []byte("\n")) || bytes.Count(line, []byte("\n")) != 1 {
		t.Fatalf("not exactly one newline-terminated line: %q", line)
	}
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatalf("line is not JSON: %v", err)
	}
	// ADR 0038 required fields.
	for _, k := range []string{"time", "level", "msg"} {
		if s, _ := m[k].(string); s == "" {
			t.Errorf("required field %q missing/empty in %s", k, line)
		}
	}
	if _, err := time.Parse(time.RFC3339, m["time"].(string)); err != nil {
		t.Errorf("time is not RFC3339: %v", err)
	}
	for _, k := range []string{"error_code", "error", "graphql_remaining", "graphql_reset_at", "graphql_reserve", "graphql_headroom", "below_reserve", "version"} {
		if _, ok := m[k]; ok {
			t.Errorf("unset field %q should be omitted: %s", k, line)
		}
	}
}

func TestInstrument_ErrorCodeAndLevel(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantCode  string
		wantLevel string
	}{
		{"unauthenticated", scriptout.WrapError(scriptout.ErrUnauthenticated, "bad token"), "unauthenticated", "error"},
		{"unavailable", scriptout.WrapError(scriptout.ErrUnavailable, "down"), "unavailable", "error"},
		{"unwrapped falls back to unavailable", errors.New("plain"), "unavailable", "error"},
		{"not_found", scriptout.WrapError(scriptout.ErrNotFound, "gone"), "not_found", "warn"},
		{"invalid_argument", scriptout.WrapError(scriptout.ErrInvalidArgument, "bad"), "invalid_argument", "warn"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sink := &memSink{}
			tb := Instrument(table(func(context.Context, json.RawMessage) (any, error) { return nil, c.err }),
				sink, "", fakeClock(time.Now(), time.Millisecond))
			_, err := call(t, tb, "list")
			if !errors.Is(err, c.err) {
				t.Fatalf("handler error altered: %v", err)
			}
			ev := sink.events[0]
			if ev.ErrorCode != c.wantCode || ev.Level != c.wantLevel {
				t.Errorf("code/level = %q/%q, want %q/%q", ev.ErrorCode, ev.Level, c.wantCode, c.wantLevel)
			}
			if ev.Msg != "list failed: "+c.wantCode {
				t.Errorf("msg = %q", ev.Msg)
			}
			if !strings.Contains(ev.Error, c.err.Error()) {
				t.Errorf("error = %q, want it to contain %q", ev.Error, c.err.Error())
			}
			// The event code must agree with the wire envelope's code.
			if got := scriptout.ErrorResponse(c.err).Error.Code; got != ev.ErrorCode {
				t.Errorf("event code %q != wire code %q", ev.ErrorCode, got)
			}
		})
	}
}

func TestInstrument_RateLimitFields(t *testing.T) {
	cases := []struct {
		name         string
		remaining    int
		reserve      int
		resetAt      string
		wantHeadroom int
		wantBelow    bool
	}{
		{"above reserve", 4200, 1000, "2026-10-03T13:00:00Z", 3200, false},
		{"exactly at reserve", 1000, 1000, "2026-10-03T13:00:00Z", 0, false},
		{"below reserve", 640, 1000, "2026-10-03T13:00:00Z", -360, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sink := &memSink{}
			tb := Instrument(table(func(ctx context.Context, _ json.RawMessage) (any, error) {
				RecordRateLimit(ctx, c.remaining, c.resetAt, c.reserve)
				if c.remaining < c.reserve {
					return nil, scriptout.WrapError(scriptout.ErrUnavailable, "below reserve")
				}
				return nil, nil
			}), sink, "", fakeClock(time.Now(), time.Millisecond))
			_, _ = call(t, tb, "list")

			ev := sink.events[0]
			if ev.GraphQLRemaining == nil || *ev.GraphQLRemaining != c.remaining {
				t.Errorf("graphql_remaining = %v, want %d", ev.GraphQLRemaining, c.remaining)
			}
			if ev.GraphQLResetAt == nil || *ev.GraphQLResetAt != c.resetAt {
				t.Errorf("graphql_reset_at = %v, want %q", ev.GraphQLResetAt, c.resetAt)
			}
			if ev.GraphQLReserve == nil || *ev.GraphQLReserve != c.reserve {
				t.Errorf("graphql_reserve = %v, want %d", ev.GraphQLReserve, c.reserve)
			}
			if ev.GraphQLHeadroom == nil || *ev.GraphQLHeadroom != c.wantHeadroom {
				t.Errorf("graphql_headroom = %v, want %d", ev.GraphQLHeadroom, c.wantHeadroom)
			}
			if ev.BelowReserve != c.wantBelow {
				t.Errorf("below_reserve = %v, want %v", ev.BelowReserve, c.wantBelow)
			}

			// And the serialized field names are the ones the alert rules query.
			line, _ := Line(ev)
			var m map[string]any
			_ = json.Unmarshal(line, &m)
			if got := m["graphql_remaining"]; got != float64(c.remaining) {
				t.Errorf("serialized graphql_remaining = %v", got)
			}
			if got := m["graphql_reset_at"]; got != c.resetAt {
				t.Errorf("serialized graphql_reset_at = %v", got)
			}
			if got := m["graphql_headroom"]; got != float64(c.wantHeadroom) {
				t.Errorf("serialized graphql_headroom = %v", got)
			}
			_, hasBelow := m["below_reserve"]
			if hasBelow != c.wantBelow {
				t.Errorf("below_reserve serialized = %v, want %v", hasBelow, c.wantBelow)
			}
		})
	}
}

func TestInstrument_RateLimitWithoutResetOmitsResetAt(t *testing.T) {
	sink := &memSink{}
	tb := Instrument(table(func(ctx context.Context, _ json.RawMessage) (any, error) {
		RecordRateLimit(ctx, 3000, "", 1000)
		return nil, nil
	}), sink, "", fakeClock(time.Now(), time.Millisecond))
	_, _ = call(t, tb, "list")
	if sink.events[0].GraphQLResetAt != nil {
		t.Errorf("graphql_reset_at = %q, want omitted", *sink.events[0].GraphQLResetAt)
	}
	if sink.events[0].GraphQLRemaining == nil || *sink.events[0].GraphQLRemaining != 3000 {
		t.Errorf("graphql_remaining missing")
	}
}

func TestRecordRateLimit_NoRecorderIsNoOp(t *testing.T) {
	RecordRateLimit(context.Background(), 1, "x", 2) // must not panic
}

func TestInstrument_RecorderIsPerCall(t *testing.T) {
	sink := &memSink{}
	n := 0
	tb := Instrument(table(func(ctx context.Context, _ json.RawMessage) (any, error) {
		n++
		if n == 1 {
			RecordRateLimit(ctx, 10, "r", 1000)
		}
		return nil, nil
	}), sink, "", fakeClock(time.Now(), time.Millisecond))
	_, _ = call(t, tb, "list")
	_, _ = call(t, tb, "list")
	if sink.events[0].GraphQLRemaining == nil {
		t.Error("first call lost its reading")
	}
	if sink.events[1].GraphQLRemaining != nil {
		t.Error("second call inherited the first call's reading")
	}
}

func TestInstrument_NilSinkReturnsTableUnchanged(t *testing.T) {
	tb := table(func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	got := Instrument(tb, nil, "", time.Now)
	if len(got) != 1 {
		t.Fatalf("table changed: %v", got)
	}
}

func TestInstrument_PreservesSchemaVersionAndAllOps(t *testing.T) {
	tb := scriptout.DispatchTable{
		"a": {SchemaVersion: 3, Handle: func(context.Context, json.RawMessage) (any, error) { return nil, nil }},
		"b": {SchemaVersion: 4, Handle: func(context.Context, json.RawMessage) (any, error) { return nil, nil }},
	}
	got := Instrument(tb, &memSink{}, "", time.Now)
	if len(got) != 2 || got["a"].SchemaVersion != 3 || got["b"].SchemaVersion != 4 {
		t.Fatalf("schema versions/ops not preserved: %+v", got)
	}
}

func TestLevelForCode(t *testing.T) {
	for code, want := range map[string]string{
		"": "info", "unauthenticated": "error", "unavailable": "error",
		"not_found": "warn", "invalid_argument": "warn", "unknown_op": "warn",
		"version_mismatch": "warn", "query_not_recognized": "warn",
	} {
		if got := LevelForCode(code); got != want {
			t.Errorf("LevelForCode(%q) = %q, want %q", code, got, want)
		}
	}
}

// ---------------------------------------------------------------------
// File sink + rotation.
// ---------------------------------------------------------------------

func TestFileSink_AppendsOneLinePerEvent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "events.jsonl")
	s := FileSink{Path: path, MaxBytes: 1 << 20}
	s.Write(Event{Base: evlog.Base{Time: "t1", Level: "info", Msg: "one", Op: "list"}})
	s.Write(Event{Base: evlog.Base{Time: "t2", Level: "error", Msg: "two", Op: "show", ErrorCode: "unauthenticated"}})

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
	if second.ErrorCode != "unauthenticated" || second.Op != "show" {
		t.Errorf("round-trip mismatch: %+v", second)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("log mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestAppend_RotatedAndLockFilesDoNotMatchJSONLGlob(t *testing.T) {
	// The registered logSources glob is ${XDG_STATE_HOME}/<name>/*.jsonl; the
	// rotated copy and the lock must stay outside it or Loki would re-ingest
	// rotated lines.
	for _, n := range []string{FileName + ".1", FileName + ".lock"} {
		if ok, _ := filepath.Match("*.jsonl", n); ok {
			t.Errorf("%q matches *.jsonl", n)
		}
	}
	if ok, _ := filepath.Match("*.jsonl", FileName); !ok {
		t.Errorf("%q does not match *.jsonl", FileName)
	}
}

func TestAppend_ConcurrentWritersNeverInterleaveLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				ev := Event{Base: evlog.Base{Time: "t", Level: "info", Msg: strings.Repeat("m", 200), Op: "list"}}
				FileSink{Path: path, MaxBytes: 1 << 30}.Write(ev)
			}
		}()
	}
	wg.Wait()
	raw, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 400 {
		t.Fatalf("lines = %d, want 400", len(lines))
	}
	for i, l := range lines {
		var ev Event
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatalf("line %d corrupt: %v", i, err)
		}
	}
}

func TestFileSink_WriteFailureIsSwallowed(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Parent "directory" is a regular file: MkdirAll fails. Must not panic.
	FileSink{Path: filepath.Join(blocker, "events.jsonl")}.Write(Event{Base: evlog.Base{Msg: "x"}})
}

// ---------------------------------------------------------------------
// Path resolution.
// ---------------------------------------------------------------------

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
		{"xdg state home", map[string]string{"XDG_STATE_HOME": "/s"}, "/s/pg-connector-pr-github/events.jsonl"},
		{"home fallback", map[string]string{"HOME": "/h"}, "/h/.local/state/pg-connector-pr-github/events.jsonl"},
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
	// darwin/modules/pg-connector-pr-github registers the attribute name
	// ServiceName with the default glob ${XDG_STATE_HOME}/<name>/*.jsonl.
	got := Path(env(map[string]string{"XDG_STATE_HOME": "/s"}))
	want := filepath.Join("/s", ServiceName)
	if filepath.Dir(got) != want {
		t.Errorf("dir = %q, want %q", filepath.Dir(got), want)
	}
	if ok, _ := filepath.Match("*.jsonl", filepath.Base(got)); !ok {
		t.Errorf("base %q does not match *.jsonl", filepath.Base(got))
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

// TestLine_GoldenWireShape pins the exact bytes of an event line, field order
// included, so refactors of how Event is built (the shared pkg/eventlog
// writer, bead pg2-kjdfi) cannot silently change what Loki ingests.
func TestLine_GoldenWireShape(t *testing.T) {
	rem, reserve, headroom := 640, 1000, -360
	reset := "2026-10-03T14:00:00Z"
	line, err := Line(Event{
		Base: evlog.Base{
			Time: "2026-10-03T12:00:00.250Z", Level: "error", Msg: "list failed: unavailable",
			Service: ServiceName, Version: "v1", PID: 42, Op: "list", DurationMS: 250,
			ErrorCode: "unavailable", Error: "boom",
		},
		GraphQLRemaining: &rem, GraphQLResetAt: &reset, GraphQLReserve: &reserve, GraphQLHeadroom: &headroom,
		BelowReserve: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"time":"2026-10-03T12:00:00.250Z","level":"error","msg":"list failed: unavailable",` +
		`"service":"pg-connector-pr-github","version":"v1","pid":42,"op":"list","duration_ms":250,` +
		`"error_code":"unavailable","error":"boom","graphql_remaining":640,` +
		`"graphql_reset_at":"2026-10-03T14:00:00Z","graphql_reserve":1000,"graphql_headroom":-360,` +
		`"below_reserve":true}` + "\n"
	if string(line) != want {
		t.Errorf("wire shape changed:\n got %s\nwant %s", line, want)
	}
}
