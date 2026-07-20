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

// TestDerivedState_SyncVsPublished_TreeEquivalence is the Phase-3 C2 behavioral-
// equivalence gate: the SynchronousMode Scan-on-tick path and the producer-
// decoupled Load-published-DerivedState path MUST build a deep-equal
// aggregate.Tree (GeneratedAt normalized).
func TestDerivedState_SyncVsPublished_TreeEquivalence(t *testing.T) {
	sessionsDir, home, pidAlive := buildEquivalenceCorpus(t)
	now := time.Unix(1_776_000_300, 0)
	ctx := context.Background()

	// Arm A — synchronous: Snapshot assembles the DerivedState inline (Scan on
	// the tick goroutine), publishes, and builds the tree from it.
	pa := newMonitorPoller(sessionsDir, home, pidAlive, now)
	treeA, keepA, err := pa.Snapshot(ctx)
	if err != nil {
		t.Fatalf("sync Snapshot: %v", err)
	}

	// Arm B — producer-decoupled: assemble + publish a DerivedState FIRST, then
	// Snapshot with SynchronousMode off Loads the published state (no re-scan)
	// and builds the tree from it.
	pb := newMonitorPoller(sessionsDir, home, pidAlive, now)
	ds, err := pb.Producer().Assemble(ctx, now)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	pb.Producer().Publish(ds)
	pb.Producer().SynchronousMode = false
	treeB, keepB, err := pb.Snapshot(ctx)
	if err != nil {
		t.Fatalf("published Snapshot: %v", err)
	}

	if keepA != keepB {
		t.Errorf("anyKeepAwake differs: sync=%v published=%v", keepA, keepB)
	}
	normalizeTree(treeA)
	normalizeTree(treeB)
	if !reflect.DeepEqual(treeA, treeB) {
		t.Fatalf("aggregate.Tree mismatch between sync and published paths:\nsync=%+v\npub =%+v", treeA, treeB)
	}
}
