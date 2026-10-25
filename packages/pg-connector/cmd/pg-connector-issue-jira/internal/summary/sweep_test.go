package summary

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

const expiry = 7 * 24 * time.Hour

func TestSweepEvictsAtSevenDaysAndTakesTheWatchedFlag(t *testing.T) {
	p, st, _ := newPersister(t)
	ctx := context.Background()
	persist(t, p, issue(key, "done", "one")) // closed_at = t0
	persist(t, p, issue("ABC-2", "indeterminate", "open"))
	for _, id := range []string{key, "ABC-2"} {
		if err := st.InTx(ctx, func(tx *store.Tx) error {
			_, err := tx.SetWatched(ctx, groups.EntityKind, id, "query", t0)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := NewAliases(st).Put(ctx, key, "abc-1"); err != nil {
		t.Fatal(err)
	}
	if err := NewAliases(st).Put(ctx, "ABC-2", "ABC-2"); err != nil {
		t.Fatal(err)
	}

	n, err := Sweep(ctx, st, expiry, t0.Add(expiry-time.Hour)) // 6 days 23 hours
	if err != nil || n != 0 {
		t.Fatalf("Sweep at 6d23h = %d, %v, want 0", n, err)
	}
	if e := entity(t, st, key); e.WatchedAt.IsZero() {
		t.Error("the watched flag went early")
	}

	n, err = Sweep(ctx, st, expiry, t0.Add(expiry))
	if err != nil || n != 1 {
		t.Fatalf("Sweep at 7d = %d, %v, want 1", n, err)
	}
	if _, ok, _ := st.GetEntity(ctx, groups.EntityKind, key); ok {
		t.Error("the entity survived")
	}
	if _, ok, _ := st.GetGroup(ctx, groups.EntityKind, key, groups.Summary); ok {
		t.Error("its summary group survived")
	}
	if _, ok, _ := NewAliases(st).Ref(ctx, key); ok {
		t.Error("its alias survived")
	}
	if c, err := st.CountWatched(ctx, groups.EntityKind); err != nil || c != 1 {
		t.Errorf("watched count = %d, %v, want 1 (the open issue)", c, err)
	}
	if _, ok, _ := st.GetEntity(ctx, groups.EntityKind, "ABC-2"); !ok {
		t.Error("an open issue was evicted")
	}
	if _, ok, _ := NewAliases(st).Ref(ctx, "ABC-2"); !ok {
		t.Error("an open issue lost its alias")
	}

	// the evicted issue is a first observation again
	if got := persist(t, p, issue(key, "done", "one")); !got.First {
		t.Error("an evicted issue was not a first observation")
	}
	if !entity(t, st, key).WatchedAt.IsZero() {
		t.Error("the watched flag came back with the row")
	}
}
