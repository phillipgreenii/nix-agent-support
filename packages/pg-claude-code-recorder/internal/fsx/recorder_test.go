package fsx

import (
	"os"
	"path/filepath"
	"testing"
)

// goodAppend performs the copier's order: append, fsync, then the (marked) commit.
func goodAppend(t *testing.T, r *Recorder, path string) {
	t.Helper()
	f, err := r.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteAt([]byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	r.Mark("commit")
}

func TestRecorderAcceptsTheCopiersAppendOrder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "1.1")
	r := NewRecorder(nil)
	goodAppend(t, r, p)
	if err := AssertOrder(r.Events(), CopierAppendOrder(p)...); err != nil {
		t.Fatal(err)
	}
}

func TestRecorderRejectsACommitBeforeTheFsync(t *testing.T) {
	p := filepath.Join(t.TempDir(), "1.1")
	r := NewRecorder(nil)
	f, _ := r.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	_, _ = f.WriteAt([]byte("data"), 0)
	r.Mark("commit") // wrong: committed before the fsync
	_ = f.Sync()
	_ = f.Close()
	if err := AssertOrder(r.Events(), CopierAppendOrder(p)...); err == nil {
		t.Fatal("a commit before the fsync passed")
	}
}

func TestRecorderRejectsAMissingFsync(t *testing.T) {
	p := filepath.Join(t.TempDir(), "1.1")
	r := NewRecorder(nil)
	f, _ := r.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	_, _ = f.WriteAt([]byte("data"), 0)
	_ = f.Close()
	r.Mark("commit")
	if err := AssertOrder(r.Events(), CopierAppendOrder(p)...); err == nil {
		t.Fatal("a missing fsync passed")
	}
}

func TestRecorderRejectsASkippedDirectoryFsync(t *testing.T) {
	dir := t.TempDir()
	tmp, final := filepath.Join(dir, "1.1.tmp"), filepath.Join(dir, "1.1")
	r := NewRecorder(nil)
	f, _ := r.OpenFile(tmp, os.O_RDWR|os.O_CREATE, 0o600)
	_, _ = f.WriteAt([]byte("data"), 0)
	_ = f.Sync()
	_ = f.Close()
	_ = r.Rename(tmp, final)
	r.Mark("commit") // wrong: the directory was never fsynced
	if err := AssertOrder(r.Events(), CopierNewCopyOrder(tmp, dir)...); err == nil {
		t.Fatal("a skipped directory fsync passed")
	}
	// The right order passes.
	r2 := NewRecorder(nil)
	f, _ = r2.OpenFile(tmp, os.O_RDWR|os.O_CREATE, 0o600)
	_, _ = f.WriteAt([]byte("data"), 0)
	_ = f.Sync()
	_ = f.Close()
	_ = r2.Rename(final, tmp) // restore name for the second run
	_ = r2.Rename(tmp, final)
	if err := r2.SyncDir(dir); err != nil {
		t.Fatal(err)
	}
	r2.Mark("commit")
	if err := AssertOrder(r2.Events(), CopierNewCopyOrder(tmp, dir)...); err != nil {
		t.Fatal(err)
	}
}

func TestRecorderRecordsRemovalAndTruncate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x")
	r := NewRecorder(nil)
	f, _ := r.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	_ = f.Truncate(0)
	_ = f.Close()
	_ = r.Remove(p)
	ev := r.Events()
	want := []string{"create", "truncate", "remove"}
	if len(ev) != 3 {
		t.Fatalf("events %v", ev)
	}
	for i, w := range want {
		if ev[i].Op != w {
			t.Fatalf("event %d = %s want %s", i, ev[i].Op, w)
		}
	}
}
