package store

import (
	"context"
	"testing"
)

// perFileLastSeenNS is the oracle: the newest of a file's last copy commit,
// snapshot versions and tombstones, read one statement family per file. Zero
// means the file has none of them, which LastSeenByFile reports by omission.
func perFileLastSeenNS(t *testing.T, s *Store, id int64) int64 {
	t.Helper()
	var ns int64
	if err := s.Read().QueryRowContext(context.Background(), `SELECT last_copy_ns FROM files WHERE id=?`, id).Scan(&ns); err != nil {
		ns = 0
	}
	sn, err := s.Snapshots(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range sn {
		ns = max(ns, x.SeenNS)
	}
	ts, err := s.Tombstones(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range ts {
		ns = max(ns, x)
	}
	return ns
}

// LastSeenByFile answers for many files in one aggregate statement; it must
// give each file the same time a per-file computation does, across the shapes
// that differ: copy commits only, a snapshot, a tombstone newer than the rest,
// a snapshot newer than the copy, and no history at all.
func TestLastSeenByFileEqualsPerFileComputation(t *testing.T) {
	s := open(t, t.TempDir())
	const n = 24
	ids := make([]int64, 0, n+1)
	for i := 0; i < n; i++ {
		id := s.NewFileID()
		ids = append(ids, id)
		recs := []*Rec{{T: TFile, At: 10, File: id, Source: "claude-transcript", Mode: "append", Dev: 1, Ino: uint64(i + 1), Birth: 3}}
		switch i % 6 {
		case 0: // copy commits only
			recs = append(recs,
				&Rec{T: TGen, At: 11, File: id, Gen: 1, Method: "bytes", Reason: "first"},
				&Rec{T: TCommit, At: int64(100 + i), File: id, Gen: 1, From: 0, To: 5, Size: 5, Tail: 1, TailLen: 1})
		case 1: // snapshot only
			recs = append(recs, &Rec{T: TSnap, At: int64(1000 + i), File: id, Version: 1, Hash: 7, Size: 5})
		case 2: // tombstone newer than the copy
			recs = append(recs,
				&Rec{T: TGen, At: 11, File: id, Gen: 1, Method: "bytes", Reason: "first"},
				&Rec{T: TCommit, At: int64(200 + i), File: id, Gen: 1, From: 0, To: 5, Size: 5, Tail: 1, TailLen: 1},
				&Rec{T: TTomb, At: int64(5000 + i), File: id, Path: "/c/x"})
		case 3: // snapshot newer than the copy, tombstone older than both
			recs = append(recs,
				&Rec{T: TGen, At: 11, File: id, Gen: 1, Method: "bytes", Reason: "first"},
				&Rec{T: TCommit, At: int64(300 + i), File: id, Gen: 1, From: 0, To: 5, Size: 5, Tail: 1, TailLen: 1},
				&Rec{T: TSnap, At: int64(7000 + i), File: id, Version: 1, Hash: 7, Size: 5},
				&Rec{T: TTomb, At: int64(50 + i), File: id, Path: "/c/x"})
		case 4: // two snapshot versions
			recs = append(recs,
				&Rec{T: TSnap, At: int64(2000 + i), File: id, Version: 1, Hash: 7, Size: 5},
				&Rec{T: TSnap, At: int64(2500 + i), File: id, Version: 2, Hash: 8, Size: 6})
		case 5: // no history
		}
		if err := s.Commit(recs...); err != nil {
			t.Fatal(err)
		}
	}
	ids = append(ids, s.NewFileID()) // an id with no row at all

	var got map[int64]int64
	if err := s.ReadTx(func(rd *Reader) (e error) {
		got, e = rd.LastSeenByFile(ids)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	distinct := map[int64]bool{}
	for _, id := range ids {
		want := perFileLastSeenNS(t, s, id)
		distinct[want] = true
		g, ok := got[id]
		if want == 0 {
			if ok {
				t.Errorf("file %d has no history but LastSeenByFile returned %d", id, g)
			}
			continue
		}
		if !ok || g != want {
			t.Errorf("file %d: bulk %d (present %v), per file %d", id, g, ok, want)
		}
	}
	if len(distinct) < 10 {
		t.Fatalf("fixture too uniform: %d distinct times", len(distinct))
	}
	if len(got) > len(ids) {
		t.Errorf("bulk map has %d entries for %d ids", len(got), len(ids))
	}
	// Asking for a subset returns only that subset.
	sub := ids[:3]
	var part map[int64]int64
	if err := s.ReadTx(func(rd *Reader) (e error) {
		part, e = rd.LastSeenByFile(sub)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	for id := range part {
		if id != sub[0] && id != sub[1] && id != sub[2] {
			t.Errorf("subset query returned file %d", id)
		}
	}
}
