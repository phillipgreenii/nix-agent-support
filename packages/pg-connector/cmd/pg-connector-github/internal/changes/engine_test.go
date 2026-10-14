package changes

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

const node = "PR_1"

// recorder is the Observer the tests install.
type recorder struct {
	mu       sync.Mutex
	detected []detectedCall
	settled  []string
}

type detectedCall struct {
	Kind, Origin string
	Delay        time.Duration
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

// rig is a real store, the manual clock and an engine.
type rig struct {
	t       *testing.T
	st      *store.Store
	clk     *governor.ManualClock
	e       *Engine
	obs     *recorder
	purpose governor.Purpose
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := governor.NewManualClock(t0)
	e, err := New(Config{Clock: clk, BodyCap: 64})
	if err != nil {
		t.Fatal(err)
	}
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

// sample is the summary every test starts from.
func sample() summary.Summary {
	return summary.Summary{
		NodeID: node, Repo: "phillipgreenii/example", Number: 7, Title: "A title", URL: "https://example.invalid/7",
		State: "OPEN", UpdatedAt: "2026-01-02T03:00:00Z", Mergeable: "MERGEABLE",
		HeadRefOid: "h1", HeadRefName: "feature", BaseRefName: "main", BaseRefOid: "b1", ChecksState: "SUCCESS",
	}
}

const ref = "phillipgreenii/example#7"

// seed stores the entity (version 5) and its summary.
func (r *rig) seed(s summary.Summary) {
	r.t.Helper()
	raw, err := json.Marshal(s)
	r.must(err)
	r.must(r.inTx(func(tx *store.Tx) error {
		if err := tx.PutEntity(context.Background(), store.EntityRow{Kind: LogKindPR, ID: s.NodeID, Version: 5}); err != nil {
			return err
		}
		return tx.PutGroup(context.Background(), store.GroupRow{
			Kind: LogKindPR, ID: s.NodeID, Group: groups.Summary, Content: raw, Generation: 1,
			FetchedAt: t0, FreshUntil: t0.Add(time.Hour), FetchedForHead: s.HeadRefOid, FetchedForBase: s.BaseRefOid,
		})
	}))
}

// seedGroup stores a non-summary group as a previous fetch left it.
func (r *rig) seedGroup(group, content, head, base string) {
	r.t.Helper()
	r.must(r.inTx(func(tx *store.Tx) error {
		return tx.PutGroup(context.Background(), store.GroupRow{
			Kind: LogKindPR, ID: node, Group: group, Content: []byte(content), Fingerprint: "seed", Generation: 1,
			FetchedAt: t0, FreshUntil: t0.Add(time.Hour), FetchedForHead: head, FetchedForBase: base,
		})
	}))
}

func (r *rig) storedSummary() summary.Summary {
	r.t.Helper()
	var s summary.Summary
	r.must(r.inTx(func(tx *store.Tx) error {
		row, ok, err := tx.GetGroup(context.Background(), LogKindPR, node, groups.Summary)
		if err != nil || !ok {
			r.t.Fatalf("stored summary: ok=%v err=%v", ok, err)
		}
		return json.Unmarshal(row.Content, &s)
	}))
	return s
}

// changeSummary stores a changed summary and runs DetectSummary in the same
// transaction, the way the persister's seam does.
func (r *rig) changeSummary(mutate func(*summary.Summary), tweak func(*summary.Stored)) error {
	r.t.Helper()
	return r.inTx(func(tx *store.Tx) error {
		ctx := context.Background()
		prev := r.storedSummary2(tx)
		next := prev
		mutate(&next)
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		row, _, err := tx.GetGroup(ctx, LogKindPR, node, groups.Summary)
		if err != nil {
			return err
		}
		row.Content, row.Generation, row.FetchedForHead, row.FetchedForBase = raw, row.Generation+1, next.HeadRefOid, next.BaseRefOid
		if err := tx.PutGroup(ctx, row); err != nil {
			return err
		}
		st := summary.Stored{Summary: next, Fingerprint: next.Fingerprint(), PrevFingerprint: prev.Fingerprint(), Prev: &prev}
		if tweak != nil {
			tweak(&st)
		}
		return r.e.DetectSummary(ctx, tx, &prev, st, summary.FetchInfo{Purpose: r.purpose})
	})
}

func (r *rig) storedSummary2(tx *store.Tx) summary.Summary {
	r.t.Helper()
	row, ok, err := tx.GetGroup(context.Background(), LogKindPR, node, groups.Summary)
	if err != nil || !ok {
		r.t.Fatalf("stored summary: ok=%v err=%v", ok, err)
	}
	var s summary.Summary
	r.must(json.Unmarshal(row.Content, &s))
	return s
}

// refresh stores a refreshed group and runs DetectGroup in the same
// transaction, the way the group engine's seam does.
func (r *rig) refresh(group, content, head, base string, started time.Time) error {
	r.t.Helper()
	return r.inTx(func(tx *store.Tx) error {
		ctx := context.Background()
		old, had, err := tx.GetGroup(ctx, LogKindPR, node, group)
		if err != nil {
			return err
		}
		var prev *store.GroupRow
		if had {
			prev = &old
		}
		now := r.clk.Now()
		next := store.GroupRow{
			Kind: LogKindPR, ID: node, Group: group, Content: []byte(content), Fingerprint: "fp",
			FetchedAt: now, FreshUntil: now.Add(time.Hour), Generation: old.Generation + 1,
			FetchedForHead: head, FetchedForBase: base,
		}
		if err := tx.PutGroup(ctx, next); err != nil {
			return err
		}
		return r.e.DetectGroup(ctx, tx, prev, next, groupsvc.RefreshInfo{Started: started, Purpose: r.purpose})
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

func (r *rig) version() int64 {
	r.t.Helper()
	var v int64
	r.must(r.inTx(func(tx *store.Tx) error {
		e, ok, err := tx.GetEntity(context.Background(), LogKindPR, node)
		if err != nil || !ok {
			r.t.Fatalf("entity: ok=%v err=%v", ok, err)
		}
		v = e.Version
		return nil
	}))
	return v
}

func fieldNames(row store.ChangeRow) []string {
	fs, err := classify.DecodeFields(row.Fields)
	if err != nil {
		panic(err)
	}
	var out []string
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return out
}

func fieldOf(t *testing.T, row store.ChangeRow, name string) classify.Field {
	t.Helper()
	fs, err := classify.DecodeFields(row.Fields)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("row has no field %q (fields %v)", name, fieldNames(row))
	return classify.Field{}
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

// Group content fixtures.
func runsContent(conclusion string) string {
	return `{"runs":[{"id":"1","name":"build","status":"completed","conclusion":"` + conclusion + `","head_sha":"h1"}]}`
}

func filesContent(path string) string {
	return `{"files":[{"path":"` + path + `","additions":1}]}`
}

func commitsContent(sha string) string { return `{"commits":[{"sha":"` + sha + `","author":"a"}]}` }

func detailContent(additions int) string {
	b, _ := json.Marshal(map[string]any{"merged": false, "mergeStateStatus": "CLEAN", "reviewRequests": []string{}, "additions": additions})
	return string(b)
}

func conversationContent() string { return `{}` }

func TestNewAppliesTheDefaultsAndRejectsNegatives(t *testing.T) {
	e, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if e.settle != 30*time.Second || e.bodyCap != 4*1024 || e.clock == nil {
		t.Errorf("defaults = settle %s, body cap %d, clock %v", e.settle, e.bodyCap, e.clock)
	}
	if _, err := New(Config{SettleTimeout: -1}); err == nil {
		t.Error("a negative settle timeout was accepted")
	}
	if _, err := New(Config{BodyCap: -1}); err == nil {
		t.Error("a negative body cap was accepted")
	}
}

func TestHintsAreUsedOnceAndExpire(t *testing.T) {
	r := newRig(t)
	r.e.NoteOrigin(node, groups.Summary, groupsvc.OriginWrite)
	if got := r.e.originFor(node, groups.Summary, governor.PurposeInteractive); got != groupsvc.OriginWrite {
		t.Errorf("origin with a hint = %q, want write", got)
	}
	if got := r.e.originFor(node, groups.Summary, governor.PurposeInteractive); got != groupsvc.OriginRead {
		t.Errorf("the hint was not cleared: origin = %q, want read", got)
	}
	if got := r.e.originFor(node, groups.Summary, governor.PurposeFill); got != groupsvc.OriginRefresh {
		t.Errorf("origin of a background purpose = %q, want refresh", got)
	}
	r.e.NoteOrigin(node, groups.Detail, groupsvc.OriginWrite)
	if got := r.e.originFor(node, groups.Summary, governor.PurposeRefresh); got != groupsvc.OriginRefresh {
		t.Errorf("a hint for another group leaked: %q", got)
	}
	r.clk.Advance(11 * time.Minute)
	if got := r.e.originFor(node, groups.Detail, governor.PurposeRefresh); got != groupsvc.OriginRefresh {
		t.Errorf("a hint older than ten minutes was used: %q", got)
	}
}

func TestEngineIsAnOriginHintsReceiver(t *testing.T) {
	var _ groupsvc.OriginHints = (*Engine)(nil)
	var _ Observer = (*recorder)(nil)
}
