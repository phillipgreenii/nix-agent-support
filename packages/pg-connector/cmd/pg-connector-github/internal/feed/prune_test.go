package feed

import (
	"context"
	"testing"
)

func TestPruneDropsOldRowsAndUnseenKeysAndKeepsTheRest(t *testing.T) {
	r := newRig(t) // retention 14 days, consumer_stale_after 7 days
	ctx := context.Background()
	r.poll(KindPR, "gone", "mine")
	r.plain(refA) // t0
	r.clk.Advance(5 * day)
	r.poll(KindPR, "kept", "mine") // seen at day 5
	r.plain(refA)                  // day 5
	r.clk.Advance(5 * day)         // day 10: "gone" unseen 10 days, "kept" 5; row 1 is 10 days old
	r.plain(refA)

	rows, keys, err := r.f.Prune(ctx)
	if err != nil || rows != 0 || keys != 1 {
		t.Fatalf("prune at day 10 = %d rows, %d keys, %v; want 0 and 1", rows, keys, err)
	}
	if _, ok := r.pos(KindPR, "gone", "mine"); ok {
		t.Fatal("the unseen key survived")
	}
	if _, ok := r.pos(KindPR, "kept", "mine"); !ok {
		t.Fatal("the seen key was dropped")
	}

	r.clk.Advance(6 * day) // day 16: row 1 (day 0) is past 14 days, row 2 (day 5) is not
	rows, keys, err = r.f.Prune(ctx)
	if err != nil || rows != 1 || keys != 1 {
		t.Fatalf("prune at day 16 = %d rows, %d keys, %v; want 1 and 1", rows, keys, err)
	}
	left, err := r.st.ChangesAfter(ctx, 0, 0)
	if err != nil || len(left) != 2 {
		t.Fatalf("rows left = %d, %v", len(left), err)
	}
	if head := r.head(); head != 3 {
		t.Fatalf("pruning lowered the head to %d", head)
	}
}

func TestPruneForgetsTheRedeliveryBookkeepingOfDroppedKeys(t *testing.T) {
	r := newRig(t)
	r.poll(KindPR, "c", "mine")
	r.plain(refA)
	r.poll(KindPR, "c", "mine")
	if len(r.f.handed) != 1 {
		t.Fatalf("handed = %v", r.f.handed)
	}
	r.clk.Advance(8 * day)
	if _, keys, err := r.f.Prune(context.Background()); err != nil || keys != 1 {
		t.Fatalf("prune = %d, %v", keys, err)
	}
	if len(r.f.handed) != 0 {
		t.Fatalf("handed = %v", r.f.handed)
	}
}
