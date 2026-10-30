package settle

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/invariants"
)

func snap(t *testing.T, dir string) *invariants.DirSnapshot {
	t.Helper()
	s, err := invariants.SnapshotDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
