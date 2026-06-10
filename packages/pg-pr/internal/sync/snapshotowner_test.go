package sync

import (
	"testing"

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
