package changes

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// putQueries stores the query rows the removed row is derived from.
func (r *rig) putQueries(qs ...store.QueryRow) {
	r.t.Helper()
	r.must(r.inTx(func(tx *store.Tx) error {
		for _, q := range qs {
			if err := tx.PutQuery(context.Background(), q); err != nil {
				return err
			}
		}
		return nil
	}))
}

// tombstone does what the seam's transaction does, then calls DetectGone.
func (r *rig) tombstone(purpose governor.Purpose) error {
	return r.inTx(func(tx *store.Tx) error {
		ctx := context.Background()
		e, ok, err := tx.GetEntity(ctx, LogKindPR, node)
		if err != nil || !ok {
			return err
		}
		e.TombstonedAt = r.clk.Now()
		if err := tx.PutEntity(ctx, e); err != nil {
			return err
		}
		return r.e.DetectGone(ctx, tx, node, purpose)
	})
}

func TestATombstoneAppendsOneRemovedRowNamingEveryQueryThatListedTheEntity(t *testing.T) {
	r := newRig(t)
	r.seed(sample())
	r.putQueries(
		store.QueryRow{Key: "mine", Configured: true, IDs: []string{"other/x#1", "PhillipGreenii/Example#7"}},
		store.QueryRow{Key: "review", Configured: true, IDs: []string{"phillipgreenii/example#7"}},
		store.QueryRow{Key: "elsewhere", Configured: true, IDs: []string{"phillipgreenii/example#70", "phillipgreenii/example#8"}},
		store.QueryRow{Key: "adhoc", Configured: false, IDs: []string{"phillipgreenii/example#7"}},
	)
	r.clk.Advance(time.Minute)
	r.must(r.tombstone(governor.PurposeRefresh))

	rows := r.changes()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	wantKinds(t, row.Kinds, classify.KindRemoved)
	if !slices.Equal(fieldNames(row), []string{"query:mine", "query:review"}) {
		t.Errorf("fields = %v, want query:mine and query:review", fieldNames(row))
	}
	if f := fieldOf(t, row, "query:mine"); string(f.Before) != `"mine"` || len(f.After) != 0 {
		t.Errorf("query:mine = %s -> %s", f.Before, f.After)
	}
	if row.ID != ref || row.Version != 6 || row.HeadSHA != "h1" || row.Origin != "refresh" {
		t.Errorf("row = %+v", row)
	}

	// A repeat for the same tombstone appends nothing and bumps nothing.
	r.must(r.inTx(func(tx *store.Tx) error {
		return r.e.DetectGone(context.Background(), tx, node, governor.PurposeRefresh)
	}))
	if len(r.changes()) != 1 || r.version() != 6 {
		t.Errorf("a repeat appended: %d rows, version %d", len(r.changes()), r.version())
	}
	wantDetected := []detectedCall{{classify.KindRemoved, "refresh", row.At.Sub(row.OriginUpdatedAt)}}
	if !slices.Equal(r.obs.detected, wantDetected) {
		t.Errorf("detected = %+v, want %+v", r.obs.detected, wantDetected)
	}

	// A later tombstone (the entity came back and went again) is a new removal.
	r.clk.Advance(time.Hour)
	r.must(r.tombstone(governor.PurposeInteractive))
	rows = r.changes()
	if len(rows) != 2 || rows[1].Origin != "read" || r.version() != 7 {
		t.Errorf("second tombstone: %d rows, version %d", len(rows), r.version())
	}
}

func TestATombstoneForAnEntityWithNoStoredSummaryAppendsNothing(t *testing.T) {
	r := newRig(t)
	r.must(r.inTx(func(tx *store.Tx) error { return tx.EnsureEntity(context.Background(), LogKindPR, node) }))
	r.must(r.inTx(func(tx *store.Tx) error {
		return r.e.DetectGone(context.Background(), tx, node, governor.PurposeRefresh)
	}))
	wantNoChange(t, r)
}

func TestATombstoneForAnEntityWhoseSummaryIsGoneDeletesItsPendingRow(t *testing.T) {
	r := newRig(t)
	r.must(r.inTx(func(tx *store.Tx) error {
		if err := tx.EnsureEntity(context.Background(), LogKindPR, node); err != nil {
			return err
		}
		return tx.PutPending(context.Background(), store.PendingChange{Kind: LogKindPR, ID: node, Awaited: []byte(`[]`), DetectedAt: t0, Deadline: t0})
	}))
	r.must(r.inTx(func(tx *store.Tx) error {
		return r.e.DetectGone(context.Background(), tx, node, governor.PurposeRefresh)
	}))
	wantNoChange(t, r)
}

func TestATombstoneMergesIntoAndEndsThePendingRow(t *testing.T) {
	r := newRig(t)
	r.seed(sample())
	r.seedGroup("runs", runsContent("success"), "h1", "b1")
	r.putQueries(store.QueryRow{Key: "mine", Configured: true, IDs: []string{ref}})
	r.clk.Advance(time.Minute)
	r.must(r.changeSummary(func(s *summary.Summary) { s.ChecksState = "FAILURE" }, nil))
	if _, ok := r.pending(); !ok {
		t.Fatal("no pending row")
	}
	r.clk.Advance(time.Second)
	r.must(r.tombstone(governor.PurposeRefresh))

	rows := r.changes()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (the pending row merged into the removal)", len(rows))
	}
	wantKinds(t, rows[0].Kinds, classify.KindCIChanged, classify.KindRemoved)
	if !slices.Equal(fieldNames(rows[0]), []string{"checks_state", "query:mine"}) {
		t.Errorf("fields = %v", fieldNames(rows[0]))
	}
	if _, ok := r.pending(); ok {
		t.Error("the pending row survived the tombstone")
	}
	if r.version() != 6 {
		t.Errorf("version = %d, want 6", r.version())
	}
}
