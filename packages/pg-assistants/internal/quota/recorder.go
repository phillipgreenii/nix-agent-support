package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

// Querier is the part of the recorder client the quota reads; *client.Client
// and clienttest.Fake both satisfy it.
type Querier interface {
	Query(ctx context.Context, req client.QueryRequest) (*client.QueryAnswer, error)
}

// Recorder reads usage totals from the recorder with one Query per call.
type Recorder struct{ C Querier }

// Totals sums, per model, the usage counters of the assistant records whose
// cwd is dir or below it and whose time is at or after since. Each message
// counts once (distinct message.id), because the transcript repeats a message
// on one line per content block and on a resumed or forked session. Records
// written after an assistant cd's outside dir escape the count.
func (r Recorder) Totals(ctx context.Context, dir string, since time.Time) (map[string]Tokens, error) {
	ans, err := r.C.Query(ctx, client.QueryRequest{
		From: "records",
		Where: &client.Filter{All: []client.Filter{
			{Field: "type", Eq: json.RawMessage(`"assistant"`)},
			{Field: "cwd", Under: dir},
			{Field: "time", Range: &client.Range{Gte: since.UTC().Format(time.RFC3339Nano)}},
		}},
		Group: []string{"model"},
		Aggregate: []client.Agg{
			{Fn: "sum", Field: "usage.input_tokens", Distinct: "message.id", As: "in"},
			{Fn: "sum", Field: "usage.output_tokens", Distinct: "message.id", As: "out"},
			{Fn: "sum", Field: "usage.cache_creation_input_tokens", Distinct: "message.id", As: "cw"},
			{Fn: "sum", Field: "usage.cache_read_input_tokens", Distinct: "message.id", As: "cr"},
		},
		Limit: 1000,
	})
	if err != nil {
		return nil, err
	}
	if ans.Degraded {
		return nil, fmt.Errorf("recorder is degraded: totals may be incomplete")
	}
	out := map[string]Tokens{}
	for _, row := range ans.Rows {
		if len(row) < 5 {
			return nil, fmt.Errorf("recorder row has %d columns, want 5", len(row))
		}
		model, _ := row[0].(string)
		t := out[model]
		t.Input += num(row[1])
		t.Output += num(row[2])
		t.CacheCreation += num(row[3])
		t.CacheRead += num(row[4])
		out[model] = t
	}
	return out, nil
}

func num(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

// Dialing is a Source that connects to the recorder on every call, so a
// recorder that is down or restarted is picked up by the next tick without
// any state to repair. Socket is the recorder socket; empty means the
// recorder's default discovery (its environment variable, then the state home).
type Dialing struct{ Socket string }

// Totals dials the recorder and reads the totals through Recorder.
func (d Dialing) Totals(ctx context.Context, dir string, since time.Time) (map[string]Tokens, error) {
	c, err := client.Dial(ctx, client.Options{Socket: d.Socket, Name: "pg-assistants"})
	if err != nil {
		return nil, fmt.Errorf("recorder: %w", err)
	}
	return Recorder{C: c}.Totals(ctx, dir, since)
}
