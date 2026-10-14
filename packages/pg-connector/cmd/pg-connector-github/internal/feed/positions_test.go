package feed

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
)

func TestPositionsReportsLagAndTheOldestUnackedRow(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if ps, err := r.f.Positions(ctx); err != nil || len(ps) != 0 {
		t.Fatalf("no keys: %v, %v", ps, err)
	}
	r.poll(KindPR, "a", "mine")
	r.poll(KindCI, "b", "mine")
	if ps, _ := r.f.Positions(ctx); len(ps) != 2 || ps[0].Head != 0 || ps[0].LagRows != 0 {
		t.Fatalf("empty log: %+v", ps)
	}

	r.clk.Advance(time.Minute)
	first := r.clk.Now()
	r.plain(refA)
	r.clk.Advance(time.Minute)
	r.plain(refOther) // not a member of mine
	r.clk.Advance(time.Minute)
	r.add(refA, []string{classify.KindCIChanged})

	ps, err := r.f.Positions(ctx)
	if err != nil || len(ps) != 2 {
		t.Fatalf("positions = %+v, %v", ps, err)
	}
	byKind := map[string]Position{}
	for _, p := range ps {
		byKind[p.Kind] = p
	}
	pr, ci := byKind[KindPR], byKind[KindCI]
	if pr.Consumer != "a" || pr.Query != "mine" || pr.Head != 3 || pr.AckedSeq != 0 || pr.LagRows != 2 || !pr.OldestUnacked.Equal(first) {
		t.Fatalf("pr key = %+v", pr)
	}
	if ci.Consumer != "b" || ci.LagRows != 1 || ci.OldestUnacked.Equal(first) {
		t.Fatalf("ci key = %+v (only the ci_changed row lags)", ci)
	}
	if !pr.SeenAt.Equal(t0) {
		t.Fatalf("seen_at = %v", pr.SeenAt)
	}

	r.ack(KindPR, "a", "mine", 3)
	ps, _ = r.f.Positions(ctx)
	for _, p := range ps {
		if p.Kind == KindPR && (p.LagRows != 0 || !p.OldestUnacked.IsZero()) {
			t.Fatalf("acked key still lags: %+v", p)
		}
	}
}

func TestPositionsOfAKeyWhoseQueryIsGoneCountsNoMember(t *testing.T) {
	r := newRig(t)
	r.poll(KindPR, "a", "mine")
	r.plain(refA)
	r.q.mu.Lock()
	delete(r.q.members, "mine")
	r.q.mu.Unlock()
	ps, err := r.f.Positions(context.Background())
	if err != nil || len(ps) != 1 || ps[0].LagRows != 0 {
		t.Fatalf("positions = %+v, %v", ps, err)
	}
}
