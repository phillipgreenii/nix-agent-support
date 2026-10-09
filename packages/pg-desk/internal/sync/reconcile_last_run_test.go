package sync

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func intp(n int) *int { return &n }

// TestReconcileLastRun_RoundTrip: a written summary reads back with its age,
// and the open-set fields survive as nil when the open set was not read.
func TestReconcileLastRun_RoundTrip(t *testing.T) {
	st := store.OpenForTest(t)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	if _, _, found, err := ReadReconcileLastRun(st, now); err != nil || found {
		t.Fatalf("empty store: found=%v err=%v, want not found", found, err)
	}

	want := ReconcileLastRun{At: "2026-10-09T11:30:00Z", OpenIDs: intp(40), SkippedOpen: intp(38), Candidates: 2, Deferred: 1}
	if err := WriteReconcileLastRun(st, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, age, found, err := ReadReconcileLastRun(st, now)
	if err != nil || !found {
		t.Fatalf("read: found=%v err=%v", found, err)
	}
	if age != 1800 {
		t.Fatalf("age = %d, want 1800 (12:00 - 11:30)", age)
	}
	if got.OpenIDs == nil || *got.OpenIDs != 40 || got.SkippedOpen == nil || *got.SkippedOpen != 38 || got.Candidates != 2 || got.Deferred != 1 {
		t.Fatalf("read back %+v, want open_ids=40 skipped_open=38 candidates=2 deferred=1", got)
	}

	if err := WriteReconcileLastRun(st, ReconcileLastRun{At: "2026-10-09T12:00:05Z", Candidates: 3}); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, age, found, _ = ReadReconcileLastRun(st, now)
	if !found || got.OpenIDs != nil || got.SkippedOpen != nil || got.Candidates != 3 || age != 0 {
		t.Fatalf("no-open-set run: %+v age=%d found=%v, want nil open fields, candidates=3, age clamped to 0", got, age, found)
	}
}

// TestReconcileLastRun_UnparseableIsNotFound: a corrupt value or timestamp
// reports not found, never a zero summary.
func TestReconcileLastRun_UnparseableIsNotFound(t *testing.T) {
	st := store.OpenForTest(t)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for _, v := range []string{"not json", `{"at":"yesterday","candidates":1}`} {
		if err := st.SetMeta(ReconcileLastRunKey, v); err != nil {
			t.Fatal(err)
		}
		if _, _, found, err := ReadReconcileLastRun(st, now); err != nil || found {
			t.Fatalf("value %q: found=%v err=%v, want not found", v, found, err)
		}
	}
}
