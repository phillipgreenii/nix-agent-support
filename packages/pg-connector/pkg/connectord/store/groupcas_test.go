package store

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func casGroup(content string, gen int64) GroupRow {
	return GroupRow{
		Kind: "pr", ID: "PR_1", Group: "detail", Content: []byte(content), Fingerprint: "fp-" + content,
		FetchedAt: time.UnixMilli(1760000000000).UTC(), FreshUntil: time.UnixMilli(1760000600000).UTC(),
		Generation: gen, FetchedForHead: "head", FetchedForBase: "base",
	}
}

func TestPutGroupIfInsertsWhenAbsentAtZero(t *testing.T) {
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	ctx := context.Background()
	ok, err := s.PutGroupIf(ctx, casGroup("a", 1), 0)
	if err != nil || !ok {
		t.Fatalf("PutGroupIf = %v, %v; want stored", ok, err)
	}
	g, found, _ := s.GetGroup(ctx, "pr", "PR_1", "detail")
	if !found || string(g.Content) != "a" || g.Generation != 1 || g.FetchedForHead != "head" {
		t.Fatalf("stored row = %+v", g)
	}
}

func TestPutGroupIfWrongGenerationWritesNothing(t *testing.T) {
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	ctx := context.Background()
	if err := s.PutGroup(ctx, casGroup("old", 3)); err != nil {
		t.Fatal(err)
	}
	for _, expect := range []int64{0, 2, 4} {
		ok, err := s.PutGroupIf(ctx, casGroup("new", expect+1), expect)
		if err != nil || ok {
			t.Fatalf("expect %d: PutGroupIf = %v, %v; want not stored", expect, ok, err)
		}
	}
	g, _, _ := s.GetGroup(ctx, "pr", "PR_1", "detail")
	if string(g.Content) != "old" || g.Generation != 3 {
		t.Fatalf("row changed: %+v", g)
	}
	// A row absent with a non-zero expectation stores nothing either.
	other := casGroup("x", 6)
	other.ID = "PR_2"
	if ok, _ := s.PutGroupIf(ctx, other, 5); ok {
		t.Fatal("stored a row that did not exist with expectGeneration 5")
	}
	if _, found, _ := s.GetGroup(ctx, "pr", "PR_2", "detail"); found {
		t.Fatal("a row appeared")
	}
	// The matching generation stores.
	if ok, err := s.PutGroupIf(ctx, casGroup("new", 4), 3); err != nil || !ok {
		t.Fatalf("matching PutGroupIf = %v, %v", ok, err)
	}
	g, _, _ = s.GetGroup(ctx, "pr", "PR_1", "detail")
	if string(g.Content) != "new" || g.Generation != 4 {
		t.Fatalf("row = %+v", g)
	}
}

func TestPutGroupIfZeroMatchesStoredGenerationZero(t *testing.T) {
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	ctx := context.Background()
	if err := s.PutGroup(ctx, casGroup("old", 0)); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.PutGroupIf(ctx, casGroup("new", 1), 0); err != nil || !ok {
		t.Fatalf("PutGroupIf = %v, %v", ok, err)
	}
}

func TestBumpGroupKeepsContentAndSetsFreshUntil(t *testing.T) {
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	ctx := context.Background()
	if err := s.PutGroup(ctx, casGroup("keep", 2)); err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(1760000100000).UTC()
	gen, err := s.BumpGroup(ctx, "pr", "PR_1", "detail", now)
	if err != nil || gen != 3 {
		t.Fatalf("BumpGroup = %d, %v; want 3", gen, err)
	}
	g, _, _ := s.GetGroup(ctx, "pr", "PR_1", "detail")
	if string(g.Content) != "keep" || g.Generation != 3 || !g.FreshUntil.Equal(now) || g.Fingerprint != "fp-keep" {
		t.Fatalf("row = %+v", g)
	}
}

func TestBumpGroupCreatesEmptyRowExceptSummary(t *testing.T) {
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	ctx := context.Background()
	now := time.UnixMilli(1760000100000).UTC()
	gen, err := s.BumpGroup(ctx, "pr", "PR_1", "detail", now)
	if err != nil || gen != 1 {
		t.Fatalf("BumpGroup(detail) = %d, %v; want 1", gen, err)
	}
	g, found, _ := s.GetGroup(ctx, "pr", "PR_1", "detail")
	if !found || len(g.Content) != 0 || g.Generation != 1 || !g.FreshUntil.Equal(now) || !g.FetchedAt.IsZero() {
		t.Fatalf("empty row = %+v found=%v", g, found)
	}
	gen, err = s.BumpGroup(ctx, "pr", "PR_1", "summary", now)
	if err != nil || gen != 0 {
		t.Fatalf("BumpGroup(missing summary) = %d, %v; want 0", gen, err)
	}
	if _, found, _ := s.GetGroup(ctx, "pr", "PR_1", "summary"); found {
		t.Fatal("a bump created a summary row")
	}
	// An existing summary does bump.
	sum := casGroup("s", 4)
	sum.Group = "summary"
	if err := s.PutGroup(ctx, sum); err != nil {
		t.Fatal(err)
	}
	if gen, err := s.BumpGroup(ctx, "pr", "PR_1", "summary", now); err != nil || gen != 5 {
		t.Fatalf("BumpGroup(summary) = %d, %v; want 5", gen, err)
	}
}

func TestPutGroupIfOneWinnerUnderConcurrency(t *testing.T) {
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	ctx := context.Background()
	if err := s.PutGroup(ctx, casGroup("base", 1)); err != nil {
		t.Fatal(err)
	}
	const n = 8
	var wins atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := s.PutGroupIf(ctx, casGroup("w", 2), 1)
			if err != nil {
				t.Error(err)
				return
			}
			if ok {
				wins.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d committers won; want exactly 1", wins.Load())
	}
}
