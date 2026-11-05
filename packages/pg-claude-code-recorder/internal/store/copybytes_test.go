package store

import (
	"os"
	"testing"
)

// CopyBytes must equal the bytes of the files the rows describe, with real
// temp files as the oracle, and must not count a copy an archive has replaced.
func TestCopyBytesMatchesOnDiskAndSkipsReclaimed(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	defer func() { _ = s.Close() }()
	write := func(p string, n int) {
		t.Helper()
		if err := os.WriteFile(p, make([]byte, n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.wdb.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// file 1: generation 1 (reclaimed by an archive), generation 2 (live, 300 bytes, 7 tail bytes).
	exec(`INSERT INTO generations(file_id,gen,start_ns,copy_offset,tail_len) VALUES(1,1,1,500,10),(1,2,2,300,7)`)
	exec(`INSERT INTO ranges(file_id,gen,from_off,to_off,session,seg) VALUES(1,1,0,500,'s','seg')`)
	// file 2: a partly archived generation keeps its whole copy.
	exec(`INSERT INTO generations(file_id,gen,start_ns,copy_offset,tail_len) VALUES(2,1,1,200,0)`)
	exec(`INSERT INTO ranges(file_id,gen,from_off,to_off,session,seg) VALUES(2,1,0,100,'s','seg')`)
	// snapshots: version 1 live, version 2 archived (bytes deleted).
	exec(`INSERT INTO snapshots(file_id,version,hash,size,seen_ns) VALUES(3,1,0,40,1),(3,2,0,60,1)`)
	exec(`INSERT INTO ranges(file_id,gen,from_off,to_off,session,seg) VALUES(3,2,0,60,'s','seg')`)

	// On-disk state as the copier leaves it: reclaimed copy 1.1 is gone, tail sidecars exist.
	write(s.CopyPath(1, 2), 300)
	write(s.TailPath(1), 10+7) // generation 1's tail (10) and generation 2's tail (7)
	write(s.CopyPath(2, 1), 200)
	write(s.SnapshotPath(3, 1), 40)
	// An orphan the database does not know: documented as NOT counted.
	write(s.CopyPath(99, 1), 12345)

	var want int64 = 300 + 17 + 200 + 40
	got, err := s.CopyBytes()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("CopyBytes %d, want %d", got, want)
	}
}
