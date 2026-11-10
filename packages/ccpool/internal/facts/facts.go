// Package facts is ccpool's reader of Claude Code facts: a policy-only client of
// the recorder (docket pg2-v77tsx, phase 9b). A turn started, a reply text,
// output tokens, whether the original transcript files are present (a session is
// resumable) and whether a Process is alive all come from the recorder's queries
// and change feed instead of whole-file re-reads.
//
// The recorder reports facts only. Interpretations (awaiting input, a reply,
// "the model started a turn") are made here, in ccpool.
//
// There is no fallback to Claude's files: with the recorder down every read
// fails with an error matching ErrDaemonOffline. Control (pg-claude-control) and
// tmux do not use this package and are unaffected.
package facts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

// ErrDaemonOffline is what a fact read reports when the recorder does not
// answer. Its text names the recorder so it is never read as another daemon
// being down, and a failed read never answers with a guess.
var ErrDaemonOffline = errors.New("the recorder is not available")

// Reader reads facts from one recorder daemon. It is safe for concurrent use.
type Reader struct {
	opts client.Options

	mu sync.Mutex
	c  *client.Client

	pingMu sync.Mutex
	pinged bool // a PingOnce succeeded: the recorder answered earlier in this process
}

// New returns a Reader. name is the per-client metric label sent to the daemon.
func New(name string) *Reader { return NewWith(client.Options{Name: name}) }

// NewWith returns a Reader with explicit client options (a test seam: Socket).
func NewWith(opts client.Options) *Reader { return &Reader{opts: opts} }

// wrapErr maps a client error to ErrDaemonOffline when the daemon does not answer.
func wrapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, client.ErrDown) || errors.Is(err, client.ErrUnavailable) {
		return fmt.Errorf("%w: %v", ErrDaemonOffline, err)
	}
	return err
}

func (r *Reader) conn(ctx context.Context) (*client.Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.c != nil {
		return r.c, nil
	}
	c, err := client.Dial(ctx, r.opts)
	if err != nil {
		return nil, wrapErr(err)
	}
	r.c = c
	return c, nil
}

// reset drops the cached client so the next read dials again.
func (r *Reader) reset() {
	r.mu.Lock()
	r.c = nil
	r.mu.Unlock()
}

func (r *Reader) query(ctx context.Context, req client.QueryRequest) (*client.QueryAnswer, error) {
	c, err := r.conn(ctx)
	if err != nil {
		return nil, err
	}
	ans, err := c.Query(ctx, req)
	if err != nil {
		if werr := wrapErr(err); errors.Is(werr, ErrDaemonOffline) {
			r.reset()
			return nil, werr
		}
		return nil, err
	}
	return ans, nil
}

// Ping reports nil when the recorder answers and ErrDaemonOffline when it does
// not. It is the capacity and dispatch gate: ccpool refuses to dispatch when it
// cannot check.
func (r *Reader) Ping(ctx context.Context) error {
	c, err := r.conn(ctx)
	if err != nil {
		return err
	}
	if _, err := c.Hello(ctx); err != nil {
		r.reset()
		return wrapErr(err)
	}
	return nil
}

// PingOnce is Ping for a process that gates many times (a multi-pool metrics run
// builds one service per pool, each consulting the gate): the first success is
// remembered, so later calls send no further hello. A failure is not remembered.
// Dial already performs a hello, so a run costs one or two, not one per pool.
func (r *Reader) PingOnce(ctx context.Context) error {
	r.pingMu.Lock()
	defer r.pingMu.Unlock()
	if r.pinged {
		return nil
	}
	if err := r.Ping(ctx); err != nil {
		return err
	}
	r.pinged = true
	return nil
}

// SessionID is the Claude session id of a transcript path: the file name without
// ".jsonl" (the recorder's file_session capture).
func SessionID(transcriptPath string) string {
	return strings.TrimSuffix(filepath.Base(transcriptPath), ".jsonl")
}

func eq(field string, v any) client.Filter {
	b, _ := json.Marshal(v)
	return client.Filter{Field: field, Eq: b}
}

func exists(field string, yes bool) client.Filter {
	return client.Filter{Field: field, Exists: &yes}
}

// mainSession selects the records of the session's own transcript (not a
// subagent's, whose lines carry the parent's session id).
func mainSession(id string, more ...client.Filter) *client.Filter {
	all := []client.Filter{eq("session", id), exists("agent_id", false)}
	all = append(all, more...)
	return &client.Filter{All: all}
}

func isMessage() client.Filter {
	return client.Filter{Any: []client.Filter{eq("type", "user"), eq("type", "assistant")}}
}

// LastAssistantText is the concatenated text of the last assistant line that has
// text ("" when there is none).
func (r *Reader) LastAssistantText(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ans, err := r.query(ctx, client.QueryRequest{
		Where:  mainSession(SessionID(path), eq("type", "text"), exists("assistant_text", true)),
		Select: []string{"time"},
		Order:  []client.Order{{By: "time", Dir: "desc"}},
		Limit:  1,
		Raw:    true,
	})
	if err != nil {
		return "", err
	}
	if len(ans.Raw) == 0 {
		return "", nil
	}
	return assistantText(ans.Raw[0]), nil
}

// assistantText concatenates the text blocks of one raw assistant line.
func assistantText(line string) string {
	var l struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(line), &l) != nil {
		return ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(l.Message.Content, &blocks) != nil {
		var s string
		if json.Unmarshal(l.Message.Content, &s) == nil {
			return s
		}
		return ""
	}
	var b strings.Builder
	for _, bl := range blocks {
		if bl.Type == "text" {
			b.WriteString(bl.Text)
		}
	}
	return b.String()
}

// IsAwaitingInput reports whether the session has an AskUserQuestion tool_use
// with no tool_result yet, asked after the last assistant record that is not a
// question (the facts are the two record kinds; the verdict is ccpool's).
func (r *Reader) IsAwaitingInput(path string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id := SessionID(path)
	asked, err := r.query(ctx, client.QueryRequest{
		Where:  mainSession(id, eq("type", "tool_use"), eq("name", "AskUserQuestion")),
		Select: []string{"tool_use_id", "time"},
		Limit:  1000,
	})
	if err != nil {
		return false, err
	}
	if len(asked.Rows) == 0 {
		return false, nil
	}
	answered, err := r.query(ctx, client.QueryRequest{
		Where:  mainSession(id, eq("type", "tool_result")),
		Select: []string{"tool_use_id"},
		Limit:  10000,
	})
	if err != nil {
		return false, err
	}
	done := map[string]bool{}
	for _, row := range answered.Rows {
		if s, ok := row[0].(string); ok {
			done[s] = true
		}
	}
	for _, row := range asked.Rows {
		if s, ok := row[0].(string); ok && s != "" && !done[s] {
			return true, nil
		}
	}
	return false, nil
}

// FirstMessageActivity is the time of the newest real (user or assistant)
// message, and whether one exists. A read that fails reports (zero, false): the
// caller treats "no turn observed" as the safe direction; use MessageActivity to
// see the error.
func (r *Reader) FirstMessageActivity(path string) (time.Time, bool) {
	t, ok, err := r.MessageActivity(path)
	if err != nil {
		return time.Time{}, false
	}
	return t, ok
}

// MessageActivity is FirstMessageActivity with the read error.
func (r *Reader) MessageActivity(path string) (time.Time, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	notErr := client.Filter{Any: []client.Filter{exists("is_api_error_message", false), eq("is_api_error_message", false)}}
	ans, err := r.query(ctx, client.QueryRequest{
		Where:  mainSession(SessionID(path), isMessage(), notErr),
		Select: []string{"time"},
		Order:  []client.Order{{By: "time", Dir: "desc"}},
		Limit:  1,
	})
	if err != nil {
		return time.Time{}, false, err
	}
	if len(ans.Rows) == 0 || len(ans.Rows[0]) == 0 {
		return time.Time{}, false, nil
	}
	s, _ := ans.Rows[0][0].(string)
	t, perr := time.Parse(time.RFC3339Nano, s)
	if perr != nil {
		return time.Time{}, false, nil
	}
	return t, true, nil
}

// OutputTokens is the total output_tokens of the session, counted once per
// distinct assistant message.
func (r *Reader) OutputTokens(path string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ans, err := r.query(ctx, client.QueryRequest{
		Where: mainSession(SessionID(path), eq("type", "assistant")),
		Aggregate: []client.Agg{
			{Fn: "sum", Field: "usage.output_tokens", Distinct: "message.id", As: "out"},
		},
	})
	if err != nil {
		return 0, err
	}
	if len(ans.Rows) == 0 || len(ans.Rows[0]) == 0 {
		return 0, nil
	}
	switch v := ans.Rows[0][0].(type) {
	case float64:
		return int64(v), nil
	case json.Number:
		n, _ := v.Int64()
		return n, nil
	}
	return 0, nil
}

// OriginalsPresent reports whether the session's original transcript files are
// still in the Claude directory: what makes a Conversation resumable. A session
// the recorder has never seen is not resumable.
func (r *Reader) OriginalsPresent(transcriptPath string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := r.conn(ctx)
	if err != nil {
		return false, err
	}
	conv, err := c.Conversation(ctx, SessionID(transcriptPath))
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			return false, nil
		}
		if werr := wrapErr(err); errors.Is(werr, ErrDaemonOffline) {
			r.reset()
			return false, werr
		}
		return false, err
	}
	return conv.OriginalsExist, nil
}

// ProcessAlive reports whether the Process (pid and its start time as the
// registry file names it) is alive now. It replaces the inline pid and
// start-time check of the registry reader.
func (r *Reader) ProcessAlive(pid int64, procStart string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := r.conn(ctx)
	if err != nil {
		return false, err
	}
	p, err := c.Process(ctx, pid, procStart)
	if err != nil {
		if errors.Is(err, client.ErrNotFound) {
			return false, nil
		}
		if werr := wrapErr(err); errors.Is(werr, ErrDaemonOffline) {
			r.reset()
			return false, werr
		}
		return false, err
	}
	return p.Alive, nil
}

// Mark returns the feed position now: a later WaitMessage sees only records
// written after it.
func (r *Reader) Mark(ctx context.Context) (string, error) {
	ans, err := r.query(ctx, client.QueryRequest{Select: []string{"type"}, Limit: 1})
	if err != nil {
		return "", err
	}
	return ans.AsOf, nil
}

// ErrFeedUnanswered is what WaitMessage reports when the window passed and the
// recorder's feed was not answering at the end of it (busy, unavailable or a
// read deadline on every page since the last good one, or never a good page).
// A window with no record proves "not ingested" only while the recorder was
// answering the feed: a feed that kept failing would look the same, so the
// caller must treat this as "unknown", never as "not ingested".
var ErrFeedUnanswered = errors.New("the recorder feed did not answer within the window")

var errFeedDegraded = errors.New("the recorder is behind: its feed page was marked degraded")

// feedRetryPause is the gap before asking the feed again after a retryable
// failure (busy, unavailable, query timeout).
const feedRetryPause = 500 * time.Millisecond

// feedGrace is how long past the window the last feed request may run.
const feedGrace = 2 * time.Second

// WaitMessage waits on the change feed, until window elapses, for a real user or
// assistant message of the session written after mark. It returns (false, nil)
// only when the window passed with no such record AND the feed's last answer was
// a successful page: the recorder answered that nothing matched. When retryable
// failures (busy, unavailable, query timeout) were still the latest outcome at
// the end of the window, or no page ever succeeded, it returns an error matching
// ErrFeedUnanswered (the iterator would have retried them silently, so the
// window's end would have read as a definitive "no record").
func (r *Reader) WaitMessage(ctx context.Context, transcriptPath, mark string, window time.Duration) (bool, error) {
	c, err := r.conn(ctx)
	if err != nil {
		return false, err
	}
	deadline := time.Now().Add(window)
	filter := mainSession(SessionID(transcriptPath), isMessage())
	cursor := mark
	answered := false // the latest completed page succeeded
	var lastErr error
	for time.Now().Before(deadline) {
		wait := time.Until(deadline)
		waitMS := int(wait / time.Millisecond)
		if waitMS < 1 {
			waitMS = 1
		}
		if waitMS > 30000 {
			waitMS = 30000
		}
		// The request outlives the window by a grace, so the last long poll
		// completes (an empty page: the recorder answered that nothing matched)
		// instead of being cut by the deadline, which would leave no answer.
		rctx, cancel := context.WithTimeout(ctx, wait+feedGrace)
		ans, err := c.FeedPage(rctx, filter, cursor, []string{"type"}, 0, waitMS)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			if werr := wrapErr(err); errors.Is(werr, ErrDaemonOffline) {
				r.reset()
				return false, werr
			}
			if !client.Retryable(err) && rctx.Err() == nil {
				return false, err
			}
			answered, lastErr = false, err
			pause := feedRetryPause
			if left := time.Until(deadline); left < pause {
				pause = left
			}
			if pause > 0 {
				select {
				case <-ctx.Done():
					return false, ctx.Err()
				case <-time.After(pause):
				}
			}
			continue
		}
		for _, en := range ans.Entries {
			if en.Kind == "record" {
				return true, nil
			}
		}
		// A degraded page (the recorder is behind or backfilling) that holds no
		// record is not an answer that nothing matched: the record may not be
		// indexed yet.
		if ans.Degraded {
			answered, lastErr = false, errFeedDegraded
		} else {
			answered, lastErr = true, nil
		}
		if ans.More && len(ans.Entries) > 0 {
			cursor = ans.Entries[len(ans.Entries)-1].Seq
		} else {
			cursor = ans.NextCursor
		}
	}
	if !answered {
		if lastErr != nil {
			return false, fmt.Errorf("%w: %v", ErrFeedUnanswered, lastErr)
		}
		return false, ErrFeedUnanswered
	}
	return false, nil
}
