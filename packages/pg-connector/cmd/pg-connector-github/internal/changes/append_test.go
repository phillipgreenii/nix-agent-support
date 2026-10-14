package changes

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

func (r *rig) complete(p store.PendingChange, result string) error {
	return r.inTx(func(tx *store.Tx) error { return r.e.Complete(context.Background(), tx, p, result) })
}

// pendingRollup leaves a pending row awaiting runs, carrying the checks_state
// field.
func (r *rig) pendingRollup() store.PendingChange {
	r.t.Helper()
	r.seed(sample())
	r.seedGroup(groups.Runs, runsContent("success"), "h1", "b1")
	r.clk.Advance(time.Minute)
	r.must(r.changeSummary(func(s *summary.Summary) { s.ChecksState = "FAILURE" }, nil))
	p, ok := r.pending()
	if !ok {
		r.t.Fatal("no pending row")
	}
	return p
}

func TestCompleteAppendsThePendingRowAndReportsEachResultWord(t *testing.T) {
	for _, word := range []string{ResultSettled, ResultFailed, ResultTimeout} {
		t.Run(word, func(t *testing.T) {
			r := newRig(t)
			p := r.pendingRollup()
			r.clk.Advance(30 * time.Second)
			r.must(r.complete(p, word))
			rows := r.changes()
			if len(rows) != 1 || r.version() != 6 {
				t.Fatalf("rows = %d, version = %d; want 1 and 6", len(rows), r.version())
			}
			wantKinds(t, rows[0].Kinds, classify.KindCIChanged)
			if !slices.Equal(fieldNames(rows[0]), []string{"checks_state"}) || rows[0].ID != ref || rows[0].HeadSHA != "h1" || !rows[0].At.Equal(r.clk.Now()) {
				t.Errorf("row = %+v", rows[0])
			}
			if _, ok := r.pending(); ok {
				t.Error("the pending row survived")
			}
			if !slices.Equal(r.obs.settled, []string{word}) {
				t.Errorf("settled = %v, want [%s]", r.obs.settled, word)
			}
		})
	}
}

func TestCompleteDropsAPendingRowWhoseMergedResultIsEmpty(t *testing.T) {
	r := newRig(t)
	r.seed(sample())
	r.seedGroup(groups.Conversation, conversationContent(), "", "")
	r.must(r.changeSummary(func(s *summary.Summary) { s.UpdatedAt = "2026-01-02T05:00:00Z" }, nil))
	p, _ := r.pending()
	r.must(r.complete(p, ResultTimeout))
	if len(r.changes()) != 0 || r.version() != 5 {
		t.Errorf("rows = %d, version = %d; want none and 5", len(r.changes()), r.version())
	}
	if _, ok := r.pending(); ok {
		t.Error("the pending row survived")
	}
	if !slices.Equal(r.obs.settled, []string{ResultTimeout}) {
		t.Errorf("settled = %v", r.obs.settled)
	}
}

func TestCompleteOnAPendingRowWhoseEntityOrSummaryIsGoneDeletesItQuietly(t *testing.T) {
	pending := store.PendingChange{
		Kind: LogKindPR, ID: node, Kinds: []string{classify.KindCIChanged},
		Fields: []byte(`[{"name":"checks_state","before":"A","after":"B"}]`), Awaited: []byte(`[]`), DetectedAt: t0, Deadline: t0,
	}
	t.Run("no entity and no summary", func(t *testing.T) {
		r := newRig(t)
		r.must(r.inTx(func(tx *store.Tx) error { return tx.PutPending(context.Background(), pending) }))
		r.must(r.complete(pending, ResultTimeout))
		wantNoChange(t, r)
		if len(r.obs.settled) != 0 {
			t.Errorf("settled = %v, want nothing reported", r.obs.settled)
		}
	})
	t.Run("entity with no summary", func(t *testing.T) {
		r := newRig(t)
		r.must(r.inTx(func(tx *store.Tx) error {
			if err := tx.EnsureEntity(context.Background(), LogKindPR, node); err != nil {
				return err
			}
			return tx.PutPending(context.Background(), pending)
		}))
		r.must(r.complete(pending, ResultTimeout))
		wantNoChange(t, r)
		if len(r.obs.settled) != 0 {
			t.Errorf("settled = %v", r.obs.settled)
		}
	})
	t.Run("summary with no entity row", func(t *testing.T) {
		r := newRig(t)
		raw, err := json.Marshal(sample())
		r.must(err)
		r.must(r.inTx(func(tx *store.Tx) error {
			// An evicted entity: the summary group is there, the entity row is not.
			if err := tx.PutGroup(context.Background(), store.GroupRow{Kind: LogKindPR, ID: node, Group: groups.Summary, Content: raw, Generation: 1}); err != nil {
				return err
			}
			return tx.PutPending(context.Background(), pending)
		}))
		r.must(r.complete(pending, ResultTimeout))
		wantNoChange(t, r)
		if len(r.obs.settled) != 0 {
			t.Errorf("settled = %v", r.obs.settled)
		}
	})
}

func TestCompleteOnARowThatIsNoLongerPendingDoesNothing(t *testing.T) {
	r := newRig(t)
	p := r.pendingRollup()
	r.must(r.complete(p, ResultSettled))
	r.must(r.complete(p, ResultSettled)) // the lifecycle's stale copy
	if len(r.changes()) != 1 || r.version() != 6 || len(r.obs.settled) != 1 {
		t.Errorf("rows = %d, version = %d, settled = %v", len(r.changes()), r.version(), r.obs.settled)
	}
}

func TestADetectionThatFailsInsideTheTransactionLeavesNothingBehind(t *testing.T) {
	boom := errors.New("boom")
	t.Run("a creating detection", func(t *testing.T) {
		r := newRig(t)
		r.seed(sample())
		r.seedGroup(groups.Runs, runsContent("success"), "h1", "b1")
		err := r.inTx(func(tx *store.Tx) error {
			prev := sample()
			next := prev
			next.ChecksState = "FAILURE"
			st := summary.Stored{Summary: next, Fingerprint: next.Fingerprint(), PrevFingerprint: prev.Fingerprint(), Prev: &prev}
			if err := r.e.DetectSummary(context.Background(), tx, &prev, st, summary.FetchInfo{}); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		wantNoChange(t, r)
		if r.version() != 5 {
			t.Errorf("version = %d, want 5", r.version())
		}
		var fresh time.Time
		r.must(r.inTx(func(tx *store.Tx) error {
			row, _, err := tx.GetGroup(context.Background(), LogKindPR, node, groups.Runs)
			fresh = row.FreshUntil
			return err
		}))
		if !fresh.Equal(t0.Add(time.Hour)) {
			t.Errorf("runs fresh_until = %v, want the seeded value (the early expiry rolled back)", fresh)
		}
	})
	t.Run("an appending detection", func(t *testing.T) {
		r := newRig(t)
		p := r.pendingRollup()
		r.clk.Advance(time.Second)
		err := r.inTx(func(tx *store.Tx) error {
			if err := r.e.Complete(context.Background(), tx, p, ResultSettled); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if len(r.changes()) != 0 || r.version() != 5 {
			t.Errorf("rows = %d, version = %d; want none and 5", len(r.changes()), r.version())
		}
		if _, ok := r.pending(); !ok {
			t.Error("the pending row was lost with the rolled-back append")
		}
	})
}
