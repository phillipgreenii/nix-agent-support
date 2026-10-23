package engine

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// The invented registration every test of this package runs on: one entity kind,
// a primary group and three others, and change kinds no real connector uses.
const (
	kindWidget = "widget"

	groupAlpha = "alpha"
	groupBeta  = "beta" // stamped: a fetch counts only for the stamps of the pending row
	groupGamma = "gamma"

	changeRetitled = "retitled"
	changeToggled  = "toggled"
	changeTweaked  = "tweaked"
	changeGone     = "gone"
	changeJoined   = "joined"
	changeLeft     = "left"
	changeAdded    = "added"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

const (
	idOne  = "w-1"
	idTwo  = "w-2"
	refOne = "Example-Org/Thing#1"
)

func testSpec() registry.Spec {
	return registry.Spec{
		EntityKind:   kindWidget,
		PrimaryGroup: store.SummaryGroupName,
		Groups:       []string{store.SummaryGroupName, groupAlpha, groupBeta, groupGamma},
		Changes:      []string{changeToggled, changeRetitled, changeTweaked, changeGone, changeJoined, changeLeft, changeAdded},
		Roles:        registry.Roles{Added: changeAdded, Removed: changeGone, EnteredQuery: changeJoined, LeftQuery: changeLeft},
		Views:        []registry.View{{Name: kindWidget}},
		FoldRef:      foldCase,
		Emission:     registry.EmitAll,
		Delivery:     registry.DeliverByMembership,
		Membership:   registry.MembershipPolicy{StoreRows: true},
	}
}

// foldCase compares the part before the number sign case-insensitively.
func foldCase(ref string) string {
	for i := len(ref) - 1; i >= 0; i-- {
		if ref[i] == '#' {
			return lower(ref[:i]) + ref[i:]
		}
	}
	return lower(ref)
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// widget is the stored primary content of the invented entity.
type widget struct {
	Ref     string `json:"ref"`
	Stamp   string `json:"stamp"`
	Stamp2  string `json:"stamp2"`
	Title   string `json:"title"`
	Open    bool   `json:"open"`
	Count   int    `json:"count"`
	Updated string `json:"updated"`
	Gone    bool   `json:"gone"`
}

func sample() widget {
	return widget{Ref: refOne, Stamp: "s1", Stamp2: "t1", Title: "A title", Open: true, Updated: "2026-01-02T03:00:00Z"}
}

func widgetJSON(t testing.TB, w widget) []byte {
	t.Helper()
	b, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fake is the Classifier the tests install: a changed title is a retitled kind,
// a flipped open flag a toggled kind, a new stamp stales alpha and beta and a new
// count stales gamma.
type fake struct{}

func (fake) Describe(content []byte) (Primary, error) {
	var w widget
	if err := json.Unmarshal(content, &w); err != nil {
		return Primary{}, err
	}
	at, _ := time.Parse(time.RFC3339, w.Updated)
	return Primary{Ref: w.Ref, Stamp: w.Stamp, Stamp2: w.Stamp2, OriginAt: at, Gone: w.Gone}, nil
}

func jsonOf(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func (fake) Primary(prev, next []byte, _ int) (Result, []string, error) {
	var p, n widget
	if err := json.Unmarshal(prev, &p); err != nil {
		return Result{}, nil, err
	}
	if err := json.Unmarshal(next, &n); err != nil {
		return Result{}, nil, err
	}
	var res Result
	if p.Title != n.Title {
		res.Kinds = append(res.Kinds, changeRetitled)
		res.Fields = append(res.Fields, Field{Name: "title", Before: jsonOf(p.Title), After: jsonOf(n.Title)})
	}
	if p.Open != n.Open {
		res.Kinds = append(res.Kinds, changeToggled)
		res.Fields = append(res.Fields, Field{Name: "open", Before: jsonOf(p.Open), After: jsonOf(n.Open)})
	}
	var queue []string
	if p.Stamp != n.Stamp {
		queue = append(queue, groupAlpha, groupBeta)
	}
	if p.Count != n.Count {
		queue = append(queue, groupGamma)
	}
	return Result{Kinds: OrderKinds(testSpec().Changes, res.Kinds), Fields: Normalize(res.Fields)}, queue, nil
}

func (fake) Group(name string, prev, next []byte, _ int) (Result, error) {
	if string(prev) == string(next) {
		return Result{}, nil
	}
	return Result{Kinds: []string{changeTweaked}, Fields: []Field{{Name: name, Before: jsonOf(string(prev)), After: jsonOf(string(next))}}}, nil
}

func (fake) Counts(a Awaiting, fetched store.GroupRow, pending store.PendingChange, started time.Time) bool {
	if a.Group != fetched.Group || started.Before(a.Since) {
		return false
	}
	if a.Group != groupBeta {
		return true
	}
	fa, fb := fetched.Stamps()
	pa, pb := pending.Stamps()
	return fa == pa && fb == pb
}

type detectedCall struct {
	Kind, Origin string
	Delay        time.Duration
}

// recorder is the Observer the tests install.
type recorder struct {
	mu       sync.Mutex
	detected []detectedCall
	settled  []string
}

func (o *recorder) Detected(kind, origin string, delay time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.detected = append(o.detected, detectedCall{kind, origin, delay})
}

func (o *recorder) Settled(result string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.settled = append(o.settled, result)
}

// rig is a real store, the manual clock and an engine over the invented registration.
type rig struct {
	t       *testing.T
	st      *store.Store
	clk     *governor.ManualClock
	e       *Engine
	obs     *recorder
	purpose governor.Purpose
}

func newEngine(t testing.TB, clk governor.Clock) *Engine {
	t.Helper()
	e, err := New(Config{Spec: testSpec(), Classifier: fake{}, Clock: clk, SettleTimeout: 30 * time.Second, BodyCap: 64})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := governor.NewManualClock(t0)
	e := newEngine(t, clk)
	obs := &recorder{}
	e.SetObserver(obs)
	return &rig{t: t, st: st, clk: clk, e: e, obs: obs, purpose: governor.PurposeRefresh}
}

func (r *rig) inTx(fn func(tx *store.Tx) error) error {
	r.t.Helper()
	return r.st.InTx(context.Background(), fn)
}

func (r *rig) must(err error) {
	r.t.Helper()
	if err != nil {
		r.t.Fatal(err)
	}
}

// seed stores the entity (version 5) and its primary content.
func (r *rig) seed(id string, w widget) {
	r.t.Helper()
	r.must(r.inTx(func(tx *store.Tx) error {
		ctx := context.Background()
		if err := tx.PutEntity(ctx, store.EntityRow{Kind: kindWidget, ID: id, Version: 5}); err != nil {
			return err
		}
		row := store.GroupRow{
			Kind: kindWidget, ID: id, Group: store.SummaryGroupName, Content: widgetJSON(r.t, w), Generation: 1,
			FetchedAt: t0, FreshUntil: t0.Add(time.Hour),
		}
		row.SetStamps(w.Stamp, w.Stamp2)
		return tx.PutGroup(ctx, row)
	}))
}

// seedGroup stores a non-primary group as a previous fetch left it.
func (r *rig) seedGroup(id, group, content, stamp, stamp2 string) {
	r.t.Helper()
	r.must(r.inTx(func(tx *store.Tx) error {
		row := store.GroupRow{
			Kind: kindWidget, ID: id, Group: group, Content: []byte(content), Fingerprint: "seed", Generation: 1,
			FetchedAt: t0, FreshUntil: t0.Add(time.Hour),
		}
		row.SetStamps(stamp, stamp2)
		return tx.PutGroup(context.Background(), row)
	}))
}

// changePrimary stores a changed primary content and runs DetectPrimary in the
// same transaction, the way the persister's seam does.
func (r *rig) changePrimary(id string, mutate func(*widget)) error {
	r.t.Helper()
	return r.inTx(func(tx *store.Tx) error {
		ctx := context.Background()
		row, ok, err := tx.GetGroup(ctx, kindWidget, id, store.SummaryGroupName)
		if err != nil || !ok {
			r.t.Fatalf("stored primary: ok=%v err=%v", ok, err)
		}
		var w widget
		r.must(json.Unmarshal(row.Content, &w))
		prev := row.Content
		mutate(&w)
		row.Content, row.Generation = widgetJSON(r.t, w), row.Generation+1
		row.SetStamps(w.Stamp, w.Stamp2)
		if err := tx.PutGroup(ctx, row); err != nil {
			return err
		}
		return r.e.DetectPrimary(ctx, tx, PrimaryChange{ID: id, Prev: prev, Next: row.Content, Info: RefreshInfo{Purpose: r.purpose}})
	})
}

// refresh stores a refreshed group and runs DetectGroup in the same transaction.
func (r *rig) refresh(id, group, content, stamp, stamp2 string, started time.Time) error {
	r.t.Helper()
	return r.inTx(func(tx *store.Tx) error {
		ctx := context.Background()
		old, had, err := tx.GetGroup(ctx, kindWidget, id, group)
		if err != nil {
			return err
		}
		var prev *store.GroupRow
		if had {
			prev = &old
		}
		now := r.clk.Now()
		next := store.GroupRow{
			Kind: kindWidget, ID: id, Group: group, Content: []byte(content), Fingerprint: "fp",
			FetchedAt: now, FreshUntil: now.Add(time.Hour), Generation: old.Generation + 1,
		}
		next.SetStamps(stamp, stamp2)
		if err := tx.PutGroup(ctx, next); err != nil {
			return err
		}
		return r.e.DetectGroup(ctx, tx, prev, next, RefreshInfo{Started: started, Purpose: r.purpose})
	})
}

func (r *rig) changes() []store.ChangeRow {
	r.t.Helper()
	rows, err := r.st.ChangesAfter(context.Background(), 0, 0)
	r.must(err)
	return rows
}

func (r *rig) pending() (store.PendingChange, bool) {
	r.t.Helper()
	ps, err := r.st.ListPending(context.Background())
	r.must(err)
	if len(ps) == 0 {
		return store.PendingChange{}, false
	}
	if len(ps) > 1 {
		r.t.Fatalf("%d pending rows, want at most 1", len(ps))
	}
	return ps[0], true
}

func (r *rig) awaitedGroups() []string {
	r.t.Helper()
	p, ok := r.pending()
	if !ok {
		return nil
	}
	aw, err := DecodeAwaited(p.Awaited)
	r.must(err)
	var out []string
	for _, a := range aw {
		out = append(out, a.Group)
	}
	return out
}

func (r *rig) version(id string) int64 {
	r.t.Helper()
	var v int64
	r.must(r.inTx(func(tx *store.Tx) error {
		e, ok, err := tx.GetEntity(context.Background(), kindWidget, id)
		if err != nil || !ok {
			r.t.Fatalf("entity: ok=%v err=%v", ok, err)
		}
		v = e.Version
		return nil
	}))
	return v
}

func rowFields(row store.ChangeRow) []string {
	fs, err := DecodeFields(row.Fields)
	if err != nil {
		panic(err)
	}
	var out []string
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return out
}

func wantKinds(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("kinds = %v, want %v", got, want)
	}
}

func wantNoChange(t *testing.T, r *rig) {
	t.Helper()
	if rows := r.changes(); len(rows) != 0 {
		t.Fatalf("appended %d rows, want none: %+v", len(rows), rows)
	}
	if _, ok := r.pending(); ok {
		t.Fatal("a pending row exists, want none")
	}
}
