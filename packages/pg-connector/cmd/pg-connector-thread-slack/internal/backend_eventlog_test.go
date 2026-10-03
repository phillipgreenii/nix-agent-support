package internal

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-thread-slack/internal/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Bead pg2-kjdfi: the backend notes, on the in-flight call's event, how many
// claude -p executions it made and where a failed call broke. These tests
// drive the real Backend handlers under eventlog.Instrument and assert the
// event the backend's own log would receive, and that the returned error is
// the one it always was.

type captureSink struct {
	mu     sync.Mutex
	events []eventlog.Event
}

func (c *captureSink) Write(ev eventlog.Event) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
}

func (c *captureSink) last(t *testing.T) eventlog.Event {
	t.Helper()
	if len(c.events) == 0 {
		t.Fatal("no event written")
	}
	return c.events[len(c.events)-1]
}

// instrumented exposes b's show/list as an instrumented dispatch table.
func instrumented(b *Backend, sink eventlog.Sink) scriptout.DispatchTable {
	table := scriptout.DispatchTable{
		"show": {SchemaVersion: 1, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
			return b.Show(ctx, "1726000000.000100")
		}},
		"list": {SchemaVersion: 1, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
			return b.List(ctx, schema.QueryExpr{"from:me", "in:#x"}, false)
		}},
	}
	return eventlog.Instrument(table, sink, "test", time.Now)
}

const okThread = `{"found":true,"id":"1726000000.000100","channel":"C1","permalink":"https://x.invalid/p1"}`

func TestBackendEvents_ShowFailureStages(t *testing.T) {
	cases := []struct {
		name      string
		runner    func(string) (string, error)
		wantErr   error
		wantCode  string
		wantStage string
		wantClass string
		wantCalls int
	}{
		{"success", func(string) (string, error) { return envelope(okThread), nil }, nil, "", "", "", 1},
		{"not found is not a failure", func(string) (string, error) { return envelope(`{"found":false}`), nil }, scriptout.ErrNotFound, "not_found", "", "", 1},
		{
			"exec failure", func(string) (string, error) { return "", errors.New("claude: executable file not found in $PATH") },
			scriptout.ErrUnavailable, "unavailable", eventlog.StageExec, "", 1,
		},
		{
			"exec failure that reads as auth", func(string) (string, error) {
				return "", errors.New("exit status 1: Not logged in - Please run /login")
			},
			scriptout.ErrUnavailable, "unavailable", eventlog.StageExec, eventlog.ClassAuth, 1,
		},
		{
			"bad envelope", func(string) (string, error) { return "not json at all", nil },
			scriptout.ErrUnavailable, "unavailable", eventlog.StageEnvelope, "", 1,
		},
		{
			"claude is_error", func(string) (string, error) { return `{"result":"max turns exceeded","is_error":true}`, nil },
			scriptout.ErrUnavailable, "unavailable", eventlog.StageClaudeError, "", 1,
		},
		{
			"claude is_error that reads as auth", func(string) (string, error) { return `{"result":"Invalid API key","is_error":true}`, nil },
			scriptout.ErrUnavailable, "unavailable", eventlog.StageClaudeError, eventlog.ClassAuth, 1,
		},
		{
			"reply is prose", func(string) (string, error) { return envelope(`this is not json`), nil },
			scriptout.ErrUnavailable, "unavailable", eventlog.StageReplyDecode, "", 1,
		},
		{
			"reply lacks required fields", func(string) (string, error) { return envelope(`{"found":true,"id":"x"}`), nil },
			scriptout.ErrUnavailable, "unavailable", eventlog.StageReplyIncomplete, "", 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sink := &captureSink{}
			table := instrumented(New(&fakeRunner{handle: c.runner}), sink)
			_, err := table["show"].Handle(context.Background(), nil)
			if c.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantErr != nil && !errors.Is(err, c.wantErr) {
				t.Fatalf("error = %v, want %v (the instrumented call must keep returning the same error)", err, c.wantErr)
			}
			ev := sink.last(t)
			if ev.Op != "show" || ev.ErrorCode != c.wantCode || ev.FailureStage != c.wantStage ||
				ev.FailureClass != c.wantClass || ev.ClaudeCalls != c.wantCalls {
				t.Errorf("event = %+v\nwant code=%q stage=%q class=%q calls=%d",
					ev, c.wantCode, c.wantStage, c.wantClass, c.wantCalls)
			}
		})
	}
}

func TestBackendEvents_ListCountsOneClaudeCallPerModifier(t *testing.T) {
	sink := &captureSink{}
	table := instrumented(New(&fakeRunner{handle: func(string) (string, error) {
		return envelope(`{"items":[]}`), nil
	}}), sink)
	if _, err := table["list"].Handle(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if ev := sink.last(t); ev.Op != "list" || ev.ClaudeCalls != 2 || ev.ErrorCode != "" {
		t.Errorf("list event = %+v, want 2 claude calls", ev)
	}
}

func TestBackendEvents_ListFailsOnSecondModifier(t *testing.T) {
	sink := &captureSink{}
	n := 0
	table := instrumented(New(&fakeRunner{handle: func(string) (string, error) {
		n++
		if n == 2 {
			return envelope(`not json`), nil
		}
		return envelope(`{"items":[]}`), nil
	}}), sink)
	if _, err := table["list"].Handle(context.Background(), nil); !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if ev := sink.last(t); ev.ClaudeCalls != 2 || ev.FailureStage != eventlog.StageReplyDecode {
		t.Errorf("list event = %+v", ev)
	}
}

// Without an Instrument wrapper (every other test in this package) the
// Record* calls are no-ops, so Backend behaves exactly as before.
func TestBackendEvents_UninstrumentedBackendIsUnaffected(t *testing.T) {
	b := New(&fakeRunner{handle: func(string) (string, error) { return envelope(okThread), nil }})
	if _, err := b.Show(context.Background(), "1726000000.000100"); err != nil {
		t.Fatal(err)
	}
}
