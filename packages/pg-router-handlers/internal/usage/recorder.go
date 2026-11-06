package usage

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

// RecorderReader answers whether the model has taken a turn, from the
// recorder's facts. Token and cost consumption come from ccpool instead (see
// NewCcpoolReader); this type no longer reads them.
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
