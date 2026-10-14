package feed

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

const (
	day      = 24 * time.Hour
	refA     = "acme-org/widget#1"
	refB     = "acme-org/widget#2"
	refOther = "acme-org/gadget#9"
)

// fakeQueries is a Queries whose membership the test sets.
type fakeQueries struct {
	mu      sync.Mutex
	members map[string]Members
	runs    map[string]int
	runErr  error
	onRun   func(name string)
}

func newFakeQueries() *fakeQueries {
	return &fakeQueries{members: map[string]Members{}, runs: map[string]int{}}
}

func (q *fakeQueries) set(name string, baseline bool, refs ...string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.members[name] = Members{Refs: refs, Baseline: baseline}
}

func (q *fakeQueries) Known(name string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.members[name]
	return ok
}

func (q *fakeQueries) Members(_ context.Context, name string) (Members, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.members[name], nil
}

func (q *fakeQueries) Run(_ context.Context, name string) error {
	q.mu.Lock()
	q.runs[name]++
	hook, err := q.onRun, q.runErr
	q.mu.Unlock()
	if hook != nil {
		hook(name)
	}
	return err
}

func (q *fakeQueries) runCount(name string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.runs[name]
}

// recorder is the Observer.
type recorder struct {
	mu    sync.Mutex
	calls []string
	rows  []int
}

func (r *recorder) Redelivered(consumer, query string, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, consumer+"/"+query)
	r.rows = append(r.rows, rows)
}

func (r *recorder) total() (calls, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range r.rows {
		rows += n
	}
	return len(r.calls), rows
}

type rig struct {
	t   *testing.T
	st  *store.Store
	clk *governor.ManualClock
	q   *fakeQueries
	obs *recorder
	f   *Feed
}

func newRig(t *testing.T, opts ...func(*Config)) *rig {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := governor.NewManualClock(t0)
	q := newFakeQueries()
	q.set("mine", true, refA)
	q.set("team", true, refA, refB)
	obs := &recorder{}
	cfg := Config{Store: st, Queries: q, Clock: clk, Retention: 14 * day, ConsumerStaleAfter: 7 * day, Observer: obs}
	for _, o := range opts {
		o(&cfg)
	}
	f, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, st: st, clk: clk, q: q, obs: obs, f: f}
}

// add appends one log row for ref at the clock's time and returns its Seq.
func (r *rig) add(ref string, kinds []string, fields ...classify.Field) int64 {
	r.t.Helper()
	raw, err := classify.EncodeFields(fields)
	if err != nil {
		r.t.Fatal(err)
	}
	var seq int64
	err = r.st.InTx(context.Background(), func(tx *store.Tx) error {
		var aerr error
		seq, aerr = tx.AppendChange(context.Background(), store.ChangeRow{
			Kind: "pr", ID: ref, Version: 1, Kinds: kinds, Fields: raw, Origin: "refresh", At: r.clk.Now(),
		})
		return aerr
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return seq
}

// plain is a row of a changed head commit.
func (r *rig) plain(ref string) int64 { return r.add(ref, []string{classify.KindHeadChanged}) }

func (r *rig) head() int64 {
	r.t.Helper()
	h, err := r.st.ChangeHead(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	return h
}

func (r *rig) poll(kind, consumer, query string) PollResult {
	r.t.Helper()
	res, err := r.f.Poll(context.Background(), PollRequest{Kind: kind, Consumer: consumer, Query: query})
	if err != nil {
		r.t.Fatalf("poll %s/%s/%s: %v", consumer, kind, query, err)
	}
	return res
}

func (r *rig) ack(kind, consumer, query string, seq int64) {
	r.t.Helper()
	if err := r.f.Ack(context.Background(), kind, consumer, query, seq); err != nil {
		r.t.Fatalf("ack: %v", err)
	}
}

// pos returns the stored position of a key; ok is false when there is none.
func (r *rig) pos(kind, consumer, query string) (int64, bool) {
	r.t.Helper()
	c, ok, err := r.st.GetConsumer(context.Background(), consumer, kind, query)
	if err != nil {
		r.t.Fatal(err)
	}
	return c.AckedSeq, ok
}

func seqs(rows []store.ChangeRow) []int64 {
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Seq)
	}
	return out
}

func eqSeqs(t *testing.T, got []store.ChangeRow, want ...int64) {
	t.Helper()
	g := seqs(got)
	if len(g) != len(want) {
		t.Fatalf("seqs = %v, want %v", g, want)
	}
	for i := range g {
		if g[i] != want[i] {
			t.Fatalf("seqs = %v, want %v", g, want)
		}
	}
}

func enteredField(query string) classify.Field {
	return classify.Field{Name: "query:" + query, After: mustJSON(query)}
}

func leftField(query string) classify.Field {
	return classify.Field{Name: "query:" + query, Before: mustJSON(query)}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
