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

	// The sessions to consider come from the recorder's per-file inventory, not
	// from a scan of records (which timed out on a real tree). A session whose
	// first record is in [since, before) has a record in that window, so with a
	// lower bound the inventory narrows to the sessions active in it (one that
	// began before since still reports its true first record and is dropped by
	// the start-time check below).
	act, ok, err := f.activeSessions(ctx, c, since)
	if err != nil {
		return nil, err
	}
	if !ok {
		// The inventory cannot be trusted (the recorder reports it degraded):
		// count records, in the window when there is one.
		where := base
		if win := windowFilter(since, before); win != nil {
			where = append(append([]client.Filter{}, base...), *win)
		}
		active, err := run(client.QueryRequest{
			Where: &client.Filter{All: where},
			Group: []string{"session"}, Aggregate: []client.Agg{{Fn: "count", As: "n"}}, Limit: 10000,
		})
		if err != nil {
			return nil, err
		}
		act = nil
		for _, r := range active.Rows {
			if sid := str(r[0]); sid != "" {
				act = append(act, sid)
			}
		}
	}

	// First and last record time per session: one ordered single-row read per
	// session (the recorder serves a query pinned to a session by its index),
	// never a min/max group over records, which scanned every record of every
	// listed session and timed out.
	timeOf := func(sid any, dir string) (time.Time, error) {
		one, err := run(client.QueryRequest{
			Where:  &client.Filter{All: append(append([]client.Filter{}, base...), client.Filter{Field: "session", Eq: json.RawMessage(fmt.Sprintf("%q", sid))})},
			Select: []string{"time"}, Order: []client.Order{{By: "time", Dir: dir}}, Limit: 1,
		})
		if err != nil || len(one.Rows) != 1 {
			return time.Time{}, err
		}
		return parseTime(one.Rows[0][0]), nil
	}
	byID := map[string]*SessionSummary{}
	var ids []any
	for _, sid := range act {
		first, err := timeOf(sid, "asc")
		if err != nil {
			return nil, err
		}
		if first.IsZero() || (!since.IsZero() && first.Before(since)) || (!before.IsZero() && !first.Before(before)) {
			continue
		}
		last, err := timeOf(sid, "desc")
		if err != nil {
			return nil, err
		}
		id := sid.(string)
		byID[id] = &SessionSummary{SessionID: id, StartedAt: first, EndedAt: last, ModelTokens: map[string]Tokens{}}
		ids = append(ids, sid)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	// Each session's counts are read by a query pinned to that one session (the
	// recorder serves those by its session index), not by one group over every
	// listed session, which read all their records at once and timed out.
	pinned := func(sid string) client.Filter {
		return client.Filter{Field: "session", Eq: json.RawMessage(fmt.Sprintf("%q", sid))}
	}
	where := func(sid string, extra ...client.Filter) *client.Filter {
		return &client.Filter{All: append([]client.Filter{transcriptSource(), main, pinned(sid)}, extra...)}
	}
	for _, id := range ids {
		s := byID[id.(string)]
		// User turns are the user lines of the main thread (tool results arrive
		// as user lines too, so this counts them as well).
		uc, err := run(client.QueryRequest{Where: where(s.SessionID, typeIs("user"), lineRecord), Aggregate: []client.Agg{{Fn: "count", As: "n"}}, Limit: 10000})
		if err != nil {
			return nil, err
		}
		for _, r := range uc.Rows {
			s.UserTurns = int(num(r[0]))
		}
		// Assistant turns, models and tokens, de-duplicated by message id.
		ac, err := run(client.QueryRequest{
			Where: where(s.SessionID, typeIs("assistant")),
			Group: []string{"model"},
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
		var best int64
		for _, r := range ac.Rows {
			model := str(r[0])
			turns := num(r[1])
			s.AssistantTurns += int(turns)
			if model != "" {
				s.ModelTokens[model] = Tokens{Input: num(r[2]), Output: num(r[3]), CacheRead: num(r[4]), CacheWrite: num(r[5])}
				if turns > best {
					best, s.Model = turns, model
				}
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

// inventoryPage is the page size of the file inventory read.
const inventoryPage = 10000

// activeSessions lists the sessions with a transcript file whose newest record
// is at or after since (every transcript file when since is zero), from the
// recorder's per-file table (no scan of records, which timed out on a real
// tree). ok is false when the answer cannot be trusted (the recorder reports it
// degraded); the caller
// then counts records. The list is a superset of the sessions active in the
// window; the caller checks each session's true first record.
func (f *Facts) activeSessions(ctx context.Context, c *client.Client, since time.Time) (ids []any, ok bool, err error) {
	req := client.InventoryRequest{Sources: []string{"claude-transcript"}, Limit: inventoryPage}
	if !since.IsZero() {
		req.Since = since.UTC().Format(time.RFC3339)
	}
	seen := map[string]bool{}
	for {
		ans, err := c.FilesInventory(ctx, req)
		if err != nil {
			return nil, false, f.wrap(err)
		}
		if ans.Degraded {
			return nil, false, nil
		}
		for _, fl := range ans.Files {
			if fl.FileSession != "" && !seen[fl.FileSession] {
				seen[fl.FileSession] = true
				ids = append(ids, fl.FileSession)
			}
		}
		if !ans.More || len(ans.Files) == 0 {
			return ids, true, nil
		}
		req.After = ans.Files[len(ans.Files)-1].File
	}
}

// windowFilter is the record-time filter for [since, before); nil when open.
func windowFilter(since, before time.Time) *client.Filter {
	if since.IsZero() && before.IsZero() {
		return nil
	}
	r := &client.Range{}
	if !since.IsZero() {
		r.Gte = since.UTC().Format(time.RFC3339)
	}
	if !before.IsZero() {
		r.Lt = before.UTC().Format(time.RFC3339)
	}
	return &client.Filter{Field: "time", Range: r}
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

// Search finds query in the transcripts of the recorder's records (word mode, or
// substring mode when substring is set: the recorder scans its copies),
// optionally for one session and within [since, before). Newest matches are the
// recorder's ranking; a recorder that is not answering is session.ErrDaemonOffline.
func (f *Facts) Search(ctx context.Context, query, sessionID string, since, before time.Time, substring bool) ([]SearchHit, error) {
	c, err := f.Client(ctx)
	if err != nil {
		return nil, err
	}
	all := []client.Filter{transcriptSource()}
	if sessionID != "" {
		all = append(all, client.Filter{Field: "session", Eq: json.RawMessage(fmt.Sprintf("%q", sessionID))})
	}
	if win := windowFilter(since, before); win != nil {
		all = append(all, *win)
	}
	req := client.SearchRequest{Mode: "word", Q: query, Where: &client.Filter{All: all}, Fields: []string{"assistant_text", "user_text"}, Limit: 200}
	if substring {
		// The recorder's substring scan reads its copies of the opaque fields
		// (tool-result bodies); it names no prose field, so none is requested.
		req.Mode, req.Fields = "substring", nil
	}
	ans, err := c.Search(ctx, req)
	if err != nil {
		return nil, f.wrap(err)
	}
	var out []SearchHit
	for _, h := range ans.Hits {
		role := "assistant"
		if strings.HasPrefix(h.Field, "user") || strings.HasPrefix(h.Field, "tool_result") {
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
