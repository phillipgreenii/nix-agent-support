package entity

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustRef(t *testing.T, s string) Ref {
	t.Helper()
	r, err := ParseRef(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestIdentity_PutAndLookup(t *testing.T) {
	ctx := context.Background()
	clk := governor.NewManualClock(t0)
	id := NewIdentity(openStore(t), clk)

	up, err := id.Put(ctx, "PR_1", mustRef(t, "Acme-Org/Widget#7"))
	if err != nil || up.Renamed {
		t.Fatalf("first Put = %+v, %v", up, err)
	}
	got, ok, err := id.Ref(ctx, "PR_1")
	if err != nil || !ok || got.String() != "Acme-Org/Widget#7" {
		t.Errorf("Ref = %v ok=%v err=%v", got, ok, err)
	}
	// A request in any case finds it, and gets GitHub's spelling back.
	for _, in := range []string{"acme-org/widget#7", "ACME-ORG/WIDGET#7", "Acme-Org/Widget#7"} {
		node, canonical, ok, err := id.NodeID(ctx, mustRef(t, in))
		if err != nil || !ok || node != "PR_1" || canonical.String() != "Acme-Org/Widget#7" {
			t.Errorf("NodeID(%q) = %q %v ok=%v err=%v", in, node, canonical, ok, err)
		}
	}
	if _, _, ok, _ := id.NodeID(ctx, mustRef(t, "acme-org/widget#8")); ok {
		t.Error("another number must not match")
	}
	if _, ok, _ := id.Ref(ctx, "PR_404"); ok {
		t.Error("unknown node id must not resolve")
	}
	// Putting the same reference again is not a rename.
	if up, err := id.Put(ctx, "PR_1", mustRef(t, "Acme-Org/Widget#7")); err != nil || up.Renamed {
		t.Errorf("repeat Put = %+v, %v", up, err)
	}
	if _, err := id.Put(ctx, "", mustRef(t, "a/b#1")); err == nil {
		t.Error("an empty node id must be refused")
	}
	if _, err := id.Put(ctx, "PR_2", Ref{}); err == nil {
		t.Error("an empty reference must be refused")
	}
}

func TestIdentity_RenameKeepsTheNodeIDAndUpdatesTheAlias(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	clk := governor.NewManualClock(t0)
	id := NewIdentity(st, clk)
	if _, err := id.Put(ctx, "PR_1", mustRef(t, "acme/widget#7")); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Hour)
	up, err := id.Put(ctx, "PR_1", mustRef(t, "acme/gadget#7"))
	if err != nil {
		t.Fatal(err)
	}
	if !up.Renamed || up.NodeID != "PR_1" || up.Old.String() != "acme/widget#7" || up.Ref.String() != "acme/gadget#7" {
		t.Errorf("Update = %+v", up)
	}
	row, ok, _ := st.AliasByNodeID(ctx, "PR_1")
	if !ok || row.Ref != "acme/gadget#7" || !row.SeenAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("alias row after rename = %+v", row)
	}
	if _, _, ok, _ := id.NodeID(ctx, mustRef(t, "acme/widget#7")); ok {
		t.Error("the old reference must no longer resolve")
	}
	if node, _, ok, _ := id.NodeID(ctx, mustRef(t, "ACME/GADGET#7")); !ok || node != "PR_1" {
		t.Errorf("the new reference resolves to %q ok=%v, want PR_1", node, ok)
	}
}
