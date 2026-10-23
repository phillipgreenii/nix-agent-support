package feedop

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/enginetest/vocab"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

// The invented vocabulary of the test registration. Each name goes through
// vocab.W, so the whole test set also runs over a second registration (see
// second_registration_test.go).
var (
	entityKind  = vocab.W("example_entity")
	viewAll     = vocab.W("all")
	viewFlagged = vocab.W("flagged")
	kindAdded   = vocab.W("added")
	kindRemoved = vocab.W("removed")
	kindEntered = vocab.W("entered_scope")
	kindLeft    = vocab.W("left_scope")
	kindContent = vocab.W("content_changed")
	kindFlag    = vocab.W("flag_changed")
	groupMain   = vocab.W("main")
)

const (
	day        = 24 * time.Hour
	refA       = "example-org/widget#1"
	refB       = "example-org/widget#2"
	sourceName = "example-source"
)

func foldRef(ref string) string {
	if i := strings.LastIndex(ref, "#"); i >= 0 {
		return strings.ToLower(ref[:i]) + ref[i:]
	}
	return strings.ToLower(ref)
}

func testSpec() registry.Spec {
	return registry.Spec{
		EntityKind:   entityKind,
		PrimaryGroup: groupMain,
		Groups:       []string{groupMain},
		Changes:      []string{kindAdded, kindRemoved, kindEntered, kindLeft, kindContent, kindFlag},
		Roles:        registry.Roles{Added: kindAdded, Removed: kindRemoved, EnteredQuery: kindEntered, LeftQuery: kindLeft},
		Views:        []registry.View{{Name: viewAll}, {Name: viewFlagged, AnyOf: []string{kindFlag}}},
		FoldRef:      foldRef,
		Emission:     registry.EmitAll,
		Delivery:     registry.DeliverByMembership,
		Membership:   registry.MembershipPolicy{StoreRows: true},
	}
}

// fakeQueries is a feed.Queries whose membership the test sets.
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
func (q *fakeQueries) Names() []string                               { return nil }
func (q *fakeQueries) Watched(string) bool                           { return false }
func (q *fakeQueries) WatchedRefs(context.Context) ([]string, error) { return nil, nil }
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

// entity is the JSON the test renderer produces.
type entity struct {
	Ref     string `json:"ref"`
	Version int64  `json:"version"`
	Stamp   string `json:"stamp"`
	Replay  bool   `json:"replay"`
}

// fakeRenderer renders an entity that shows what it was given.
type fakeRenderer struct{ err error }

func (r fakeRenderer) Row(_ context.Context, row store.ChangeRow) (json.RawMessage, error) {
	if r.err != nil {
		return nil, r.err
	}
	return json.Marshal(entity{Ref: row.ID, Version: row.Version, Stamp: row.Stamp()})
}

func (r fakeRenderer) Replay(_ context.Context, ref string) (json.RawMessage, error) {
	if r.err != nil {
		return nil, r.err
	}
	return json.Marshal(entity{Ref: ref, Replay: true})
}

type rig struct {
	t   *testing.T
	st  *store.Store
	clk *governor.ManualClock
	q   *fakeQueries
	f   *feed.Feed
	ops map[string]func(context.Context, json.RawMessage) (any, error)
}

var errAuth = fmt.Errorf("example credentials refused")

func newRig(t *testing.T, view string, renderer Renderer) *rig {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := governor.NewManualClock(t0)
	q := &fakeQueries{members: map[string]feed.Members{}, runs: map[string]int{}}
	f, err := feed.New(feed.Config{Spec: testSpec(), Store: st, Queries: q, Clock: clk, Retention: 14 * day, ConsumerStaleAfter: 7 * day})
	if err != nil {
		t.Fatal(err)
	}
	if renderer == nil {
		renderer = fakeRenderer{}
	}
	h := Handlers(Deps{Feed: f, Store: st, View: view, Source: sourceName, Renderer: renderer, AuthErrors: []error{errAuth}, SchemaVersion: 7, Clock: clk})
	r := &rig{t: t, st: st, clk: clk, q: q, f: f, ops: map[string]func(context.Context, json.RawMessage) (any, error){}}
	for name, op := range h {
		if op.SchemaVersion != 7 {
			t.Fatalf("op %s schema version = %d", name, op.SchemaVersion)
		}
		r.ops[name] = op.Handle
	}
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

// add appends one log row for ref and returns its Seq.
func (r *rig) add(ref string, version int64, stamp string, kinds []string, fields ...engine.Field) int64 {
	r.t.Helper()
	raw, err := engine.EncodeFields(fields)
	if err != nil {
		r.t.Fatal(err)
	}
	var seq int64
	err = r.st.InTx(context.Background(), func(tx *store.Tx) error {
		row := store.ChangeRow{Kind: entityKind, ID: ref, Version: version, Kinds: kinds, Fields: raw, Origin: "refresh", At: r.clk.Now()}
		row.SetStamp(stamp)
		var aerr error
		seq, aerr = tx.AppendChange(context.Background(), row)
		return aerr
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return seq
}

// call runs one op with the argument text and returns the marshalled result or
// the error.
func (r *rig) call(op, args string) (json.RawMessage, error) {
	r.t.Helper()
	h, ok := r.ops[op]
	if !ok {
		r.t.Fatalf("no op %q", op)
	}
	res, err := h(context.Background(), json.RawMessage(args))
	if err != nil {
		return nil, err
	}
	raw, merr := json.Marshal(res)
	if merr != nil {
		r.t.Fatal(merr)
	}
	return raw, nil
}

func (r *rig) changes(args string) schema.ChangesResult {
	r.t.Helper()
	raw, err := r.call("changes", args)
	if err != nil {
		r.t.Fatalf("changes %s: %v", args, err)
	}
	var out schema.ChangesResult
	if err := json.Unmarshal(raw, &out); err != nil {
		r.t.Fatal(err)
	}
	return out
}

func (r *rig) ack(args string) schema.ChangesAckResult {
	r.t.Helper()
	raw, err := r.call("changes_ack", args)
	if err != nil {
		r.t.Fatalf("changes_ack %s: %v", args, err)
	}
	var out schema.ChangesAckResult
	if err := json.Unmarshal(raw, &out); err != nil {
		r.t.Fatal(err)
	}
	return out
}

func decodeEntity(t *testing.T, raw json.RawMessage) entity {
	t.Helper()
	var e entity
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	return e
}

func enteredField(scope string) engine.Field {
	return engine.Field{Name: "query:" + scope, After: json.RawMessage(`"` + scope + `"`)}
}

func leftField(scope string) engine.Field {
	return engine.Field{Name: "query:" + scope, Before: json.RawMessage(`"` + scope + `"`)}
}

// forView returns a rig over the same store and feed whose handlers serve view.
func (r *rig) forView(view string) *rig {
	h := Handlers(Deps{Feed: r.f, Store: r.st, View: view, Source: sourceName, Renderer: fakeRenderer{}, AuthErrors: []error{errAuth}, SchemaVersion: 7, Clock: r.clk})
	o := &rig{t: r.t, st: r.st, clk: r.clk, q: r.q, f: r.f, ops: map[string]func(context.Context, json.RawMessage) (any, error){}}
	for name, op := range h {
		o.ops[name] = op.Handle
	}
	return o
}
