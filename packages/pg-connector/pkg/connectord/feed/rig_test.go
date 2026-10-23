package feed

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/enginetest/vocab"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

const (
	day      = 24 * time.Hour
	refA     = "example-org/widget#1"
	refB     = "example-org/widget#2"
	refOther = "example-org/gadget#9"
)

// The invented vocabulary of the test registration: one entity kind, two views
// (all rows, and only the rows that carry the flag kind) and the four roles.
// Each name goes through vocab.W, so the whole test set also runs over a second
// registration (see second_registration_test.go).
var (
	entityKind   = vocab.W("example_entity")
	viewAll      = vocab.W("all")
	viewFlagged  = vocab.W("flagged")
	kindAdded    = vocab.W("added")
	kindRemoved  = vocab.W("removed")
	kindEntered  = vocab.W("entered_scope")
	kindLeft     = vocab.W("left_scope")
	kindContent  = vocab.W("content_changed")
	kindFlag     = vocab.W("flag_changed")
	kindOther    = vocab.W("other_changed")
	groupPrimary = vocab.W("main")
)

// foldRef folds the part of a reference before the number sign to lower case and
// keeps the number exact.
func foldRef(ref string) string {
	if i := strings.LastIndex(ref, "#"); i >= 0 {
		return strings.ToLower(ref[:i]) + ref[i:]
	}
	return strings.ToLower(ref)
}

func testSpec() registry.Spec {
	return registry.Spec{
		EntityKind:   entityKind,
		PrimaryGroup: groupPrimary,
		Groups:       []string{groupPrimary},
		Changes:      []string{kindAdded, kindRemoved, kindEntered, kindLeft, kindContent, kindFlag, kindOther},
		Roles:        registry.Roles{Added: kindAdded, Removed: kindRemoved, EnteredQuery: kindEntered, LeftQuery: kindLeft},
		Views: []registry.View{
			{Name: viewAll},
			{Name: viewFlagged, AnyOf: []string{kindFlag}},
		},
		FoldRef:    foldRef,
		Emission:   registry.EmitAll,
		Delivery:   registry.DeliverByMembership,
		Membership: registry.MembershipPolicy{StoreRows: true},
	}
}

// ackErr is Ack reduced to its error, for the tests that only look at it.
func ackErr(f *Feed, ctx context.Context, view, consumer, scope string, seq int64) error {
	_, err := f.Ack(ctx, AckRequest{View: view, Consumer: consumer, Scope: scope, NextSeq: seq})
	return err
}

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

func (q *fakeQueries) Names() []string { return nil }

func (q *fakeQueries) Watched(string) bool { return false }

func (q *fakeQueries) WatchedRefs(context.Context) ([]string, error) { return nil, nil }

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

func (r *recorder) Redelivered(consumer, scope string, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, consumer+"/"+scope)
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
	cfg := Config{Spec: testSpec(), Store: st, Queries: q, Clock: clk, Retention: 14 * day, ConsumerStaleAfter: 7 * day, Observer: obs}
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
func (r *rig) add(ref string, kinds []string, fields ...engine.Field) int64 {
	r.t.Helper()
	raw, err := engine.EncodeFields(fields)
	if err != nil {
		r.t.Fatal(err)
	}
	var seq int64
	err = r.st.InTx(context.Background(), func(tx *store.Tx) error {
		var aerr error
		seq, aerr = tx.AppendChange(context.Background(), store.ChangeRow{
			Kind: entityKind, ID: ref, Version: 1, Kinds: kinds, Fields: raw, Origin: "refresh", At: r.clk.Now(),
		})
		return aerr
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return seq
}

// plain is a row of changed content.
func (r *rig) plain(ref string) int64 { return r.add(ref, []string{kindContent}) }

func (r *rig) head() int64 {
	r.t.Helper()
	h, err := r.st.ChangeHead(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	return h
}

func (r *rig) poll(view, consumer, scope string) PollResult {
	r.t.Helper()
	res, err := r.f.Poll(context.Background(), PollRequest{View: view, Consumer: consumer, Scope: scope})
	if err != nil {
		r.t.Fatalf("poll %s/%s/%s: %v", consumer, view, scope, err)
	}
	return res
}

func (r *rig) ack(view, consumer, scope string, seq int64) {
	r.t.Helper()
	if err := ackErr(r.f, context.Background(), view, consumer, scope, seq); err != nil {
		r.t.Fatalf("ack: %v", err)
	}
}

// pos returns the stored position of a cursor; ok is false when there is none.
func (r *rig) pos(view, consumer, scope string) (int64, bool) {
	r.t.Helper()
	c, ok, err := r.st.GetConsumer(context.Background(), consumer, view, scope)
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

func enteredField(scope string) engine.Field {
	return engine.Field{Name: "query:" + scope, After: mustJSON(scope)}
}

func leftField(scope string) engine.Field {
	return engine.Field{Name: "query:" + scope, Before: mustJSON(scope)}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
