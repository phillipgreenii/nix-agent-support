package store

import (
	"errors"
	"path/filepath"
	"testing"
)

// Tests of the persisted last-run counts and the failed count behind
// `pg-desk status` (docs/behavior/pg-desk/reinterpret.md, "Observability";
// INV-REINTERPRET-11 and -13), against a real store.

func TestReinterpretLastRun_RoundTripsOnePass(t *testing.T) {
	s := OpenForTest(t)
	if _, found, err := s.LastReinterpretRun(); err != nil || found {
		t.Fatalf("empty store: found=%v err=%v", found, err)
	}
	want := ReinterpretRun{At: "2026-11-10T08:00:00Z", Source: "heartbeat", Selected: 7, Changed: 1, Unchanged: 2, StampedOnly: 3, Skipped: 4, Failed: 5, Remaining: 6}
	wrote, err := s.RecordReinterpretRun(want)
	if err != nil || !wrote {
		t.Fatalf("record: wrote=%v err=%v", wrote, err)
	}
	got, found, err := s.LastReinterpretRun()
	if err != nil || !found || got != want {
		t.Fatalf("got %+v found=%v err=%v, want %+v", got, found, err, want)
	}
	// A later pass replaces the earlier one whole.
	next := ReinterpretRun{At: "2026-11-10T08:01:00Z", Source: "pr reinterpret --open", Selected: 1, Changed: 1}
	if _, err := s.RecordReinterpretRun(next); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.LastReinterpretRun(); got != next {
		t.Fatalf("got %+v, want %+v", got, next)
	}
}

// An idle pass (nothing selected) writes no meta row and leaves the last one.
func TestReinterpretLastRun_IdlePassWritesNothing(t *testing.T) {
	s := OpenForTest(t)
	wrote, err := s.RecordReinterpretRun(ReinterpretRun{At: "x", Source: "heartbeat"})
	if err != nil || wrote {
		t.Fatalf("idle: wrote=%v err=%v", wrote, err)
	}
	if _, found, _ := s.GetMeta(MetaKeyReinterpretLastRun); found {
		t.Fatal("an idle pass wrote a meta row")
	}
	keep := ReinterpretRun{At: "t", Source: "heartbeat", Selected: 2}
	if _, err := s.RecordReinterpretRun(keep); err != nil {
		t.Fatal(err)
	}
	if wrote, _ := s.RecordReinterpretRun(ReinterpretRun{At: "later", Selected: 0}); wrote {
		t.Fatal("idle pass wrote")
	}
	if got, _, _ := s.LastReinterpretRun(); got != keep {
		t.Fatalf("idle pass changed the record: %+v", got)
	}
}

func TestReinterpretLastRun_CorruptValueIsAnError(t *testing.T) {
	s := OpenForTest(t)
	if err := s.SetMeta(MetaKeyReinterpretLastRun, "not json"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LastReinterpretRun(); err == nil {
		t.Fatal("want a decode error")
	}
}

func TestReinterpretLastRun_ClosedStoreErrors(t *testing.T) {
	s := OpenForTest(t)
	_ = s.Close()
	if _, err := s.RecordReinterpretRun(ReinterpretRun{Selected: 1}); err == nil {
		t.Error("record on a closed store: want an error")
	}
	if _, _, err := s.LastReinterpretRun(); err == nil {
		t.Error("read on a closed store: want an error")
	}
	s.SetInterpStamp("s")
	if _, _, err := s.InterpretationsFailed("o/r"); err == nil {
		t.Error("failed count on a closed store: want an error")
	}
}

func TestInterpretationsFailed_CountsOnlyRowsFailedUnderTheStamp(t *testing.T) {
	s := OpenForTest(t)
	s.SetInterpStamp("s1")
	if _, _, err := (&Store{}).InterpretationsFailed("o/r"); !errors.Is(err, ErrNoInterpStamp) {
		t.Fatalf("no stamp: err = %v", err)
	}
	for _, id := range []string{"1", "2", "3"} {
		if err := s.UpsertInterpretation(stampRow("o/r#" + id)); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"1", "2"} {
		if err := s.MarkInterpretationFailed(InterpretationKey{Repo: "o/r", EntityType: "pr", EntityID: "o/r#" + id}, "s1"); err != nil {
			t.Fatal(err)
		}
	}
	// Failed under another stamp does not count.
	if err := s.MarkInterpretationFailed(InterpretationKey{Repo: "o/r", EntityType: "pr", EntityID: "o/r#3"}, "s0"); err != nil {
		t.Fatal(err)
	}
	n, known, err := s.InterpretationsFailed("o/r")
	if err != nil || !known || n != 2 {
		t.Fatalf("n=%d known=%v err=%v, want 2", n, known, err)
	}
	if n, _, _ := s.InterpretationsFailed("other/repo"); n != 0 {
		t.Fatalf("other repo n=%d", n)
	}
}

func TestInterpretationsFailed_UnknownNeverZero(t *testing.T) {
	s1, err := Open(filepath.Join(t.TempDir(), "v1.db")) // schema 1
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s1.Close() })
	s1.SetInterpStamp("s1")
	if n, known, err := s1.InterpretationsFailed("o/r"); err != nil || known || n != 0 {
		t.Fatalf("schema 1: n=%d known=%v err=%v", n, known, err)
	}
	s := OpenForTest(t)
	s.SetInterpStamp("s1")
	dropStampColumn(t, s)
	if _, known, err := s.InterpretationsFailed("o/r"); err != nil || known {
		t.Fatalf("no column: known=%v err=%v", known, err)
	}
}
