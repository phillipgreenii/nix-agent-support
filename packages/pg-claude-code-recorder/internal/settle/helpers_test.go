package settle

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/invariants"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit/storetpl"
)

func snap(t *testing.T, dir string) *invariants.DirSnapshot {
	t.Helper()
	s, err := invariants.SnapshotDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// openStore is store.Open on a directory that already holds a store with its
// schema built (restored from a template built once per test process), so the
// test pays the open of an existing store, not the creation of a new one. The
// schema-creation path itself is tested in internal/store and
// internal/testkit/storetpl.
func openStore(t testing.TB, dir string, opt store.Options) (*store.Store, error) {
	t.Helper()
	storetpl.Prepare(t, dir, opt)
	return store.Open(dir, opt)
}
