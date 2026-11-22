package storetest

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

func TestOpenGivesAnIndependentStoreAtTheLatestVersion(t *testing.T) {
	a, b := Open(t), Open(t)
	if a.Version() != store.SchemaVersion {
		t.Fatalf("version = %d, want %d", a.Version(), store.SchemaVersion)
	}
	ctx := t.Context()
	if err := a.PutMeta(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := b.GetMeta(ctx, "k"); err != nil || ok {
		t.Fatalf("second store sees the first store's write: ok=%v err=%v", ok, err)
	}
	if _, err := a.Identity(ctx); err != nil {
		t.Fatalf("a copied store has no identity: %v", err)
	}
}
