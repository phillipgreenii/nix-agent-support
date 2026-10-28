package feedop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// HT-25: the wire of the two ops. A registration answers changes and changes_ack
// with no query, a given query binds the scope, and every answer carries the
// store identity.

var storeIdentityShape = regexp.MustCompile(`^[0-9a-f]{32}$`)

// newWatchedRig is newRig over a registration spec set up in the test itself.
func newWatchedRig(t *testing.T) *rig {
	t.Helper()
	return newRigOverSpec(t, filepath.Join(t.TempDir(), "store.db"), testSpec())
}

// newRigOverSpec opens the store at path and serves view viewAll of spec.
func newRigOverSpec(t *testing.T, path string, spec registry.Spec) *rig {
	t.Helper()
	st, _, err := store.Open(path, store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return newRigOverStore(t, st, spec)
}

func newRigOverStore(t *testing.T, st *store.Store, spec registry.Spec) *rig {
	t.Helper()
	clk := governor.NewManualClock(t0)
	q := &fakeQueries{members: map[string]feed.Members{}, runs: map[string]int{}}
	f, err := feed.New(feed.Config{Spec: spec, Store: st, Queries: q, Clock: clk, Retention: 14 * day, ConsumerStaleAfter: 7 * day})
	if err != nil {
		t.Fatal(err)
	}
	h := Handlers(Deps{Feed: f, Store: st, View: viewAll, Source: sourceName, Renderer: fakeRenderer{}, AuthErrors: []error{errAuth}, SchemaVersion: 7, Clock: clk})
	r := &rig{t: t, st: st, clk: clk, q: q, f: f, ops: map[string]func(context.Context, json.RawMessage) (any, error){}}
	for name, op := range h {
		r.ops[name] = op.Handle
	}
	r.setQuery("mine", true, refA)
	r.setQuery("team", true, refA, refB)
	return r
}

func TestAWatchedRegistrationAnswersChangesAndAckWithNoQuery(t *testing.T) {
	r := newWatchedRig(t)
	res := r.changes(`{"consumer":"c"}`)
	if len(res.Changes) != 0 || len(res.Sources) != 1 {
		t.Fatalf("first poll = %+v", res)
	}
	next := res.Sources[0].NextSeq

	raw, err := r.call("changes_ack", `{"consumer":"c","next_seq":`+itoa(next)+`}`)
	if err != nil {
		t.Fatalf("changes_ack with no query: %v", err)
	}
	if strings.Contains(string(raw), `"query"`) {
		t.Errorf("the ack of a query-less call carries a query: %s", raw)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"consumer", "acked_seq", "store_identity"} {
		if _, ok := m[k]; !ok {
			t.Errorf("the ack answer lacks %q: %s", k, raw)
		}
	}
}

func TestAGivenQueryBindsTheScopeOfAWatchedRegistrationAndTheAckKeepsIt(t *testing.T) {
	r := newWatchedRig(t)
	r.changes(`{"consumer":"c","query":"mine"}`)
	if got := r.ack(`{"consumer":"c","query":"mine","next_seq":0}`); got.Query != "mine" {
		t.Fatalf("ack = %+v, want the query echoed", got)
	}
	// The cursor is bound to the scope it was created with.
	_, err := r.call("changes", `{"consumer":"c","query":"team"}`)
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("a call naming another scope = %v, want invalid_argument", err)
	}
	_, err = r.call("changes", `{"consumer":"c"}`)
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("a query-less call of a scoped cursor = %v, want invalid_argument", err)
	}
}

func TestEveryAnswerCarriesTheStoreIdentityAndItIsStableAcrossCalls(t *testing.T) {
	for name, r := range map[string]*rig{"default": newRig(t, viewAll, nil), "own store": newWatchedRig(t)} {
		want, err := r.st.Identity(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !storeIdentityShape.MatchString(want) {
			t.Fatalf("%s: store identity %q is not 32 lowercase hex characters", name, want)
		}
		first := r.changes(`{"consumer":"c","query":"mine"}`)
		r.add(refA, 2, "s2", []string{kindContent})
		second := r.changes(`{"consumer":"c","query":"mine"}`)
		reset := r.changes(`{"consumer":"c","query":"mine","reset":true}`)
		acked := r.ack(`{"consumer":"c","query":"mine","next_seq":1}`)
		for what, got := range map[string]string{
			"first poll": first.StoreIdentity, "second poll": second.StoreIdentity,
			"reset": reset.StoreIdentity, "ack": acked.StoreIdentity,
		} {
			if got != want {
				t.Errorf("%s: %s store_identity = %q, want %q", name, what, got, want)
			}
		}
	}
}

func TestTheStoreIdentityChangesAfterAMovedAsideOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	first := newRigOverSpec(t, path, testSpec())
	oldID := first.changes(`{"consumer":"c","query":"mine"}`).StoreIdentity
	if err := first.st.Close(); err != nil {
		t.Fatal(err)
	}
	// A file that is not a database is moved aside and replaced by a new store.
	garbage := bytes.Repeat([]byte("this is not a database\n"), 200)
	if err := os.WriteFile(path, garbage, 0o644); err != nil {
		t.Fatal(err)
	}
	st, res, err := store.Open(path, store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if res.Outcome != store.OutcomeMovedAside {
		t.Fatalf("open result = %+v, want moved aside", res)
	}
	second := newRigOverStore(t, st, testSpec())
	newID := second.changes(`{"consumer":"c","query":"mine"}`).StoreIdentity
	if newID == "" || newID == oldID {
		t.Fatalf("store_identity after a moved-aside open = %q, before = %q: want a new non-empty one", newID, oldID)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
