package store

import (
	"context"
	"testing"
)

func seedPR(t *testing.T, db *DB) int64 {
	t.Helper()
	id, err := db.UpsertPR(context.Background(), PullRequest{
		Repo: "o/r", Number: 1, Ownership: "mine", State: "open", HeadSHA: "h1",
	})
	if err != nil {
		t.Fatalf("seed pr: %v", err)
	}
	return id
}

func TestRecordRevision_AppendsAndTouches(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)

	// First observation seeds seq=1.
	r, appended, err := db.RecordRevision(ctx, prID, "h1", "b1")
	if err != nil || !appended || r.Seq != 1 {
		t.Fatalf("seed: r=%+v appended=%v err=%v", r, appended, err)
	}

	// Identical pair -> touch, no append.
	r2, appended2, err := db.RecordRevision(ctx, prID, "h1", "b1")
	if err != nil || appended2 || r2.Seq != 1 {
		t.Fatalf("touch: r=%+v appended=%v err=%v", r2, appended2, err)
	}

	// Head change -> append seq=2.
	r3, appended3, _ := db.RecordRevision(ctx, prID, "h2", "b1")
	if !appended3 || r3.Seq != 2 {
		t.Fatalf("head change: r=%+v appended=%v", r3, appended3)
	}

	// Base change under same head -> append seq=3.
	r4, appended4, _ := db.RecordRevision(ctx, prID, "h2", "b2")
	if !appended4 || r4.Seq != 3 {
		t.Fatalf("base change: r=%+v appended=%v", r4, appended4)
	}

	// Force-push back to an earlier pair -> NEW row (re-introduction), seq=4.
	r5, appended5, _ := db.RecordRevision(ctx, prID, "h1", "b1")
	if !appended5 || r5.Seq != 4 {
		t.Fatalf("re-introduction: r=%+v appended=%v", r5, appended5)
	}
}

func TestRecordRevision_NullBaseFallsBackToHead(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)

	if _, appended, _ := db.RecordRevision(ctx, prID, "h1", ""); !appended {
		t.Fatal("first seed should append")
	}
	// Same head, still no base -> touch only (no spurious append).
	if _, appended, _ := db.RecordRevision(ctx, prID, "h1", ""); appended {
		t.Fatal("null base + same head must not append")
	}
}
