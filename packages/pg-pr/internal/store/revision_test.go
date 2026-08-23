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

func TestSetRevisionCIAndReads(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)
	r1, _, _ := db.RecordRevision(ctx, prID, "h1", "b1")
	_, _, _ = db.RecordRevision(ctx, prID, "h2", "b1")

	if err := db.SetRevisionCI(ctx, r1.ID, CIRollup{
		State: "failure", Passed: 3, Failed: 1, Pending: 0, CapturedAt: "t",
	}); err != nil {
		t.Fatalf("SetRevisionCI: %v", err)
	}

	revs, err := db.ListRevisions(ctx, prID)
	if err != nil || len(revs) != 2 {
		t.Fatalf("ListRevisions: n=%d err=%v", len(revs), err)
	}
	if revs[0].Seq != 1 || revs[1].Seq != 2 {
		t.Fatalf("not ascending: %d,%d", revs[0].Seq, revs[1].Seq)
	}
	if revs[0].CIState != "failure" || revs[0].CIFailed != 1 {
		t.Fatalf("CI not stored: %+v", revs[0])
	}

	latest, _ := db.LatestRevision(ctx, prID)
	if latest == nil || latest.Seq != 2 {
		t.Fatalf("LatestRevision: %+v", latest)
	}
}

func TestSetRevisionCI_DefaultsAndOverwrite(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)
	r1, _, err := db.RecordRevision(ctx, prID, "h1", "b1")
	if err != nil {
		t.Fatalf("RecordRevision: %v", err)
	}

	// Calling SetRevisionCI with an all-empty rollup stores ci_state=="none"
	// and ci_captured_at=="" (SQL NULL → "" after COALESCE), with zero counts.
	if err := db.SetRevisionCI(ctx, r1.ID, CIRollup{}); err != nil {
		t.Fatalf("SetRevisionCI empty: %v", err)
	}
	revs, err := db.ListRevisions(ctx, prID)
	if err != nil || len(revs) != 1 {
		t.Fatalf("ListRevisions after empty: n=%d err=%v", len(revs), err)
	}
	got := revs[0]
	if got.CIState != "none" {
		t.Errorf("CIState: got %q want \"none\"", got.CIState)
	}
	if got.CICapturedAt != "" {
		t.Errorf("CICapturedAt: got %q want \"\" (NULL)", got.CICapturedAt)
	}
	if got.CIPassed != 0 || got.CIFailed != 0 || got.CIPending != 0 {
		t.Errorf("counts: got passed=%d failed=%d pending=%d; want all 0",
			got.CIPassed, got.CIFailed, got.CIPending)
	}

	// A subsequent SetRevisionCI with a populated rollup OVERWRITES (idempotent).
	populated := CIRollup{State: "success", Passed: 5, Failed: 0, Pending: 0, CapturedAt: "2026-01-02T03:04:05Z"}
	if err := db.SetRevisionCI(ctx, r1.ID, populated); err != nil {
		t.Fatalf("SetRevisionCI populated: %v", err)
	}
	latest, err := db.LatestRevision(ctx, prID)
	if err != nil || latest == nil {
		t.Fatalf("LatestRevision: %+v err=%v", latest, err)
	}
	if latest.CIState != "success" {
		t.Errorf("overwrite CIState: got %q want \"success\"", latest.CIState)
	}
	if latest.CIPassed != 5 {
		t.Errorf("overwrite CIPassed: got %d want 5", latest.CIPassed)
	}
	if latest.CICapturedAt != "2026-01-02T03:04:05Z" {
		t.Errorf("overwrite CICapturedAt: got %q want \"2026-01-02T03:04:05Z\"", latest.CICapturedAt)
	}
}

// TestSetRevisionGateStateAndReads round-trips all four approval-gate states
// — including the (n,m) pair that ONLY partially-satisfied and unsatisfied
// carry (unsatisfied(0,m) in particular: n=0 is a MEANINGFUL reading for that
// state, not an absent one, so it must round-trip as 0, not NULL) — through a
// t.TempDir() store via OpenForTest/SetRevisionGateState/ListRevisions.
func TestSetRevisionGateStateAndReads(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)
	r1, _, err := db.RecordRevision(ctx, prID, "h1", "b1")
	if err != nil {
		t.Fatalf("RecordRevision: %v", err)
	}

	// A freshly-appended revision defaults to "unknown" with no (n,m) pair,
	// mirroring the DB-level DEFAULT (see TestMigrate_V11GateStateDefaults).
	if r1.GateState != "unknown" || r1.GateStateN != 0 || r1.GateStateM != 0 {
		t.Fatalf("freshly-recorded revision gate state = %+v, want unknown/0/0", r1)
	}

	cases := []struct {
		name string
		in   GateState
		want GateState
	}{
		{
			name: "satisfied",
			in:   GateState{State: "satisfied", CapturedAt: "t1"},
			want: GateState{State: "satisfied", N: 0, M: 0, CapturedAt: "t1"},
		},
		{
			name: "partially-satisfied",
			in:   GateState{State: "partially-satisfied", N: 2, M: 5, CapturedAt: "t2"},
			want: GateState{State: "partially-satisfied", N: 2, M: 5, CapturedAt: "t2"},
		},
		{
			// unsatisfied(0,m): n=0 is a real, meaningful count and must
			// round-trip as 0 (never NULL/absent).
			name: "unsatisfied",
			in:   GateState{State: "unsatisfied", N: 0, M: 3, CapturedAt: "t3"},
			want: GateState{State: "unsatisfied", N: 0, M: 3, CapturedAt: "t3"},
		},
		{
			name: "unknown",
			in:   GateState{State: "unknown", CapturedAt: "t4"},
			want: GateState{State: "unknown", N: 0, M: 0, CapturedAt: "t4"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.SetRevisionGateState(ctx, r1.ID, tc.in); err != nil {
				t.Fatalf("SetRevisionGateState(%+v): %v", tc.in, err)
			}
			latest, err := db.LatestRevision(ctx, prID)
			if err != nil || latest == nil {
				t.Fatalf("LatestRevision: %+v err=%v", latest, err)
			}
			got := GateState{
				State: latest.GateState, N: latest.GateStateN, M: latest.GateStateM,
				CapturedAt: latest.GateStateCapturedAt,
			}
			if got != tc.want {
				t.Fatalf("round trip %s: got %+v, want %+v", tc.name, got, tc.want)
			}
		})
	}

	// satisfied/unknown ignore a passed (n,m) pair and persist NULL (read
	// back as 0), even when the caller mistakenly supplies non-zero values —
	// those two states never carry the pair.
	if err := db.SetRevisionGateState(ctx, r1.ID, GateState{State: "satisfied", N: 9, M: 9}); err != nil {
		t.Fatalf("SetRevisionGateState(satisfied with n/m): %v", err)
	}
	latest, err := db.LatestRevision(ctx, prID)
	if err != nil || latest == nil {
		t.Fatalf("LatestRevision: %+v err=%v", latest, err)
	}
	if latest.GateStateN != 0 || latest.GateStateM != 0 {
		t.Errorf("satisfied with n/m supplied: got n=%d m=%d, want both 0 (NULL ignored)", latest.GateStateN, latest.GateStateM)
	}

	// Empty State defaults to "unknown", mirroring CIRollup's "" -> "none".
	if err := db.SetRevisionGateState(ctx, r1.ID, GateState{}); err != nil {
		t.Fatalf("SetRevisionGateState(empty): %v", err)
	}
	latest, err = db.LatestRevision(ctx, prID)
	if err != nil || latest == nil {
		t.Fatalf("LatestRevision: %+v err=%v", latest, err)
	}
	if latest.GateState != "unknown" {
		t.Errorf("empty GateState.State: got %q, want \"unknown\"", latest.GateState)
	}
}

func TestRevision_FKCascadeOnPRDelete(t *testing.T) {
	ctx := context.Background()
	db := OpenForTest(t)
	prID := seedPR(t, db)

	if _, _, err := db.RecordRevision(ctx, prID, "h1", "b1"); err != nil {
		t.Fatalf("RecordRevision: %v", err)
	}
	if _, _, err := db.RecordRevision(ctx, prID, "h2", "b1"); err != nil {
		t.Fatalf("RecordRevision seq2: %v", err)
	}

	// Confirm revisions exist before deletion.
	before, err := db.ListRevisions(ctx, prID)
	if err != nil || len(before) != 2 {
		t.Fatalf("pre-delete ListRevisions: n=%d err=%v", len(before), err)
	}

	// Delete the parent pull_request row; FK cascade should remove revisions.
	if _, err := db.sql.Exec("DELETE FROM pull_request WHERE id=?", prID); err != nil {
		t.Fatalf("DELETE pull_request: %v", err)
	}

	after, err := db.ListRevisions(ctx, prID)
	if err != nil {
		t.Fatalf("post-delete ListRevisions: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("FK cascade: expected 0 revisions after PR delete, got %d", len(after))
	}
}
