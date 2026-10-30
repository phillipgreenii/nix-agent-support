package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

// ErrDaemonOffline is what a RecorderReader reports when the recorder does not
// answer. There is no fallback to Claude's files (docket pg2-v77tsx): the
// watchdog treats a failed read as "no new usage this poll" and polls again.
var ErrDaemonOffline = errors.New("daemon offline")

// RecorderReader is a Reader backed by the recorder's queries instead of a
// whole-file re-read: one aggregate query per poll, each assistant message
// counted once by its id (the same rule as the transcript reader).
type RecorderReader struct {
	opts client.Options
}

// NewRecorderReader returns a RecorderReader. The zero Options discover the
// socket from the environment, then the default path.
func NewRecorderReader(opts client.Options) *RecorderReader {
	if opts.Name == "" {
		opts.Name = "pg-router-handler-ccpool"
	}
	return &RecorderReader{opts: opts}
}

func offline(err error) error {
	if errors.Is(err, client.ErrDown) || errors.Is(err, client.ErrUnavailable) {
		return fmt.Errorf("%w: %v", ErrDaemonOffline, err)
	}
	return err
}

func sessionOf(path string) string { return strings.TrimSuffix(filepath.Base(path), ".jsonl") }

func eqf(field string, v any) client.Filter {
	b, _ := json.Marshal(v)
	return client.Filter{Field: field, Eq: b}
}

func mainSession(id string, more ...client.Filter) *client.Filter {
	no := false
	all := []client.Filter{eqf("session", id), {Field: "agent_id", Exists: &no}}
	return &client.Filter{All: append(all, more...)}
}

// Read implements Reader. A transcript the recorder has no records for yet is a
// zero Snapshot (the worker has not produced one).
func (r *RecorderReader) Read(ctx context.Context, transcriptPath string) (Snapshot, error) {
	if transcriptPath == "" {
		return Snapshot{}, nil
	}
	c, err := client.Dial(ctx, r.opts)
	if err != nil {
		return Snapshot{}, offline(err)
	}
	id := sessionOf(transcriptPath)
	sum := func(field, as string) client.Agg {
		return client.Agg{Fn: "sum", Field: field, Distinct: "message.id", As: as}
	}
	ans, err := c.Query(ctx, client.QueryRequest{
		Where: mainSession(id, eqf("type", "assistant")),
		Aggregate: []client.Agg{
			sum("usage.input_tokens", "in"),
			sum("usage.cache_creation_input_tokens", "cc"),
			sum("usage.cache_read_input_tokens", "cr"),
			sum("usage.output_tokens", "out"),
			sum("usage.cache_creation.ephemeral_1h_input_tokens", "e1h"),
			sum("usage.cache_creation.ephemeral_5m_input_tokens", "e5m"),
		},
	})
	if err != nil {
		return Snapshot{}, offline(err)
	}
	var s Snapshot
	if len(ans.Rows) > 0 && len(ans.Rows[0]) >= 6 {
		n := func(i int) int {
			if f, ok := ans.Rows[0][i].(float64); ok {
				return int(f)
			}
			return 0
		}
		s = Snapshot{
			InputTokens: n(0), CacheCreationTokens: n(1), CacheReadTokens: n(2), OutputTokens: n(3),
			CacheCreationEphemeral1hTokens: n(4), CacheCreationEphemeral5mTokens: n(5),
		}
	}
	model, err := c.Query(ctx, client.QueryRequest{
		Where:  mainSession(id, eqf("type", "assistant"), client.Filter{Field: "model", Exists: boolp(true)}),
		Select: []string{"model"},
		Order:  []client.Order{{By: "time", Dir: "desc"}},
		Limit:  1,
	})
	if err != nil {
		return Snapshot{}, offline(err)
	}
	if len(model.Rows) > 0 && len(model.Rows[0]) > 0 {
		s.Model, _ = model.Rows[0][0].(string)
	}
	return s, nil
}

func boolp(b bool) *bool { return &b }

// FirstTurnStarted reports whether the model has taken a turn in the transcript:
// a real user or assistant message exists. A failed read reports false (no turn
// observed: the safe direction for the nudge gate).
func (r *RecorderReader) FirstTurnStarted(transcriptPath string) bool {
	if transcriptPath == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, r.opts)
	if err != nil {
		return false
	}
	ans, err := c.Query(ctx, client.QueryRequest{
		Where: mainSession(sessionOf(transcriptPath), client.Filter{Any: []client.Filter{eqf("type", "user"), eqf("type", "assistant")}}),
		Limit: 1,
	})
	return err == nil && len(ans.Rows) > 0
}
