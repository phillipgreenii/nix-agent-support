package changes

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

const key = "PROJ-1"

// rig is a real store, the manual clock and an engine over the Jira registration.
type rig struct {
	t   *testing.T
	st  *store.Store
	clk *governor.ManualClock
	e   *Engine
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
	return &rig{t: t, st: st, clk: clk, e: e}
}

func (r *rig) must(err error) {
	r.t.Helper()
	if err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) inTx(fn func(ctx context.Context, tx *store.Tx) error) error {
	r.t.Helper()
	return r.st.InTx(context.Background(), func(tx *store.Tx) error { return fn(context.Background(), tx) })
}

// seed stores the entity (version 5), its summary and its detail, and sets the
// watched flag when watched.
func (r *rig) seed(s groups.SummaryContent, d groups.DetailContent, watched bool) {
	r.t.Helper()
	r.must(r.inTx(func(ctx context.Context, tx *store.Tx) error {
		if err := tx.PutEntity(ctx, store.EntityRow{Kind: LogKindIssue, ID: key, Version: 5}); err != nil {
			return err
		}
		for _, g := range []struct {
			name    string
			content any
		}{{groups.Summary, groups.Stored(s)}, {groups.Detail, d}} {
			raw, err := json.Marshal(g.content)
			if err != nil {
				return err
			}
			if err := tx.PutGroup(ctx, store.GroupRow{
				Kind: LogKindIssue, ID: key, Group: g.name, Content: raw, Fingerprint: "seed", Generation: 1,
				FetchedAt: t0, FreshUntil: t0.Add(time.Hour),
			}); err != nil {
				return err
			}
		}
		if watched {
			_, err := tx.SetWatched(ctx, LogKindIssue, key, registry.ViaDirect, t0)
			return err
		}
		return nil
	}))
}

// storeSummary stores next as the summary and runs DetectSummary in the same
// transaction, the way the persister does.
func (r *rig) storeSummary(prev *groups.SummaryContent, next groups.SummaryContent, first bool) error {
	r.t.Helper()
	return r.inTx(func(ctx context.Context, tx *store.Tx) error {
		raw, err := json.Marshal(groups.Stored(next))
		if err != nil {
			return err
		}
		row, _, err := tx.GetGroup(ctx, LogKindIssue, key, groups.Summary)
		if err != nil {
			return err
		}
		row.Content, row.Generation = raw, row.Generation+1
		if err := tx.PutGroup(ctx, row); err != nil {
			return err
		}
		return r.e.DetectSummary(ctx, tx, prev, next, first, engine.RefreshInfo{Purpose: governor.PurposeRefresh})
	})
}

// refreshDetail stores a refreshed detail and runs DetectGroup in the same
// transaction, the way the group engine's seam does.
func (r *rig) refreshDetail(d groups.DetailContent, started time.Time) error {
	r.t.Helper()
	return r.inTx(func(ctx context.Context, tx *store.Tx) error {
		old, had, err := tx.GetGroup(ctx, LogKindIssue, key, groups.Detail)
		if err != nil {
			return err
		}
		var prev *store.GroupRow
		if had {
			prev = &old
		}
		raw, err := json.Marshal(d)
		if err != nil {
			return err
		}
		now := r.clk.Now()
		next := store.GroupRow{
			Kind: LogKindIssue, ID: key, Group: groups.Detail, Content: raw, Fingerprint: "fp",
			FetchedAt: now, FreshUntil: now.Add(time.Hour), Generation: old.Generation + 1,
		}
		if err := tx.PutGroup(ctx, next); err != nil {
			return err
		}
		return r.e.DetectGroup(ctx, tx, prev, next, engine.RefreshInfo{Started: started, Purpose: governor.PurposeRefresh})
	})
}

func (r *rig) changes() []store.ChangeRow {
	r.t.Helper()
	rows, err := r.st.ChangesAfter(context.Background(), 0, 0)
	r.must(err)
	return rows
}

func (r *rig) pending() []store.PendingChange {
	r.t.Helper()
	ps, err := r.st.ListPending(context.Background())
	r.must(err)
	return ps
}

func (r *rig) version() int64 {
	r.t.Helper()
	var v int64
	r.must(r.inTx(func(ctx context.Context, tx *store.Tx) error {
		e, ok, err := tx.GetEntity(ctx, LogKindIssue, key)
		if err != nil || !ok {
			r.t.Fatalf("entity: ok=%v err=%v", ok, err)
		}
		v = e.Version
		return nil
	}))
	return v
}

func (r *rig) wantNothing() {
	r.t.Helper()
	if rows := r.changes(); len(rows) != 0 {
		r.t.Fatalf("appended %d rows, want none: %+v", len(rows), rows)
	}
	if ps := r.pending(); len(ps) != 0 {
		r.t.Fatalf("%d pending rows, want none", len(ps))
	}
	if v := r.version(); v != 5 {
		r.t.Fatalf("version = %d, want 5", v)
	}
}

func TestNewAppliesTheConfigDefaultsToAZeroConfigAndRejectsNegatives(t *testing.T) {
	if _, err := New(Config{}); err != nil {
		t.Fatalf("a zero Config was rejected: %v", err)
	}
	if _, err := New(Config{SettleTimeout: -1}); err == nil {
		t.Error("a negative settle timeout was accepted")
	}
	if _, err := New(Config{BodyCap: -1}); err == nil {
		t.Error("a negative body cap was accepted")
	}
}

func TestAZeroSettleTimeoutTakesTheConfigDefault(t *testing.T) {
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	clk := governor.NewManualClock(t0)
	e, err := New(Config{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, st: st, clk: clk, e: e}
	r.seed(summaryOf("a"), groups.DetailContent{}, true)
	prev := summaryOf("a")
	r.must(r.storeSummary(&prev, summaryOf("a", "b"), false))
	ps := r.pending()
	if len(ps) != 1 {
		t.Fatalf("pending = %d, want 1", len(ps))
	}
	if got := ps[0].Deadline.Sub(ps[0].DetectedAt); got != config.DefaultSettleTimeout {
		t.Errorf("settle = %s, want %s", got, config.DefaultSettleTimeout)
	}
}

func TestAWatchedIssuesLabelChangeAppendsOneRowOnceTheDetailGroupSettles(t *testing.T) {
	r := newRig(t)
	prev := summaryOf("a")
	r.seed(prev, groups.DetailContent{Description: "d"}, true)
	r.clk.Advance(time.Minute)
	r.must(r.storeSummary(&prev, summaryOf("a", "b"), false))

	if rows := r.changes(); len(rows) != 0 {
		t.Fatalf("a row appended before the detail group settled: %+v", rows)
	}
	ps := r.pending()
	if len(ps) != 1 {
		t.Fatalf("pending = %d, want 1", len(ps))
	}
	aw, err := engine.DecodeAwaited(ps[0].Awaited)
	if err != nil || len(aw) != 1 || aw[0].Group != groups.Detail {
		t.Fatalf("awaited = %+v err = %v, want the detail group", aw, err)
	}

	r.clk.Advance(time.Second)
	r.must(r.refreshDetail(groups.DetailContent{Description: "d"}, r.clk.Now()))
	rows := r.changes()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if !slices.Equal(rows[0].Kinds, []string{classify.KindLabelsChanged}) {
		t.Errorf("kinds = %v, want labels_changed", rows[0].Kinds)
	}
	if rows[0].ID != key || rows[0].Version != 6 || !rows[0].Watched {
		t.Errorf("row = %+v", rows[0])
	}
	if ps := r.pending(); len(ps) != 0 {
		t.Errorf("the pending row survived: %+v", ps)
	}
}

func TestADetailChangeInTheSameWindowMergesIntoTheOneRow(t *testing.T) {
	r := newRig(t)
	prev := summaryOf("a")
	r.seed(prev, groups.DetailContent{Description: "one"}, true)
	r.clk.Advance(time.Minute)
	r.must(r.storeSummary(&prev, summaryOf("a", "b"), false))
	r.clk.Advance(time.Second)
	r.must(r.refreshDetail(groups.DetailContent{Description: "two"}, r.clk.Now()))
	rows := r.changes()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if !slices.Equal(rows[0].Kinds, []string{classify.KindDescriptionChanged, classify.KindLabelsChanged}) &&
		!slices.Equal(rows[0].Kinds, []string{classify.KindLabelsChanged, classify.KindDescriptionChanged}) {
		t.Errorf("kinds = %v", rows[0].Kinds)
	}
}

func TestAnUnwatchedIssuesChangeAppendsNothingBumpsNoVersionAndCreatesNoPendingRow(t *testing.T) {
	r := newRig(t)
	prev := summaryOf("a")
	r.seed(prev, groups.DetailContent{Description: "d"}, false)
	r.clk.Advance(time.Minute)
	r.must(r.storeSummary(&prev, summaryOf("a", "b"), false))
	r.wantNothing()
	// A detail refresh of an unwatched issue adds nothing either.
	r.clk.Advance(time.Second)
	r.must(r.refreshDetail(groups.DetailContent{Description: "other"}, r.clk.Now()))
	r.wantNothing()
}

func TestAChangeToUpdatedAloneAppendsNothing(t *testing.T) {
	r := newRig(t)
	prev := summaryOf("a")
	r.seed(prev, groups.DetailContent{Description: "d"}, true)
	r.clk.Advance(time.Minute)
	next := summaryOf("a")
	next.Updated = "2026-10-05T10:11:13.000+0000"
	r.must(r.storeSummary(&prev, next, false))
	r.clk.Advance(time.Second)
	r.must(r.refreshDetail(groups.DetailContent{Description: "d"}, r.clk.Now()))
	if rows := r.changes(); len(rows) != 0 {
		t.Fatalf("appended %d rows, want none: %+v", len(rows), rows)
	}
	if ps := r.pending(); len(ps) != 0 {
		t.Fatalf("the pending row survived: %+v", ps)
	}
	if v := r.version(); v != 5 {
		t.Errorf("version = %d, want 5", v)
	}
}

func TestABaselineAppendsNothing(t *testing.T) {
	prev := summaryOf("a")
	gone := summaryOf("a", "b")
	gone.Gone = true
	cases := []struct {
		name  string
		prev  *groups.SummaryContent
		next  groups.SummaryContent
		first bool
	}{
		{"nil prev", nil, summaryOf("a", "b"), false},
		{"first sighting", &prev, summaryOf("a", "b"), true},
		{"gone", &prev, gone, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.seed(prev, groups.DetailContent{}, true)
			r.must(r.storeSummary(c.prev, c.next, c.first))
			r.wantNothing()
		})
	}
}

func TestDetectSummaryHandsTheEngineTheStoredFormOfBothContents(t *testing.T) {
	r := newRig(t)
	prev := summaryOf("a")
	prev.Issue.AsOf, prev.Issue.Version = "2026-01-01T00:00:00Z", 3
	r.seed(prev, groups.DetailContent{}, true)
	next := summaryOf("a", "b")
	next.Issue.AsOf, next.Issue.Version = "2026-01-02T00:00:00Z", 4
	r.must(r.storeSummary(&prev, next, false))
	ps := r.pending()
	if len(ps) != 1 {
		t.Fatalf("pending = %d, want 1", len(ps))
	}
	// Only the labels differ after the normalisation; the as_of and version
	// movement is not a change, and the pending row records the labels field.
	fields, err := engine.DecodeFields(ps[0].Fields)
	r.must(err)
	names := make([]string, 0, len(fields))
	for _, f := range fields {
		names = append(names, f.Name)
	}
	if !slices.Equal(names, []string{"labels"}) {
		t.Errorf("fields = %v, want [labels]", names)
	}
}
