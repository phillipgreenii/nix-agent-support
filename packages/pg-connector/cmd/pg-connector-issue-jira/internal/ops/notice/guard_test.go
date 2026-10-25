package notice

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

func row(id string, gen int64, content string) store.GroupRow {
	return store.GroupRow{Kind: groups.EntityKind, ID: id, Group: groups.Summary, Content: []byte(content), Generation: gen}
}

func TestGuardStoresNothingWhileHeld(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if ok, err := r.guard.PutGroupIf(ctx, row(keyA, 1, "first"), 0); err != nil || !ok {
		t.Fatalf("unheld PutGroupIf = %v, %v; want stored", ok, err)
	}
	r.table.Begin(ctx, keyA, 0)

	ok, err := r.guard.PutGroupIf(ctx, row(keyA, 2, "second"), 1)
	if err != nil || ok {
		t.Fatalf("held PutGroupIf = %v, %v; want not stored", ok, err)
	}
	called := false
	ok, err = r.guard.PutGroupIfThen(ctx, row(keyA, 2, "second"), 1, func(*store.Tx) error { called = true; return nil })
	if err != nil || ok || called {
		t.Fatalf("held PutGroupIfThen = %v, %v, then called %v; want not stored and no callback", ok, err, called)
	}
	if g, _ := r.group(keyA, groups.Summary); string(g.Content) != "first" || g.Generation != 1 {
		t.Fatalf("the held entity's row changed: %q gen %d", g.Content, g.Generation)
	}

	// Another entity is not held.
	if ok, err := r.guard.PutGroupIf(ctx, row("B-2", 1, "other"), 0); err != nil || !ok {
		t.Fatalf("PutGroupIf of another entity = %v, %v", ok, err)
	}

	// After the release the same commit stores.
	r.table.Release(ctx, keyA)
	called = false
	ok, err = r.guard.PutGroupIfThen(ctx, row(keyA, 2, "second"), 1, func(*store.Tx) error { called = true; return nil })
	if err != nil || !ok || !called {
		t.Fatalf("released PutGroupIfThen = %v, %v, then called %v; want stored with callback", ok, err, called)
	}
	if g, _ := r.group(keyA, groups.Summary); string(g.Content) != "second" {
		t.Fatalf("content = %q", g.Content)
	}
}

func TestGuardKeepsTheStoreCompareAndSwapWhenNotHeld(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if ok, _ := r.guard.PutGroupIf(ctx, row(keyA, 1, "first"), 0); !ok {
		t.Fatal("first put")
	}
	if ok, err := r.guard.PutGroupIf(ctx, row(keyA, 6, "stale"), 5); err != nil || ok {
		t.Fatalf("a wrong expected generation stored: %v, %v", ok, err)
	}
	if ok, err := r.guard.PutGroupIfThen(ctx, row(keyA, 6, "stale"), 5, nil); err != nil || ok {
		t.Fatalf("a wrong expected generation stored through PutGroupIfThen: %v, %v", ok, err)
	}
}
