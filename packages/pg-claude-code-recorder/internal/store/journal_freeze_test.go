package store

import (
	"os"
	"path/filepath"
	"testing"
)

// A frozen journal decodes once for the delta and the replay, and both equal
// what JournalDelta and ReplayDump report for the same bytes (with a torn tail).
func TestFreezeJournalDecodesOnceAndMatchesPathReaders(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	defer func() { _ = s.Close() }()
	id := seed(t, s)
	if err := s.Commit(&Rec{T: TCommit, At: 20, File: id, Gen: 1, From: 10, To: 25, Size: 25, Tail: 78, TailLen: 15}); err != nil {
		t.Fatal(err)
	}
	jp := filepath.Join(dir, JournalName)
	jf, err := os.OpenFile(jp, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = jf.WriteString(`{"seq":99,"t":"commit","file":1`)
	_ = jf.Close()

	fz, err := s.FreezeJournal()
	if err != nil {
		t.Fatal(err)
	}
	before := journalDecodes.Load()
	delta := fz.Delta()
	replay, err := fz.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if n := journalDecodes.Load() - before; n != 1 {
		t.Fatalf("journal decoded %d times for one delta and one replay, want 1", n)
	}
	wantDelta, err := JournalDelta(jp)
	if err != nil || delta != wantDelta || delta.TornBytes == 0 {
		t.Fatalf("delta %+v, path reader %+v (%v)", delta, wantDelta, err)
	}
	wantReplay, err := ReplayDump(jp)
	if err != nil || replay != wantReplay || replay != fz.Live {
		t.Fatalf("replay differs from the path reader or the live dump: %v", err)
	}
}
