package sync

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func anchorRow(entity, beadID, hash, at string) store.LedgerEntry {
	return store.LedgerEntry{
		Repo: "o/r", EntityType: "pr", EntityID: entity, Kind: KindAnchor,
		BeadID: beadID, LastSyncedContentHash: hash, LastSyncedAt: at,
	}
}

// TestOldestAnchorCheckAgeOf covers bead pg2-u4c1s's staleness gauge: the
// oldest applied, non-closed anchor row wins; closed, planned (no bead),
// non-anchor, and unparseable rows are excluded; 0 when nothing qualifies.
func TestOldestAnchorCheckAgeOf(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		rows []store.LedgerEntry
		want int
	}{
		{"empty", nil, 0},
		{"oldest of several", []store.LedgerEntry{
			anchorRow("o/r#1", "b1", "h", "2026-10-05T11:00:00Z"), // 3600
			anchorRow("o/r#2", "b2", "h", "2026-10-05T09:00:00Z"), // 10800
			anchorRow("o/r#3", "b3", "h", "2026-10-05T11:59:00Z"), // 60
		}, 10800},
		{"closed excluded", []store.LedgerEntry{
			anchorRow("o/r#1", "b1", ClosedSentinel, "2026-10-01T00:00:00Z"),
			anchorRow("o/r#2", "b2", "h", "2026-10-05T11:00:00Z"),
		}, 3600},
		{"planned excluded", []store.LedgerEntry{
			anchorRow("o/r#1", "", "h", "2026-10-01T00:00:00Z"),
		}, 0},
		{"non-anchor kinds excluded", []store.LedgerEntry{
			{Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Kind: KindReviewRequest, BeadID: "b1", LastSyncedContentHash: "h", LastSyncedAt: "2026-10-01T00:00:00Z"},
			{Repo: "o/r", EntityType: "pr", EntityID: "o/r#1", Kind: KindFeedbackCycle, BeadID: "b2", LastSyncedContentHash: "h", LastSyncedAt: "2026-10-01T00:00:00Z"},
		}, 0},
		{"unparseable and future skipped", []store.LedgerEntry{
			anchorRow("o/r#1", "b1", "h", "not-a-time"),
			anchorRow("o/r#2", "b2", "h", ""),
			anchorRow("o/r#3", "b3", "h", "2026-10-06T00:00:00Z"),
		}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OldestAnchorCheckAgeOf(tc.rows, now); got != tc.want {
				t.Fatalf("OldestAnchorCheckAgeOf = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestOldestAnchorCheckAge_Store reads through a real store: an
// uninitialised-ledger-free empty store reports 0, and seeded rows are
// aged against now.
func TestOldestAnchorCheckAge_Store(t *testing.T) {
	st := store.OpenForTest(t)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	if got, err := OldestAnchorCheckAge(st, now); err != nil || got != 0 {
		t.Fatalf("empty store: got %d, err %v; want 0, nil", got, err)
	}
	for _, r := range []store.LedgerEntry{
		anchorRow("o/r#1", "b1", "h", "2026-10-05T10:00:00Z"),
		anchorRow("o/r#2", "b2", ClosedSentinel, "2026-10-01T00:00:00Z"),
		anchorRow("o/r#3", "", "h", "2026-10-01T00:00:00Z"),
	} {
		if err := st.UpsertLedger(r); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if got, err := OldestAnchorCheckAge(st, now); err != nil || got != 7200 {
		t.Fatalf("seeded store: got %d, err %v; want 7200, nil", got, err)
	}
}
