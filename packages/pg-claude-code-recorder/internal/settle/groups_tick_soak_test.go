//go:build soak

// Scale loop (300 files) moved out of the unit tier.
// Run by pg-claude-code-recorder-soak (tag soak, tests named TestSoak*), never in the unit gate.

package settle

import (
	"context"
	"fmt"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

func TestSoakGroupsTickReadsOnlyWhatChanged(t *testing.T) {
	e := newGrpEnv(t)
	for i := 0; i < 300; i++ {
		e.add("append", fmt.Sprintf("/c/projects/p/%08d-1111-4111-8111-111111111111.jsonl", i))
	}
	e.same("populated")
	e.cnt.Reset()
	if _, err := e.s.groups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r, q := e.cnt.RowsRead(), e.cnt.Queries(); r != 0 || q != 0 {
		t.Fatalf("idle groups read %d rows in %d queries, want none", r, q)
	}
	id := e.add("append", "/c/projects/p/"+gSessA+".jsonl")
	e.cnt.Reset()
	if _, err := e.s.groups(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := e.cnt.RowsRead(); r > 4 {
		t.Fatalf("one new file read %d rows, want a handful", r)
	}
	e.commit(&store.Rec{T: store.TCommit, File: id, Gen: 1, From: 0, To: 9, Size: 9, Tail: 1, TailLen: 1})
	e.same("after commit")
}
