package clienttest

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/wire"
)

// Fake is an in-memory client.API: seed its exported fields, call it like a
// client. It serves what it was given and the errors the real daemon gives for
// absent things (not_found, cursor_expired, bad_request); it does not run
// queries. Inject a failure for one method with Fail. Safe for concurrent use
// once seeded.
type Fake struct {
	// HelloAnswer is the identity returned; NewFake fills a protocol-1, epoch-1 hello
	// that lists every operation.
	HelloAnswer wire.Hello
	// Conversations and Processes seed conversation.* and process.*.
	Conversations []wire.Conversation
	Processes     []wire.Process
	// Entries is the feed log; entry i has the cursor "<epoch>:<i+1>".
	Entries []wire.Entry
	// Query, Search, Inventory and Facts are the canned answers of the matching
	// operations (empty when unset).
	QueryAnswer     wire.QueryAnswer
	SearchAnswer    wire.SearchAnswer
	InventoryAnswer wire.InventoryAnswer
	Facts           wire.SessionsFactsAnswer
	// Fields are the field names a filter may name; any other is bad_request.
	Fields []string
	// Fail maps a method name ("Query", "FeedPage", ...) to the error it returns.
	Fail map[string]error

	mu    sync.Mutex
	hello wire.Hello
}

var _ client.API = (*Fake)(nil)

// Operations is the capability list NewFake reports.
var Operations = wire.OpNames()

// NewFake returns an empty Fake.
func NewFake() *Fake {
	return &Fake{
		HelloAnswer: wire.Hello{
			Service: "pg-claude-code-recorder", Version: "fake", ProtocolVersion: wire.ProtocolVersion,
			SchemaVersion: 1, Epoch: 1, Capabilities: append([]string(nil), Operations...), CaughtUp: true,
		},
		Fields: []string{"type", "uuid", "session_id", "timestamp", "cwd", "model"},
	}
}

func (f *Fake) fail(method string) error {
	if e := f.Fail[method]; e != nil {
		return e
	}
	return nil
}

func apiErr(name, msg string) error {
	return client.NewError(name, msg, "")
}

// Hello returns HelloAnswer and remembers it for Capabilities.
func (f *Fake) Hello(context.Context) (wire.Hello, error) {
	if err := f.fail("Hello"); err != nil {
		return wire.Hello{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hello = f.HelloAnswer
	return f.hello, nil
}

func (f *Fake) caps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hello.Capabilities == nil {
		return f.HelloAnswer.Capabilities
	}
	return f.hello.Capabilities
}

// Capabilities returns the capability list.
func (f *Fake) Capabilities() []string { return f.caps() }

// HasCapability reports whether the capability list names op.
func (f *Fake) HasCapability(op string) bool {
	for _, x := range f.caps() {
		if x == op {
			return true
		}
	}
	return false
}

func (f *Fake) checkFilter(fl *wire.Filter) error {
	if fl == nil {
		return nil
	}
	if fl.Field != "" {
		ok := false
		for _, n := range f.Fields {
			ok = ok || n == fl.Field
		}
		if !ok {
			return apiErr(wire.ErrBadRequest, "unknown field "+fl.Field)
		}
	}
	for _, sub := range append(append([]wire.Filter(nil), fl.All...), fl.Any...) {
		if err := f.checkFilter(&sub); err != nil {
			return err
		}
	}
	return f.checkFilter(fl.Not)
}

// FilesInventory returns InventoryAnswer.
func (f *Fake) FilesInventory(_ context.Context, _ wire.InventoryRequest) (*wire.InventoryAnswer, error) {
	if err := f.fail("FilesInventory"); err != nil {
		return nil, err
	}
	a := f.InventoryAnswer
	return &a, nil
}

// Query returns QueryAnswer after validating the filter's fields.
func (f *Fake) Query(_ context.Context, req wire.QueryRequest) (*wire.QueryAnswer, error) {
	if err := f.fail("Query"); err != nil {
		return nil, err
	}
	if err := f.checkFilter(req.Where); err != nil {
		return nil, err
	}
	a := f.QueryAnswer
	return &a, nil
}

// Search returns SearchAnswer after validating the filter's fields.
func (f *Fake) Search(_ context.Context, req wire.SearchRequest) (*wire.SearchAnswer, error) {
	if err := f.fail("Search"); err != nil {
		return nil, err
	}
	if err := f.checkFilter(req.Where); err != nil {
		return nil, err
	}
	a := f.SearchAnswer
	return &a, nil
}

// Snapshot returns the feed log as one page at the head cursor.
func (f *Fake) Snapshot(_ context.Context, _ wire.SnapshotRequest) (*wire.SnapshotAnswer, error) {
	if err := f.fail("Snapshot"); err != nil {
		return nil, err
	}
	return &wire.SnapshotAnswer{Cursor: f.cursor(len(f.Entries)), Entries: append([]wire.Entry(nil), f.Entries...)}, nil
}

// Raw is always not_found: the Fake holds no transcript bytes.
func (f *Fake) Raw(context.Context, wire.RawRequest) (*wire.RawAnswer, error) {
	if err := f.fail("Raw"); err != nil {
		return nil, err
	}
	return nil, apiErr(wire.ErrNotFound, "no such record")
}

func (f *Fake) cursor(n int) string { return fmt.Sprintf("%d:%d", f.HelloAnswer.Epoch, n) }

func (f *Fake) position(after string) (int, error) {
	if after == "" {
		return 0, nil
	}
	ep, seq, ok := strings.Cut(after, ":")
	e, err1 := strconv.Atoi(ep)
	n, err2 := strconv.Atoi(seq)
	if !ok || err1 != nil || err2 != nil || e != f.HelloAnswer.Epoch || n < 0 || n > len(f.Entries) {
		return 0, client.NewError(wire.ErrCursorExpired, "cursor "+after+" is not in this log", f.cursor(len(f.Entries)))
	}
	return n, nil
}

// FeedPage serves the log after the cursor (never waits).
func (f *Fake) FeedPage(_ context.Context, _ *wire.Filter, after string, _ []string, limit, _ int) (*wire.FeedAnswer, error) {
	if err := f.fail("FeedPage"); err != nil {
		return nil, err
	}
	n, err := f.position(after)
	if err != nil {
		return nil, err
	}
	rest := f.Entries[n:]
	ans := &wire.FeedAnswer{Entries: []wire.Entry{}}
	if limit > 0 && len(rest) > limit {
		rest, ans.More = rest[:limit], true
	}
	for i, en := range rest {
		en.Seq = f.cursor(n + i + 1)
		ans.Entries = append(ans.Entries, en)
	}
	ans.NextCursor = f.cursor(n + len(rest))
	ans.TimedOut = len(rest) == 0
	return ans, nil
}

// Feed returns an iterator over the log.
func (f *Fake) Feed(_ context.Context, filter *wire.Filter, after string, opts ...client.FeedOptions) *client.FeedIter {
	return client.NewFeedIter(f, nil, filter, after, opts...)
}

// Conversation returns the seeded Conversation or not_found.
func (f *Fake) Conversation(_ context.Context, id string) (*wire.Conversation, error) {
	if err := f.fail("Conversation"); err != nil {
		return nil, err
	}
	for _, c := range f.Conversations {
		if c.SessionID == id {
			return &c, nil
		}
	}
	return nil, apiErr(wire.ErrNotFound, "no conversation "+id)
}

// ConversationList returns every seeded Conversation (filters are not applied).
func (f *Fake) ConversationList(_ context.Context, _ wire.ConversationFilter) (*wire.ConversationList, error) {
	if err := f.fail("ConversationList"); err != nil {
		return nil, err
	}
	return &wire.ConversationList{Conversations: append([]wire.Conversation{}, f.Conversations...)}, nil
}

// Process returns the seeded Process (the newest with that pid for an empty
// procStart) or not_found.
func (f *Fake) Process(_ context.Context, pid int64, procStart string) (*wire.Process, error) {
	if err := f.fail("Process"); err != nil {
		return nil, err
	}
	for _, p := range f.Processes {
		if p.Pid == pid && (procStart == "" || p.ProcStart == procStart) {
			return &p, nil
		}
	}
	return nil, apiErr(wire.ErrNotFound, fmt.Sprintf("no process %d", pid))
}

// ProcessList returns every seeded Process (filters are not applied).
func (f *Fake) ProcessList(_ context.Context, _ wire.ProcessFilter) (*wire.ProcessList, error) {
	if err := f.fail("ProcessList"); err != nil {
		return nil, err
	}
	return &wire.ProcessList{Processes: append([]wire.Process{}, f.Processes...)}, nil
}

type fakePoster struct{ f *Fake }

func (p fakePoster) Post(ctx context.Context, op string, _, out any) error {
	if op != "conversation.list" {
		return apiErr(wire.ErrNotFound, "no operation "+op)
	}
	a, err := p.f.ConversationList(ctx, wire.ConversationFilter{})
	if err != nil {
		return err
	}
	*out.(*wire.ConversationList) = *a
	return nil
}

// Ended labels the seeded Conversations by their evidence, as the client does.
func (f *Fake) Ended(ctx context.Context, q wire.EndQuery) (*wire.EndedResult, error) {
	if err := f.fail("Ended"); err != nil {
		return nil, err
	}
	res, err := wire.QueryEnded(ctx, fakePoster{f}, q)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Crashed returns the Conversations labeled crashed.
func (f *Fake) Crashed(ctx context.Context, since time.Time) ([]wire.Ended, error) {
	res, err := f.Ended(ctx, wire.EndQuery{Label: wire.LabelCrashed, Since: since})
	if err != nil {
		return nil, err
	}
	return res.Rows, nil
}

// SessionsFacts returns Facts restricted to the requested ids.
func (f *Fake) SessionsFacts(_ context.Context, ids, _ []string) (*wire.SessionsFactsAnswer, error) {
	if err := f.fail("SessionsFacts"); err != nil {
		return nil, err
	}
	out := &wire.SessionsFactsAnswer{Sessions: map[string]wire.SessionFacts{}, Unavailable: f.Facts.Unavailable}
	for _, id := range ids {
		if v, ok := f.Facts.Sessions[id]; ok {
			out.Sessions[id] = v
		}
	}
	return out, nil
}
