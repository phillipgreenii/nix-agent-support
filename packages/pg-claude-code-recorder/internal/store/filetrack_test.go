package store

import (
	"database/sql"
	"errors"
	"slices"
	"testing"
)

func openTrackStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func sorted(ids []int64) []int64 {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

func trackedFile(t *testing.T, st *Store, id int64) {
	t.Helper()
	if err := st.Commit(&Rec{T: TFile, At: 1, File: id, Source: "transcript", Mode: "append"}); err != nil {
		t.Fatal(err)
	}
}

// Two trackers on one store each see every change, and draining one leaves the
// other's pending ids alone.
func TestTwoFileTrackersAreIndependent(t *testing.T) {
	st := openTrackStore(t)
	a, b := st.TrackFiles(), st.TrackFiles()
	if _, full := a.Take(); !full {
		t.Fatal("the first Take of a tracker must report full")
	}
	if _, full := b.Take(); !full {
		t.Fatal("the first Take of the second tracker must report full")
	}
	trackedFile(t, st, 11)
	trackedFile(t, st, 12)
	ids, full := a.Take()
	if full || !slices.Equal(sorted(ids), []int64{11, 12}) {
		t.Fatalf("tracker a: ids %v full %v, want [11 12]", ids, full)
	}
	if ids, _ := a.Take(); len(ids) != 0 {
		t.Fatalf("tracker a after a drain still holds %v", ids)
	}
	ids, full = b.Take() // b was not drained by a's Take
	if full || !slices.Equal(sorted(ids), []int64{11, 12}) {
		t.Fatalf("tracker b: ids %v full %v, want [11 12]", ids, full)
	}
}

// A record that names no file (File == 0) touches no tracker.
func TestFileTrackerSkipsRecordsWithoutAFile(t *testing.T) {
	st := openTrackStore(t)
	k := st.TrackFiles()
	k.Take()
	if err := st.Commit(&Rec{T: TStage, At: 2, Session: "s", State: StageHeld}); err != nil {
		t.Fatal(err)
	}
	if ids, full := k.Take(); full || len(ids) != 0 {
		t.Fatalf("a stage record reported ids %v full %v", ids, full)
	}
}

// A rolled back write may have undone what a tracker was told about, so the
// next Take of every tracker is full.
func TestFileTrackerReportsFullAfterARollback(t *testing.T) {
	st := openTrackStore(t)
	a, b := st.TrackFiles(), st.TrackFiles()
	a.Take()
	b.Take()
	trackedFile(t, st, 21)
	if err := st.WithWrite("", func(*sql.Tx) error { return errors.New("fail") }); err == nil {
		t.Fatal("the failing write returned nil")
	}
	for name, k := range map[string]*FileTracker{"a": a, "b": b} {
		if _, full := k.Take(); !full {
			t.Fatalf("tracker %s: Take after a rollback was not full", name)
		}
		if ids, full := k.Take(); full || len(ids) != 0 {
			t.Fatalf("tracker %s: second Take gave ids %v full %v", name, ids, full)
		}
	}
}

// A tracker that is not drained keeps every id named since its last Take
// without disturbing a drained one.
func TestFileTrackerNeverDrainedDoesNotAffectOthers(t *testing.T) {
	st := openTrackStore(t)
	idle, busy := st.TrackFiles(), st.TrackFiles()
	idle.Take()
	busy.Take()
	var want []int64
	for id := int64(31); id < 36; id++ {
		want = append(want, id)
		trackedFile(t, st, id)
		if ids, full := busy.Take(); full || !slices.Equal(ids, []int64{id}) {
			t.Fatalf("busy tracker: ids %v full %v, want [%d]", ids, full, id)
		}
	}
	ids, full := idle.Take()
	if full || !slices.Equal(sorted(ids), want) {
		t.Fatalf("idle tracker: ids %v full %v, want %v", ids, full, want)
	}
}
