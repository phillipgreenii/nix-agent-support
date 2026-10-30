package recorderfacts

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

// SessionSummary is one session of the `sessions` listing, read from the
// recorder's transcript records.
type SessionSummary struct {
	SessionID      string
	Cwd            string
	Branch         string
	Model          string
	StartedAt      time.Time
	EndedAt        time.Time
	UserTurns      int
	AssistantTurns int
	FirstPrompt    string
	// Tokens is the sum over models; ModelTokens is per model (for pricing).
	ModelTokens map[string]Tokens
}

// Tokens is a token tally.
type Tokens struct{ Input, Output, CacheRead, CacheWrite int64 }

func transcriptSource() client.Filter {
	return client.Filter{Field: "source", Eq: json.RawMessage(`"claude-transcript"`)}
}

func typeIs(t string) client.Filter {
	return client.Filter{Field: "type", Eq: json.RawMessage(fmt.Sprintf("%q", t))}
}

func num(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case json.Number:
		n, _ := x.Int64()
		return n
	}
	return 0
}

func parseTime(v any) time.Time {
	s, _ := v.(string)
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// Sessions lists the sessions whose first main-thread record is in
// [since, before) (a zero bound is open), oldest first. It asks the recorder
// and reads no transcript file; a recorder that is not answering is
// session.ErrDaemonOffline.
func (f *Facts) Sessions(ctx context.Context, since, before time.Time) ([]SessionSummary, error) {
	c, err := f.Client(ctx)
	if err != nil {
		return nil, err
	}
	run := func(req client.QueryRequest) (*client.QueryAnswer, error) {
		req.From = "records"
		a, err := c.Query(ctx, req)
		return a, f.wrap(err)
	}
	main := client.Filter{Field: "is_sidechain", Eq: json.RawMessage("false")}
	turns := client.Filter{Any: []client.Filter{typeIs("user"), typeIs("assistant")}}
	base := []client.Filter{transcriptSource(), turns, main}

	// First and last record time per session.
	span, err := run(client.QueryRequest{
		Where:     &client.Filter{All: base},
		Group:     []string{"session"},
		Aggregate: []client.Agg{{Fn: "min", Field: "time", As: "first"}, {Fn: "max", Field: "time", As: "last"}},
		Limit:     10000,
	})
	if err != nil {
		return nil, err
	}
	byID := map[string]*SessionSummary{}
	var ids []any
	for _, r := range span.Rows {
		sid, _ := r[0].(string)
		first, last := parseTime(r[1]), parseTime(r[2])
		if sid == "" || first.IsZero() || (!since.IsZero() && first.Before(since)) || (!before.IsZero() && !first.Before(before)) {
			continue
		}
		byID[sid] = &SessionSummary{SessionID: sid, StartedAt: first, EndedAt: last, ModelTokens: map[string]Tokens{}}
		ids = append(ids, sid)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	inSessions := client.Filter{Field: "session", In: ids}
	where := func(extra ...client.Filter) *client.Filter {
		return &client.Filter{All: append([]client.Filter{transcriptSource(), main, inSessions}, extra...)}
	}

	// User turns are the user lines of the main thread (tool results arrive as
	// user lines too, so this counts them as well).
	uc, err := run(client.QueryRequest{Where: where(typeIs("user"), lineRecord), Group: []string{"session"}, Aggregate: []client.Agg{{Fn: "count", As: "n"}}, Limit: 10000})
	if err != nil {
		return nil, err
	}
	for _, r := range uc.Rows {
		if s := byID[str(r[0])]; s != nil {
			s.UserTurns = int(num(r[1]))
		}
	}
	// Assistant turns, models and tokens, de-duplicated by message id.
	ac, err := run(client.QueryRequest{
		Where: where(typeIs("assistant")),
		Group: []string{"session", "model"},
		Aggregate: []client.Agg{
			{Fn: "count", Field: "message.id", Distinct: "message.id", As: "turns"},
			{Fn: "sum", Field: "usage.input_tokens", Distinct: "message.id", As: "in"},
			{Fn: "sum", Field: "usage.output_tokens", Distinct: "message.id", As: "out"},
			{Fn: "sum", Field: "usage.cache_read_input_tokens", Distinct: "message.id", As: "cr"},
			{Fn: "sum", Field: "usage.cache_creation_input_tokens", Distinct: "message.id", As: "cw"},
		},
		Limit: 10000,
	})
	if err != nil {
		return nil, err
	}
	bestTurns := map[string]int64{}
	for _, r := range ac.Rows {
		s := byID[str(r[0])]
		if s == nil {
			continue
		}
		model := str(r[1])
		turns := num(r[2])
		s.AssistantTurns += int(turns)
		if model != "" {
			s.ModelTokens[model] = Tokens{Input: num(r[3]), Output: num(r[4]), CacheRead: num(r[5]), CacheWrite: num(r[6])}
			if turns > bestTurns[s.SessionID] {
				bestTurns[s.SessionID], s.Model = turns, model
			}
		}
	}
	// Cwd and branch of the first record; the first prompt.
	for _, sid := range ids {
		s := byID[sid.(string)]
		one, err := run(client.QueryRequest{
			Where:  &client.Filter{All: []client.Filter{transcriptSource(), main, {Field: "session", Eq: json.RawMessage(fmt.Sprintf("%q", s.SessionID))}}},
			Select: []string{"cwd", "git_branch"}, Order: []client.Order{{By: "time"}}, Limit: 1,
		})
		if err != nil {
			return nil, err
		}
		if len(one.Rows) == 1 {
			s.Cwd, s.Branch = str(one.Rows[0][0]), str(one.Rows[0][1])
		}
		fp, err := run(client.QueryRequest{
			Where:  &client.Filter{All: []client.Filter{transcriptSource(), main, typeIs("user"), {Field: "session", Eq: json.RawMessage(fmt.Sprintf("%q", s.SessionID))}, lineRecord}},
			Select: []string{"session"}, Order: []client.Order{{By: "time"}}, Limit: 1, Raw: true,
		})
		if err != nil {
			return nil, err
		}
		if len(fp.Raw) == 1 {
			s.FirstPrompt = promptOf(fp.Raw[0])
		}
	}
	out := make([]SessionSummary, 0, len(byID))
	for _, s := range byID {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.Before(out[j].StartedAt)
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out, nil
}

// lineRecord selects the one record per transcript line (its blocks are item 0..).
var lineRecord = client.Filter{Field: "item", Eq: json.RawMessage("-1")}

func str(v any) string { s, _ := v.(string); return s }

// SearchHit is one match of a search.
type SearchHit struct {
	SessionID string
	Role      string
	Snippet   string
	Time      time.Time
}

// Search finds query in the transcripts of the recorder's records (word mode),
// optionally for one session and within [since, before). Newest matches are the
// recorder's ranking; a recorder that is not answering is session.ErrDaemonOffline.
func (f *Facts) Search(ctx context.Context, query, sessionID string, since, before time.Time) ([]SearchHit, error) {
	c, err := f.Client(ctx)
	if err != nil {
		return nil, err
	}
	all := []client.Filter{transcriptSource()}
	if sessionID != "" {
		all = append(all, client.Filter{Field: "session", Eq: json.RawMessage(fmt.Sprintf("%q", sessionID))})
	}
	if !since.IsZero() || !before.IsZero() {
		r := &client.Range{}
		if !since.IsZero() {
			r.Gte = since.UTC().Format(time.RFC3339)
		}
		if !before.IsZero() {
			r.Lt = before.UTC().Format(time.RFC3339)
		}
		all = append(all, client.Filter{Field: "time", Range: r})
	}
	ans, err := c.Search(ctx, client.SearchRequest{Mode: "word", Q: query, Where: &client.Filter{All: all}, Fields: []string{"assistant_text", "user_text"}, Limit: 200})
	if err != nil {
		return nil, f.wrap(err)
	}
	var out []SearchHit
	for _, h := range ans.Hits {
		role := "assistant"
		if strings.HasPrefix(h.Field, "user") {
			role = "user"
		}
		t, _ := time.Parse(time.RFC3339Nano, h.Time)
		out = append(out, SearchHit{SessionID: h.SessionID, Role: role, Snippet: h.Snippet, Time: t})
	}
	return out, nil
}

// promptOf is the text of a user transcript line (message.content is a string
// or a list of blocks).
func promptOf(line string) string {
	var l struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(line), &l) != nil {
		return ""
	}
	var text string
	if json.Unmarshal(l.Message.Content, &text) == nil {
		return strings.TrimSpace(text)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(l.Message.Content, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	}
	return ""
}
