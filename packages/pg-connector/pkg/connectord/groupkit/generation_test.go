package groupkit

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func detailRow(content string) store.GroupRow {
	return store.GroupRow{
		Content: []byte(content), Fingerprint: Fingerprint([]byte(content)),
		FetchedAt: t0, FreshUntil: t0.Add(10 * time.Minute), MaxAgeAt: t0.Add(30 * time.Minute),
	}
}

func contentOf(t *testing.T, s *store.Store, group string) (store.GroupRow, bool) {
	t.Helper()
	g, ok, err := s.GetGroup(context.Background(), testKind, "example-1", group)
	if err != nil {
		t.Fatal(err)
	}
	return g, ok
}

func TestBeginCommitStoresAndBumps(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	tok, err := Begin(ctx, s, testKind, "example-1", Detail)
	if err != nil || tok.Generation != 0 {
		t.Fatalf("Begin = %+v, %v", tok, err)
	}
	if ok, err := Commit(ctx, s, tok, detailRow("one")); err != nil || !ok {
		t.Fatalf("Commit = %v, %v", ok, err)
	}
	g, _ := contentOf(t, s, Detail)
	if string(g.Content) != "one" || g.Generation != 1 || g.Kind != testKind || g.ID != "example-1" || g.Group != Detail {
		t.Fatalf("row = %+v", g)
	}
	tok, _ = Begin(ctx, s, testKind, "example-1", Detail)
	if tok.Generation != 1 {
		t.Fatalf("second Begin generation = %d, want 1", tok.Generation)
	}
	if ok, _ := Commit(ctx, s, tok, detailRow("two")); !ok {
		t.Fatal("second Commit not stored")
	}
	if g, _ = contentOf(t, s, Detail); string(g.Content) != "two" || g.Generation != 2 {
		t.Fatalf("row = %+v", g)
	}
}

func TestInvalidateSupersedesEarlierBegin(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	tok, _ := Begin(ctx, s, testKind, "example-1", Detail)
	if ok, _ := Commit(ctx, s, tok, detailRow("stored")); !ok {
		t.Fatal("setup commit failed")
	}
	early, _ := Begin(ctx, s, testKind, "example-1", Detail)
	if err := Invalidate(ctx, s, testKind, "example-1", Detail, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	ok, err := Commit(ctx, s, early, detailRow("from before the write"))
	if err != nil || ok {
		t.Fatalf("superseded Commit = %v, %v; want not stored", ok, err)
	}
	g, _ := contentOf(t, s, Detail)
	if string(g.Content) != "stored" || g.Generation != 2 || !g.FreshUntil.Equal(t0.Add(time.Minute)) {
		t.Fatalf("row = %+v; Invalidate must keep content, set fresh_until to now, and the loser must write nothing", g)
	}
	// A Begin taken after the invalidation commits.
	late, _ := Begin(ctx, s, testKind, "example-1", Detail)
	if ok, _ := Commit(ctx, s, late, detailRow("fresh")); !ok {
		t.Fatal("Commit after Invalidate did not store")
	}
}

func TestTwoBeginsOnlyFirstCommitStores(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	a, _ := Begin(ctx, s, testKind, "example-1", Conversation)
	b, _ := Begin(ctx, s, testKind, "example-1", Conversation)
	if ok, _ := Commit(ctx, s, a, detailRow("a")); !ok {
		t.Fatal("first Commit not stored")
	}
	if ok, _ := Commit(ctx, s, b, detailRow("b")); ok {
		t.Fatal("second Commit stored")
	}
	if g, _ := contentOf(t, s, Conversation); string(g.Content) != "a" {
		t.Fatalf("row = %+v", g)
	}
}

func TestInvalidateOfNeverFetchedGroupCreatesEmptyRow(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	early, _ := Begin(ctx, s, testKind, "example-1", Detail)
	if err := Invalidate(ctx, s, testKind, "example-1", Detail, t0); err != nil {
		t.Fatal(err)
	}
	g, ok := contentOf(t, s, Detail)
	if !ok || len(g.Content) != 0 || g.Generation != 1 || !g.FreshUntil.Equal(t0) {
		t.Fatalf("row = %+v found=%v; want empty at generation 1", g, ok)
	}
	if got := testTable().Fresh(g, Stamps{}, t0.Add(-time.Hour)); got.Level != LevelStale {
		t.Errorf("empty row Fresh = %v, want stale", got)
	}
	if ok, _ := Commit(ctx, s, early, detailRow("lost")); ok {
		t.Fatal("a Begin taken before the invalidation committed")
	}
	if g, _ = contentOf(t, s, Detail); len(g.Content) != 0 {
		t.Fatalf("content written by the loser: %+v", g)
	}
}

func TestInvalidateOfMissingSummaryStaysMissing(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	if err := Invalidate(ctx, s, testKind, "example-1", Summary, t0); err != nil {
		t.Fatal(err)
	}
	if _, ok := contentOf(t, s, Summary); ok {
		t.Fatal("Invalidate created a summary row")
	}
}

func TestEightConcurrentCommittersOneWins(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	const n = 8
	toks := make([]Token, n)
	for i := range toks {
		toks[i], _ = Begin(ctx, s, testKind, "example-1", Files)
	}
	var wins atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := Commit(ctx, s, toks[i], detailRow("c"))
			if err != nil {
				t.Error(err)
			}
			if ok {
				wins.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d committers won, want 1", wins.Load())
	}
	if g, _ := contentOf(t, s, Files); g.Generation != 1 {
		t.Fatalf("generation = %d, want 1", g.Generation)
	}
}
