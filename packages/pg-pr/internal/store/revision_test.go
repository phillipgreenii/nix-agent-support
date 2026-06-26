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
	r3, appended3, err := db.RecordRevision(ctx, prID, "h2", "b1")
	if err != nil {
		t.Fatalf("head change: %v", err)
	}
	if !appended3 || r3.Seq != 2 {
		t.Fatalf("head change: r=%+v appended=%v", r3, appended3)
	}

	// Base change under same head -> append seq=3.
	r4, appended4, err := db.RecordRevision(ctx, prID, "h2", "b2")
	if err != nil {
		t.Fatalf("base change: %v", err)
	}
	if !appended4 || r4.Seq != 3 {
		t.Fatalf("base change: r=%+v appended=%v", r4, appended4)
	}

	// Force-push back to an earlier pair -> NEW row (re-introduction), seq=4.
	r5, appended5, err := db.RecordRevision(ctx, prID, "h1", "b1")
	if err != nil {
		t.Fatalf("re-introduction: %v", err)
	}
	if !appended5 || r5.Seq != 4 {
		t.Fatalf("re-introduction: r=%+v appended=%v", r5, appended5)
	}
}

func TestRecordRevision_NullBaseFallsBackToHead(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)

	if _, appended, err := db.RecordRevision(ctx, prID, "h1", ""); err != nil {
		t.Fatalf("seed: %v", err)
	} else if !appended {
		t.Fatal("first seed should append")
	}
	// Same head, still no base -> touch only (no spurious append).
	if _, appended, err := db.RecordRevision(ctx, prID, "h1", ""); err != nil {
		t.Fatalf("same head same empty base: %v", err)
	} else if appended {
		t.Fatal("null base + same head must not append")
	}

	// Asymmetric: stored base empty, new base set -> touch (head unchanged).
	t.Run("stored_empty_new_set", func(t *testing.T) {
		prID2 := seedPR(t, db)
		if _, _, err := db.RecordRevision(ctx, prID2, "h1", ""); err != nil {
			t.Fatalf("seed empty base: %v", err)
		}
		_, appended, err := db.RecordRevision(ctx, prID2, "h1", "b1")
		if err != nil {
			t.Fatalf("stored empty, new base set: %v", err)
		}
		if appended {
			t.Fatal("stored empty base + new base set + same head must TOUCH, not append")
		}
	})

	// Asymmetric: stored base set, new base empty -> touch (head unchanged).
	t.Run("stored_set_new_empty", func(t *testing.T) {
		prID3 := seedPR(t, db)
		if _, _, err := db.RecordRevision(ctx, prID3, "h1", "b1"); err != nil {
			t.Fatalf("seed with base: %v", err)
		}
		_, appended, err := db.RecordRevision(ctx, prID3, "h1", "")
		if err != nil {
			t.Fatalf("stored base set, new base empty: %v", err)
		}
		if appended {
			t.Fatal("stored base set + new base empty + same head must TOUCH, not append")
		}
	})
}
