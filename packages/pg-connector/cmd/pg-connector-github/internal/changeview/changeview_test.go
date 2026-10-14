package changeview

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

const (
	nodeA = "PR_node_a"
	nodeB = "PR_node_unaliased"
	refA  = "example-org/example-repo#42"
	refB  = "example-org/example-repo#43"
)

// fakeSettle is the lifecycle's Stats, set by the test.
type fakeSettle struct {
	stats changes.Stats
	err   error
}

func (f fakeSettle) Stats(context.Context) (changes.Stats, error) { return f.stats, f.err }

// queries is a feed.Queries with one known query whose members the test sets.
type queries map[string][]string

func (q queries) Known(name string) bool { _, ok := q[name]; return ok }
func (q queries) Members(_ context.Context, name string) (feed.Members, error) {
	return feed.Members{Refs: q[name], Baseline: true}, nil
}
func (q queries) Run(context.Context, string) error { return nil }

type rig struct {
	t   *testing.T
	st  *store.Store
	clk *governor.ManualClock
	f   *feed.Feed
	cfg Config
	set *fakeSettle
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := governor.NewManualClock(t0)
	f, err := feed.New(feed.Config{
		Store: st, Queries: queries{"mine": {refA}}, Clock: clk,
		Retention: 14 * 24 * time.Hour, ConsumerStaleAfter: 7 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	set := &fakeSettle{}
	ctx := context.Background()
	if err := st.PutAlias(ctx, store.AliasRow{NodeID: nodeA, Ref: refA, SeenAt: t0}); err != nil {
		t.Fatal(err)
	}
	return &rig{
		t: t, st: st, clk: clk, f: f, set: set,
		cfg: Config{Store: st, Settle: set, Positions: f, Refs: entity.NewIdentity(st, clk), Clock: clk},
	}
}

func (r *rig) appendChange(ref string, kinds []string, origin string) int64 {
	r.t.Helper()
	var seq int64
	err := r.st.InTx(context.Background(), func(tx *store.Tx) error {
		var err error
		seq, err = tx.AppendChange(context.Background(), store.ChangeRow{
			Kind: changes.LogKindPR, ID: ref, Version: seq + 1, Kinds: kinds, Origin: origin, At: r.clk.Now(),
		})
		return err
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return seq
}

func (r *rig) putPending(node string, awaited []changes.Awaiting, kinds []string, detected, deadline time.Time) {
	r.t.Helper()
	raw, err := json.Marshal(awaited)
	if err != nil {
		r.t.Fatal(err)
	}
	err = r.st.InTx(context.Background(), func(tx *store.Tx) error {
		return tx.PutPending(context.Background(), store.PendingChange{
			Kind: changes.LogKindPR, ID: node, Kinds: kinds, Awaited: raw, Origin: "refresh", DetectedAt: detected, Deadline: deadline,
		})
	})
	if err != nil {
		r.t.Fatal(err)
	}
}

func TestStatusSourceReadsTheStoreTheLifecycleAndTheFeed(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	src, err := NewStatusSource(r.cfg)
	if err != nil {
		t.Fatal(err)
	}

	// An empty engine: no log, no rows, no keys.
	head, oldest, err := src.ChangeLog(ctx)
	if err != nil || head != 0 || oldest != 0 {
		t.Fatalf("empty log: %d %d %v", head, oldest, err)
	}
	if rows, err := src.PendingRows(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("pending: %v %v", rows, err)
	}
	if cs, err := src.Consumers(ctx); err != nil || len(cs) != 0 {
		t.Fatalf("consumers: %v %v", cs, err)
	}

	// A consumer key (first poll, at the tail), then rows it has not acknowledged.
	if _, err := r.f.Poll(ctx, feed.PollRequest{Kind: feed.KindPR, Consumer: "consumer-a", Query: "mine"}); err != nil {
		t.Fatal(err)
	}
	r.clk.Advance(time.Minute)
	first := r.clk.Now()
	r.appendChange(refA, []string{"head_changed"}, "refresh")
	r.clk.Advance(time.Minute)
	r.appendChange(refA, []string{"title_changed"}, "read")
	r.clk.Advance(2*time.Minute + 500*time.Millisecond)

	r.putPending(nodeA,
		[]changes.Awaiting{{Group: "files", Since: t0.Add(1500 * time.Millisecond)}, {Group: "commits", Since: t0}},
		[]string{"head_changed"}, t0.Add(100*time.Millisecond), t0.Add(30*time.Second+900*time.Millisecond))
	r.putPending(nodeB, []changes.Awaiting{{Group: "conversation", Since: t0}}, nil, t0, t0.Add(5*time.Second))
	r.set.stats = changes.Stats{Pending: 2, Timeouts: 3, Failures: 1}

	head, oldest, err = src.ChangeLog(ctx)
	if err != nil || head != 2 || oldest != 1 {
		t.Errorf("log: head %d oldest %d err %v", head, oldest, err)
	}

	rows, err := src.PendingRows(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("pending rows %+v %v", rows, err)
	}
	// Soonest deadline first (the store's order), reference where the alias is
	// known and the node id where it is not; groups in the stored order; times
	// UTC at whole seconds.
	if rows[0].ID != nodeB || strings.Join(rows[0].Awaited, ",") != "conversation" {
		t.Errorf("row 0 %+v", rows[0])
	}
	if rows[1].ID != refA || strings.Join(rows[1].Awaited, ",") != "files,commits" ||
		!rows[1].DetectedAt.Equal(t0) || !rows[1].Deadline.Equal(t0.Add(30*time.Second)) || rows[1].Deadline.Location() != time.UTC {
		t.Errorf("row 1 %+v", rows[1])
	}

	st, err := src.SettleStats(ctx)
	if err != nil || st.Timeouts != 3 || st.Failures != 1 {
		t.Errorf("stats %+v %v", st, err)
	}

	cs, err := src.Consumers(ctx)
	if err != nil || len(cs) != 1 {
		t.Fatalf("consumers %+v %v", cs, err)
	}
	c := cs[0]
	// The age is the adapter's clock minus the oldest unacknowledged row's time:
	// now = t0+4m0.5s, first row at t0+1m, so 3m0.5s, whole seconds 180.
	if c.Consumer != "consumer-a" || c.Kind != "pr" || c.Query != "mine" || c.AckedSeq != 0 || c.LagRows != 2 ||
		c.OldestUnackedAgeSeconds != int64(r.clk.Now().Sub(first).Seconds()) || c.OldestUnackedAgeSeconds != 180 || !c.SeenAt.Equal(t0) {
		t.Errorf("consumer %+v", c)
	}

	// Acknowledged: no lag and no age.
	if err := r.f.Ack(ctx, feed.KindPR, "consumer-a", "mine", 2); err != nil {
		t.Fatal(err)
	}
	cs, _ = src.Consumers(ctx)
	if cs[0].LagRows != 0 || cs[0].OldestUnackedAgeSeconds != 0 || cs[0].AckedSeq != 2 {
		t.Errorf("acked consumer %+v", cs[0])
	}
}

func TestStatusSourceAgesFollowTheAdaptersClockNotTheWallClock(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	src, _ := NewStatusSource(r.cfg)
	if _, err := r.f.Poll(ctx, feed.PollRequest{Kind: feed.KindPR, Consumer: "c", Query: "mine"}); err != nil {
		t.Fatal(err)
	}
	r.appendChange(refA, []string{"head_changed"}, "refresh")
	r.clk.Advance(10 * time.Minute)
	cs, _ := src.Consumers(ctx)
	if cs[0].OldestUnackedAgeSeconds != 600 {
		t.Errorf("age %d, want 600 from the manual clock", cs[0].OldestUnackedAgeSeconds)
	}
	r.clk.Advance(5 * time.Minute)
	cs, _ = src.Consumers(ctx)
	if cs[0].OldestUnackedAgeSeconds != 900 {
		t.Errorf("age %d, want 900", cs[0].OldestUnackedAgeSeconds)
	}
}

func TestStatusSourcePassesFailuresThrough(t *testing.T) {
	r := newRig(t)
	boom := errors.New("lifecycle down")
	r.set.err = boom
	src, _ := NewStatusSource(r.cfg)
	if _, err := src.SettleStats(context.Background()); !errors.Is(err, boom) {
		t.Errorf("err %v", err)
	}
	if _, err := NewStatusSource(Config{Store: r.st}); err == nil {
		t.Error("the status source needs every collaborator")
	}
}

func TestReaderGivesThePendingRowOfAnEntityKeyAndTheLastChangesOfAReference(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	rd, err := NewReader(Config{Store: r.st})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := rd.PendingSettle(ctx, nodeA); err != nil || ok {
		t.Fatalf("no pending row: %v %v", ok, err)
	}
	if rows, err := rd.LastChanges(ctx, refA, 5); err != nil || len(rows) != 0 {
		t.Fatalf("no rows: %v %v", rows, err)
	}

	r.putPending(nodeA,
		[]changes.Awaiting{{Group: "files", Since: t0.Add(2 * time.Second)}, {Group: "commits", Since: t0.Add(3 * time.Second)}},
		[]string{"head_changed", "files_changed"}, t0.Add(time.Second), t0.Add(31*time.Second))
	r.putPending(nodeB, []changes.Awaiting{{Group: "runs", Since: t0}}, nil, t0, t0.Add(time.Second))
	for i := 0; i < 7; i++ {
		r.clk.Advance(time.Minute)
		r.appendChange(refA, []string{"head_changed"}, "refresh")
	}
	r.appendChange(refB, []string{"title_changed"}, "read") // another pull request

	p, ok, err := rd.PendingSettle(ctx, nodeA)
	if err != nil || !ok {
		t.Fatalf("pending: %v %v", ok, err)
	}
	if len(p.Awaited) != 2 || p.Awaited[0].Group != "files" || !p.Awaited[0].Since.Equal(t0.Add(2*time.Second)) || p.Awaited[1].Group != "commits" ||
		!p.DetectedAt.Equal(t0.Add(time.Second)) || !p.Deadline.Equal(t0.Add(31*time.Second)) || strings.Join(p.Kinds, ",") != "head_changed,files_changed" {
		t.Errorf("pending %+v", p)
	}
	if _, ok, _ := rd.PendingSettle(ctx, "PR_never_seen"); ok {
		t.Error("an entity with no pending row has none")
	}

	rows, err := rd.LastChanges(ctx, refA, 5)
	if err != nil || len(rows) != 5 {
		t.Fatalf("rows %+v %v", rows, err)
	}
	if rows[0].Seq != 7 || rows[4].Seq != 3 || rows[0].Version == 0 || rows[0].Origin != "refresh" || !rows[0].At.Equal(t0.Add(7*time.Minute)) ||
		strings.Join(rows[0].Kinds, ",") != "head_changed" {
		t.Errorf("rows %+v (newest first, only this pull request's)", rows)
	}
	if other, _ := rd.LastChanges(ctx, refB, 5); len(other) != 1 || other[0].Seq != 8 {
		t.Errorf("other pull request %+v", other)
	}
	if _, err := NewReader(Config{}); err == nil {
		t.Error("the reader needs the store")
	}
}
