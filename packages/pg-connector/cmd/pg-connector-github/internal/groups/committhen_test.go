package groups

import (
	"context"
	"errors"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

func TestCommitThenRunsThenInTheSameTransactionOnlyWhenStored(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	tok, _ := Begin(ctx, s, KindPR, "PR_1", Detail)

	calls := 0
	stored, err := CommitThen(ctx, s, tok, detailRow("one"), func(tx *store.Tx) error {
		calls++
		// The group is already written, and visible to the transaction only.
		g, ok, err := tx.GetGroup(ctx, KindPR, "PR_1", Detail)
		if err != nil || !ok || string(g.Content) != "one" || g.Generation != 1 {
			t.Errorf("inside the transaction the row = %+v ok=%v err=%v", g, ok, err)
		}
		return nil
	})
	if err != nil || !stored || calls != 1 {
		t.Fatalf("CommitThen = %v, %v, then called %d times", stored, err, calls)
	}
	if g, _ := contentOf(t, s, Detail); string(g.Content) != "one" || g.Generation != 1 || g.Kind != KindPR || g.Group != Detail {
		t.Errorf("row = %+v", g)
	}

	// The same token is now superseded: nothing is written and then is not run.
	stored, err = CommitThen(ctx, s, tok, detailRow("late"), func(*store.Tx) error {
		t.Error("then ran for a superseded commit")
		return nil
	})
	if err != nil || stored {
		t.Fatalf("superseded CommitThen = %v, %v", stored, err)
	}
	if g, _ := contentOf(t, s, Detail); string(g.Content) != "one" {
		t.Errorf("a superseded commit wrote: %+v", g)
	}
}

func TestCommitThenRollsTheGroupBackWhenThenFails(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	tok, _ := Begin(ctx, s, KindPR, "PR_1", Detail)
	if ok, err := Commit(ctx, s, tok, detailRow("old")); err != nil || !ok {
		t.Fatal(ok, err)
	}
	tok, _ = Begin(ctx, s, KindPR, "PR_1", Detail)
	boom := errors.New("boom")
	stored, err := CommitThen(ctx, s, tok, detailRow("new"), func(*store.Tx) error { return boom })
	if !errors.Is(err, boom) || stored {
		t.Fatalf("CommitThen = %v, %v, want the hook's error and not stored", stored, err)
	}
	if g, _ := contentOf(t, s, Detail); string(g.Content) != "old" || g.Generation != 1 {
		t.Errorf("row = %+v, want the old row in place", g)
	}
	// The token is still good: the row did not move.
	if ok, err := CommitThen(ctx, s, tok, detailRow("new"), nil); err != nil || !ok {
		t.Errorf("a nil then must commit like Commit: %v, %v", ok, err)
	}
}
