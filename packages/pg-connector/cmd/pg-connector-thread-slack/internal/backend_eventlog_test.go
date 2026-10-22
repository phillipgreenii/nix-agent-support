package internal

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
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

// ---------------------------------------------------------------------
// Bead pg2-xrbik: item count, num_turns and cost on list/show rows.
// ---------------------------------------------------------------------

// costEnvelope is envelope() plus the claude -p result envelope's own
// num_turns and total_cost_usd fields.
func costEnvelope(inner string, turns int, cost string) string {
	return `{"result":` + toJSONString(inner) + `,"is_error":false,"num_turns":` + strconv.Itoa(turns) + `,"total_cost_usd":` + cost + `}`
}

// rowOf serializes ev exactly as the file sink does and decodes it back, so
// the assertions see the numbers a LogQL reader sees.
func rowOf(t *testing.T, ev eventlog.Event) map[string]any {
	t.Helper()
	line, err := eventlog.Line(ev)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatalf("row is not JSON: %v", err)
	}
	return m
}

func wantNumbers(t *testing.T, row map[string]any, want map[string]float64) {
	t.Helper()
	for k, w := range want {
		if got, ok := row[k]; !ok || got != w {
			t.Errorf("row[%q] = %v (present %v), want %v; row = %v", k, got, ok, w, row)
		}
	}
}

func TestBackendEvents_ShowOkRowCarriesItemCountTurnsAndCost(t *testing.T) {
	sink := &captureSink{}
	table := instrumented(New(&fakeRunner{handle: func(string) (string, error) {
		return costEnvelope(okThread, 7, "0.125"), nil
	}}), sink)
	if _, err := table["show"].Handle(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	row := rowOf(t, sink.last(t))
	if row["op"] != "show" || row["msg"] == nil {
		t.Fatalf("row = %v", row)
	}
	wantNumbers(t, row, map[string]float64{
		"claude_calls": 1, "item_count": 1, "num_turns": 7, "max_num_turns": 7, "cost_usd": 0.125,
	})
}

func TestBackendEvents_ListOkRowSumsAcrossModifiersAndCountsDedupedItems(t *testing.T) {
	sink := &captureSink{}
	n := 0
	table := instrumented(New(&fakeRunner{handle: func(string) (string, error) {
		n++
		if n == 1 {
			return costEnvelope(`{"items":[{"id":"C01AAAAAAAA_1726000000.000100","channel":"C01AAAAAAAA","permalink":"https://x.invalid/1"},{"id":"C01AAAAAAAA_1726000000.000200","channel":"C01AAAAAAAA","permalink":"https://x.invalid/2"}]}`, 5, "0.25"), nil
		}
		// the first id repeats across modifiers and the empty id is skipped, so this
		// call adds exactly one item (the C01BBBBBBBB one).
		return costEnvelope(`{"items":[{"id":"C01AAAAAAAA_1726000000.000100","channel":"C01AAAAAAAA","permalink":"https://x.invalid/1"},{"id":"","channel":"C01AAAAAAAA","permalink":"https://x.invalid/9"},{"id":"C01BBBBBBBB_1726000000.000300","channel":"C01BBBBBBBB","permalink":"https://x.invalid/3"}]}`, 9, "0.5"), nil
	}}), sink)
	res, err := table["list"].Handle(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(res.(*schema.ThreadListResult).Entities); got != 3 {
		t.Fatalf("entities = %d, want 3", got)
	}
	row := rowOf(t, sink.last(t))
	wantNumbers(t, row, map[string]float64{
		"claude_calls": 2, "item_count": 3, "num_turns": 14, "max_num_turns": 9, "cost_usd": 0.75,
	})
}

// An empty list, and an envelope that predates the fields, still log numeric
// zeros: a reader must be able to tell "0" from "not logged".
func TestBackendEvents_ListAndShowRowsLogZeroNotAbsent(t *testing.T) {
	sink := &captureSink{}
	table := instrumented(New(&fakeRunner{handle: func(string) (string, error) {
		return envelope(`{"items":[]}`), nil
	}}), sink)
	if _, err := table["list"].Handle(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	wantNumbers(t, rowOf(t, sink.last(t)), map[string]float64{"item_count": 0, "num_turns": 0, "max_num_turns": 0, "cost_usd": 0})

	notFound := instrumented(New(&fakeRunner{handle: func(string) (string, error) {
		return costEnvelope(`{"found":false}`, 2, "0.01"), nil
	}}), sink)
	if _, err := notFound["show"].Handle(context.Background(), nil); !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	// not_found returned no item but still spent turns and money.
	wantNumbers(t, rowOf(t, sink.last(t)), map[string]float64{"item_count": 0, "num_turns": 2, "max_num_turns": 2, "cost_usd": 0.01})
}

// error_max_turns arrives as an is_error envelope: its turn count is the
// reading the max_turns limit is judged by, so a failed call still logs it.
func TestBackendEvents_IsErrorEnvelopeStillLogsTurnsAndCost(t *testing.T) {
	sink := &captureSink{}
	table := instrumented(New(&fakeRunner{handle: func(string) (string, error) {
		return `{"result":"max turns","is_error":true,"subtype":"error_max_turns","num_turns":17,"total_cost_usd":0.4}`, nil
	}}), sink)
	if _, err := table["show"].Handle(context.Background(), nil); !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	wantNumbers(t, rowOf(t, sink.last(t)), map[string]float64{"item_count": 0, "num_turns": 17, "max_num_turns": 17, "cost_usd": 0.4})
}
