package poller

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
)

// normalizeTree zeroes the wall-clock-derived fields so two trees built at the
// same logical `now` but different real instants compare equal.
func normalizeTree(t *aggregate.Tree) {
	if t == nil {
		return
	}
	t.GeneratedAt = time.Time{}
}

// TestDerivedState_Determinism is the surviving guard after the transitional
// sync-on-tick flag removal (former Phase-3 C2 sync-vs-async equivalence gate).
// With async the sole path, the property worth pinning is that the producer→tick
// pipeline is deterministic: two independent Assemble+Publish runs over the same
// corpus at the same logical `now` MUST build a deep-equal aggregate.Tree
// (GeneratedAt normalized) and agree on anyKeepAwake.
func TestDerivedState_Determinism(t *testing.T) {
	sessionsDir, home, pidAlive := buildEquivalenceCorpus(t)
	now := time.Unix(1_776_000_300, 0)
	ctx := context.Background()

	build := func() (*aggregate.Tree, bool) {
		p := newMonitorPoller(sessionsDir, home, pidAlive, now)
		tree, keep, err := assembleAndSnapshot(t, p, ctx)
		if err != nil {
			t.Fatalf("assembleAndSnapshot: %v", err)
		}
		return tree, keep
	}

	treeA, keepA := build()
	treeB, keepB := build()

	if keepA != keepB {
		t.Errorf("anyKeepAwake differs across runs: A=%v B=%v", keepA, keepB)
	}
	normalizeTree(treeA)
	normalizeTree(treeB)
	if !reflect.DeepEqual(treeA, treeB) {
		t.Fatalf("aggregate.Tree not deterministic across two independent runs:\nA=%+v\nB=%+v", treeA, treeB)
	}
}
