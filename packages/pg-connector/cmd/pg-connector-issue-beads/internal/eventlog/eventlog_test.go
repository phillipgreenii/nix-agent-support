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

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

type memSink struct {
	mu       sync.Mutex
	events   []Event
	progress []evlog.ProgressEvent
}

func (m *memSink) Write(ev Event) {
	m.mu.Lock()
	m.events = append(m.events, ev)
	m.mu.Unlock()
}

func (m *memSink) WriteProgress(ev evlog.ProgressEvent) {
	m.mu.Lock()
	m.progress = append(m.progress, ev)
	m.mu.Unlock()
}

func table(h func(ctx context.Context, args json.RawMessage) (any, error)) scriptout.DispatchTable {
	return scriptout.DispatchTable{"list": {SchemaVersion: 1, Handle: h}}
}

func TestInstrument_RecordsBDCallsSlowestAndStartRow(t *testing.T) {
	sink := &memSink{}
	tb := Instrument(table(func(ctx context.Context, _ json.RawMessage) (any, error) {
		RecordBDCall(ctx, []string{"bd", "-C", "/ws", "list", "--json"}, 40*time.Millisecond)
		RecordBDCall(ctx, []string{"bd", "-C", "/ws", "show", "tp-1"}, 900*time.Millisecond)
		RecordBDCall(ctx, []string{"bd", "-C", "/ws", "dep", "list"}, 10*time.Millisecond)
		return "res", nil
	}), sink, "v9", time.Now)

	res, err := tb["list"].Handle(context.Background(), json.RawMessage(`{"query":"work-beads"}`))
	if res != "res" || err != nil {
		t.Fatalf("result altered: %v, %v", res, err)
	}
	if len(sink.progress) != 1 || sink.progress[0].Phase != evlog.PhaseStart ||
		sink.progress[0].Op != "list" || sink.progress[0].Args != `{"query":"work-beads"}` {
		t.Fatalf("progress rows = %+v, want one start row naming op and args", sink.progress)
	}
	if len(sink.events) != 1 {
		t.Fatalf("events = %d, want 1", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Service != ServiceName || ev.Op != "list" || ev.Version != "v9" || ev.Level != "info" || ev.Msg != "list ok" {
		t.Errorf("event base = %+v", ev.Base)
	}
	if ev.BDCalls != 3 || ev.BDSlowestMS == nil || *ev.BDSlowestMS != 900 || ev.BDSlowestArgv != "bd -C /ws show tp-1" {
		t.Errorf("bd fields = calls %d slowest %v argv %q", ev.BDCalls, ev.BDSlowestMS, ev.BDSlowestArgv)
	}
}

func TestInstrument_NoBDCallOmitsBDFieldsAndFailureKeepsCode(t *testing.T) {
	sink := &memSink{}
	tb := Instrument(table(func(context.Context, json.RawMessage) (any, error) {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "no workspace")
	}), sink, "", time.Now)
	_, _ = tb["list"].Handle(context.Background(), nil)
	ev := sink.events[0]
	if ev.ErrorCode != "unavailable" || ev.Level != "error" || ev.BDCalls != 0 || ev.BDSlowestMS != nil {
		t.Errorf("event = %+v", ev)
	}
	line, err := Line(ev)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"bd_calls", "bd_slowest_ms", "bd_slowest_argv"} {
		if strings.Contains(string(line), k) {
			t.Errorf("line carries %s without any bd call: %s", k, line)
		}
	}
}

func TestRecordBDCall_NoRecorderIsNoOp(t *testing.T) {
	RecordBDCall(context.Background(), []string{"bd"}, time.Second)
}

func TestRecordBDCall_LongArgvIsCappedOnRuneBoundary(t *testing.T) {
	sink := &memSink{}
	tb := Instrument(table(func(ctx context.Context, _ json.RawMessage) (any, error) {
		RecordBDCall(ctx, []string{"bd", strings.Repeat("é", 400)}, time.Second)
		return nil, nil
	}), sink, "", time.Now)
	_, _ = tb["list"].Handle(context.Background(), nil)
	argv := sink.events[0].BDSlowestArgv
	if len(argv) > maxArgvBytes+3 || !strings.HasSuffix(argv, "...") || strings.ContainsRune(argv, '�') {
		t.Errorf("argv = %d bytes %q", len(argv), argv)
	}
}

func TestInstrument_NilSinkReturnsTableUnchanged(t *testing.T) {
	tb := table(func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	got := Instrument(tb, nil, "", time.Now)
	if len(got) != 1 || got["list"].SchemaVersion != 1 {
		t.Errorf("table changed: %+v", got)
	}
}

// A call that never finishes (the SIGKILL case) must still leave a start row
// and heartbeats in the real log file.
func TestInstrument_FileSink_KilledCallLeavesStartAndHeartbeatNoFinalRow(t *testing.T) {
	orig := heartbeatInterval
	heartbeatInterval = 10 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = orig })

	path := filepath.Join(t.TempDir(), "events.jsonl")
	release := make(chan struct{})
	tb := Instrument(table(func(context.Context, json.RawMessage) (any, error) { <-release; return nil, errors.New("killed") }),
		FileSink{Path: path, MaxBytes: 1 << 20}, "", time.Now)
	done := make(chan struct{})
	go func() { defer close(done); _, _ = tb["list"].Handle(context.Background(), json.RawMessage(`{"q":1}`)) }()

	deadline := time.Now().Add(5 * time.Second)
	var raw []byte
	for time.Now().Before(deadline) {
		raw, _ = os.ReadFile(path)
		if strings.Count(string(raw), `"phase":"heartbeat"`) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	s := string(raw)
	if !strings.Contains(s, `"phase":"start"`) || !strings.Contains(s, `"phase":"heartbeat"`) {
		t.Fatalf("log lacks start/heartbeat rows while the call is in flight:\n%s", s)
	}
	if strings.Contains(s, "duration_ms") {
		t.Errorf("a final row was written for a call still in flight:\n%s", s)
	}
	close(release)
	<-done
}

func TestPath(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"explicit override", map[string]string{EnvPath: "/x/e.jsonl", "XDG_STATE_HOME": "/s"}, "/x/e.jsonl"},
		{"xdg state home", map[string]string{"XDG_STATE_HOME": "/s"}, "/s/pg-connector-issue-beads/events.jsonl"},
		{"home fallback", map[string]string{"HOME": "/h"}, "/h/.local/state/pg-connector-issue-beads/events.jsonl"},
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
	// darwin/modules/pg-connector-issue-beads registers the attribute name
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
	if EnvPath != "PG_CONNECTOR_ISSUE_BEADS_EVENTS_FILE" {
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
