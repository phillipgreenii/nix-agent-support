package changesop

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

const (
	day   = 24 * time.Hour
	refA  = "acme-org/widget#1"
	refB  = "acme-org/widget#2"
	other = "acme-org/gadget#9"
)

// fakeQueries is a feed.Queries whose membership the test sets; the stored
// configured query rows (which the handlers read) are written by setQuery.
type fakeQueries struct {
	mu      sync.Mutex
	members map[string]feed.Members
	runs    map[string]int
	runErr  error
}

func (q *fakeQueries) Known(name string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.members[name]
	return ok
}

func (q *fakeQueries) Members(_ context.Context, name string) (feed.Members, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.members[name], nil
}

func (q *fakeQueries) Run(_ context.Context, name string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.runs[name]++
	return q.runErr
}

func (q *fakeQueries) runCount(name string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.runs[name]
}

type rig struct {
	t   *testing.T
	st  *store.Store
	clk *governor.ManualClock
	q   *fakeQueries
	f   *feed.Feed
	rt  *router.Router
}

func newRig(t *testing.T, opts ...func(*feed.Config)) *rig {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := governor.NewManualClock(t0)
	q := &fakeQueries{members: map[string]feed.Members{}, runs: map[string]int{}}
	cfg := feed.Config{Store: st, Queries: q, Clock: clk, Retention: 14 * day, ConsumerStaleAfter: 7 * day}
	for _, o := range opts {
		o(&cfg)
	}
	f, err := feed.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rt := router.New(router.Options{Instances: []string{"pr", "ci"}})
	for instance, ops := range Handlers(Deps{Feed: f, Store: st, Aliases: entity.NewIdentity(st, clk), Clock: clk}) {
		for op, h := range ops {
			rt.Register(instance, op, h)
		}
	}
	r := &rig{t: t, st: st, clk: clk, q: q, f: f, rt: rt}
	r.setQuery("mine", true, refA)
	r.setQuery("team", true, refA, refB)
	return r
}

// setQuery makes the query known to the feed with the given members and writes
// its stored configured row.
func (r *rig) setQuery(name string, baseline bool, refs ...string) {
	r.t.Helper()
	r.q.mu.Lock()
	r.q.members[name] = feed.Members{Refs: refs, Baseline: baseline}
	r.q.mu.Unlock()
	row := store.QueryRow{Key: name, Configured: true, IDs: refs, Complete: true}
	if baseline {
		row.BaselineAt = t0
	}
	if err := r.st.PutQuery(context.Background(), row); err != nil {
		r.t.Fatal(err)
	}
}

// putQueryRow overwrites the stored row of a configured query (for freshness).
func (r *rig) putQueryRow(row store.QueryRow) {
	r.t.Helper()
	if err := r.st.PutQuery(context.Background(), row); err != nil {
		r.t.Fatal(err)
	}
}

// putPR stores the alias, the entity row and the summary of a pull request.
func (r *rig) putPR(ref, node, title, head string, version int64) {
	r.t.Helper()
	ctx := context.Background()
	parsed, err := entity.ParseRef(ref)
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := entity.NewIdentity(r.st, r.clk).Put(ctx, node, parsed); err != nil {
		r.t.Fatal(err)
	}
	if err := r.st.PutEntity(ctx, store.EntityRow{Kind: "pr", ID: node, Version: version}); err != nil {
		r.t.Fatal(err)
	}
	raw, err := json.Marshal(summary.Summary{
		NodeID: node, Repo: parsed.NameWithOwner(), Number: parsed.Number, Title: title, State: "OPEN",
		Author: "phillipgreenii", URL: "https://example.invalid/" + ref, HeadRefOid: head,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.st.PutGroup(ctx, store.GroupRow{Kind: "pr", ID: node, Group: summary.GroupName, Content: raw, FetchedAt: t0}); err != nil {
		r.t.Fatal(err)
	}
}

// add appends one log row for ref and returns its Seq.
func (r *rig) add(ref string, version int64, head string, kinds []string, fields ...classify.Field) int64 {
	r.t.Helper()
	raw, err := classify.EncodeFields(fields)
	if err != nil {
		r.t.Fatal(err)
	}
	var seq int64
	err = r.st.InTx(context.Background(), func(tx *store.Tx) error {
		var aerr error
		seq, aerr = tx.AppendChange(context.Background(), store.ChangeRow{
			Kind: "pr", ID: ref, Version: version, HeadSHA: head, Kinds: kinds, Fields: raw, Origin: "refresh", At: r.clk.Now(),
		})
		return aerr
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return seq
}

func (r *rig) head() int64 {
	r.t.Helper()
	h, err := r.st.ChangeHead(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	return h
}

func (r *rig) pos(consumer, query string) (int64, bool) {
	r.t.Helper()
	c, ok, err := r.st.GetConsumer(context.Background(), consumer, "pr", query)
	if err != nil {
		r.t.Fatal(err)
	}
	return c.AckedSeq, ok
}

// call dispatches one op of the pr instance through the router.
func (r *rig) call(op, args string) scriptout.Response {
	r.t.Helper()
	return r.rt.Dispatch(context.Background(), router.Call{Instance: "pr", Request: scriptout.Request{Op: op, Args: json.RawMessage(args)}})
}

// changes dispatches a changes op and decodes its result.
func (r *rig) changes(args string) (schema.PRChangesResult, scriptout.Response) {
	r.t.Helper()
	resp := r.call(OpChanges, args)
	if resp.Error != nil {
		r.t.Fatalf("changes %s: %s: %s", args, resp.Error.Code, resp.Error.Message)
	}
	var res schema.PRChangesResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		r.t.Fatalf("decode %s: %v", resp.Result, err)
	}
	return res, resp
}

func (r *rig) ack(consumer, query string, seq int64) schema.ChangesAckResult {
	r.t.Helper()
	resp := r.call(OpChangesAck, ackArgs(consumer, query, seq))
	if resp.Error != nil {
		r.t.Fatalf("changes_ack: %s: %s", resp.Error.Code, resp.Error.Message)
	}
	var res schema.ChangesAckResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		r.t.Fatal(err)
	}
	return res
}

func ackArgs(consumer, query string, seq int64) string {
	b, _ := json.Marshal(map[string]any{"consumer": consumer, "query": query, "next_seq": seq})
	return string(b)
}

func pollArgs(consumer, query string) string {
	b, _ := json.Marshal(map[string]any{"consumer": consumer, "query": query})
	return string(b)
}

func jsonOf(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func enteredField(query string) classify.Field {
	return classify.Field{Name: "query:" + query, After: jsonOf(query)}
}

func leftField(query string) classify.Field {
	return classify.Field{Name: "query:" + query, Before: jsonOf(query)}
}

// wantError checks the wire code and that the message contains want.
func wantError(t *testing.T, resp scriptout.Response, code, want string) {
	t.Helper()
	if resp.Error == nil {
		t.Fatalf("answer = %s, want error %s", resp.Result, code)
	}
	if resp.Error.Code != code {
		t.Errorf("code = %s (%s), want %s", resp.Error.Code, resp.Error.Message, code)
	}
	if !strings.Contains(resp.Error.Message, want) {
		t.Errorf("message = %q, want it to contain %q", resp.Error.Message, want)
	}
}

func feedPoll(kind, consumer, query string) feed.PollRequest {
	return feed.PollRequest{Kind: kind, Consumer: consumer, Query: query}
}
