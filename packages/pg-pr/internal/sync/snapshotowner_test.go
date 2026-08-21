package sync

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

func TestSnapshotModel_UpsertDeleteSorted(t *testing.T) {
	m := newSnapshotModel()
	m.upsert(snapshot.PRInput{PR: api.PR{Repo: "o/r", Number: 2, Author: "me", State: "open"}})
	m.upsert(snapshot.PRInput{PR: api.PR{Repo: "o/r", Number: 1, Author: "me", State: "open"}})
	m.upsert(snapshot.PRInput{PR: api.PR{Repo: "a/b", Number: 9, Author: "me", State: "open"}})
	got := m.sortedInputs()
	want := []struct {
		repo string
		num  int
	}{{"a/b", 9}, {"o/r", 1}, {"o/r", 2}}
	if len(got) != 3 {
		t.Fatalf("len = %d", len(got))
	}
	for i, w := range want {
		if got[i].PR.Repo != w.repo || got[i].PR.Number != w.num {
			t.Errorf("[%d] = %s#%d, want %s#%d", i, got[i].PR.Repo, got[i].PR.Number, w.repo, w.num)
		}
	}
	m.delete(prKey{Repo: "o/r", Number: 1})
	if got := m.sortedInputs(); len(got) != 2 {
		t.Fatalf("after delete len = %d, want 2", len(got))
	}
}

// TestSnapshotModel_PruneExpiredMerged verifies pruneExpiredMerged (pg2-ew4kf)
// bounds the retained model's memory: a merged PR of mine past
// snapshot.MergedRetentionWindow is removed from the underlying map (not just
// excluded from Build's output), while a merged mine PR still within the
// window, an active mine PR, and a merged TEAM PR (never actually retained by
// refreshPR, but the pruner must not mishandle one if it somehow got upserted)
// are all left alone.
func TestSnapshotModel_PruneExpiredMerged(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	m := newSnapshotModel()
	m.upsert(snapshot.PRInput{ // expired: merged 25h ago, mine
		PR:        api.PR{Repo: "o/r", Number: 1, Author: "me", State: "merged", Merged: true, MergedAt: now.Add(-25 * time.Hour).Format(time.RFC3339)},
		Ownership: ownership.Mine,
	})
	m.upsert(snapshot.PRInput{ // still within window: merged 1h ago, mine
		PR:        api.PR{Repo: "o/r", Number: 2, Author: "me", State: "merged", Merged: true, MergedAt: now.Add(-1 * time.Hour).Format(time.RFC3339)},
		Ownership: ownership.Mine,
	})
	m.upsert(snapshot.PRInput{ // active, not merged at all
		PR:        api.PR{Repo: "o/r", Number: 3, Author: "me", State: "open"},
		Ownership: ownership.Mine,
	})
	m.upsert(snapshot.PRInput{ // merged, but NOT mine — pruner must not touch it either way
		PR:        api.PR{Repo: "o/r", Number: 4, Author: "teammate", State: "merged", Merged: true, MergedAt: now.Add(-48 * time.Hour).Format(time.RFC3339)},
		Ownership: ownership.Team,
	})

	m.pruneExpiredMerged(now)

	remaining := map[int]bool{}
	for _, in := range m.sortedInputs() {
		remaining[in.PR.Number] = true
	}
	if remaining[1] {
		t.Error("expired merged-mine PR #1 must be pruned from the model")
	}
	if !remaining[2] {
		t.Error("merged-mine PR #2 still within the retention window must remain")
	}
	if !remaining[3] {
		t.Error("active (non-merged) mine PR #3 must remain")
	}
	if !remaining[4] {
		t.Error("out-of-scope merged team PR #4 must remain (the pruner only acts on Ownership==Mine)")
	}
}
