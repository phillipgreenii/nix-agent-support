package feed

import (
	"context"
	"testing"
	"time"
)

func TestPositionsReportsLagAndTheOldestUnackedRow(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if ps, err := r.f.Positions(ctx); err != nil || len(ps) != 0 {
		t.Fatalf("no keys: %v, %v", ps, err)
	}
	r.poll(viewAll, "a", "mine")
	r.poll(viewFlagged, "b", "mine")
	if ps, _ := r.f.Positions(ctx); len(ps) != 2 || ps[0].Head != 0 || ps[0].LagRows != 0 {
		t.Fatalf("empty log: %+v", ps)
	}

	r.clk.Advance(time.Minute)
	first := r.clk.Now()
	r.plain(refA)
	r.clk.Advance(time.Minute)
	r.plain(refOther) // not a member of mine
	r.clk.Advance(time.Minute)
	r.add(refA, []string{kindFlag})

	ps, err := r.f.Positions(ctx)
	if err != nil || len(ps) != 2 {
		t.Fatalf("positions = %+v, %v", ps, err)
	}
	byKind := map[string]Position{}
	for _, p := range ps {
		byKind[p.View] = p
	}
	all, flagged := byKind[viewAll], byKind[viewFlagged]
	if all.Consumer != "a" || all.Scope != "mine" || all.Head != 3 || all.AckedSeq != 0 || all.LagRows != 2 || !all.OldestUnacked.Equal(first) {
		t.Fatalf("all key = %+v", all)
	}
	if flagged.Consumer != "b" || flagged.LagRows != 1 || flagged.OldestUnacked.Equal(first) {
		t.Fatalf("flagged key = %+v (only the flag row lags)", flagged)
	}
	if !all.SeenAt.Equal(t0) {
		t.Fatalf("seen_at = %v", all.SeenAt)
	}

	r.ack(viewAll, "a", "mine", 3)
	ps, _ = r.f.Positions(ctx)
	for _, p := range ps {
		if p.View == viewAll && (p.LagRows != 0 || !p.OldestUnacked.IsZero()) {
			t.Fatalf("acked key still lags: %+v", p)
		}
	}
}

func TestPositionsOfAKeyWhoseQueryIsGoneCountsNoMember(t *testing.T) {
	r := newRig(t)
	r.poll(viewAll, "a", "mine")
	r.plain(refA)
	r.q.mu.Lock()
	delete(r.q.members, "mine")
	r.q.mu.Unlock()
	ps, err := r.f.Positions(context.Background())
	if err != nil || len(ps) != 1 || ps[0].LagRows != 0 {
		t.Fatalf("positions = %+v, %v", ps, err)
	}
}
