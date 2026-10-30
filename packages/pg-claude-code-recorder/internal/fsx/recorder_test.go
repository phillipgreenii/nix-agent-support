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

// removalEvents builds the event list a removal of one file would record, in the
// given order of the named actions.
func removalEvents(order ...string) []Event {
	ev := map[string]Event{
		"rename":   {Op: "rename", Path: "/c/p/f.jsonl->/c/p/.pg-recorder-quarantine/1-f.jsonl"},
		"syncdir":  {Op: "syncdir", Path: "/c/p"},
		"holders":  {Op: "mark", Path: "holders"},
		"rehash":   {Op: "mark", Path: "rehash"},
		"fullsync": {Op: "fullsync", Path: "/s/archives/x/000001.seg"},
		"fsdir":    {Op: "fullsyncdir", Path: "/s/archives/x"},
		"remove":   {Op: "remove", Path: "/c/p/.pg-recorder-quarantine/1-f.jsonl"},
		"qsync":    {Op: "syncdir", Path: "/c/p/.pg-recorder-quarantine"},
	}
	var out []Event
	for _, o := range order {
		out = append(out, ev[o])
	}
	return out
}

func TestRemovalOrderAssertion(t *testing.T) {
	steps := RemovalOrder("/c/p", ".pg-recorder-quarantine", "000001.seg")
	right := removalEvents("rename", "syncdir", "holders", "rehash", "fullsync", "fsdir", "remove", "qsync")
	if err := AssertOrder(right, steps...); err != nil {
		t.Fatalf("the right order failed: %v", err)
	}
	for name, wrong := range map[string][]Event{
		"unlink before fullsync":           removalEvents("rename", "syncdir", "holders", "rehash", "remove", "fullsync", "fsdir", "qsync"),
		"skipped directory fsync":          removalEvents("rename", "holders", "rehash", "fullsync", "fsdir", "remove", "qsync"),
		"skipped quarantine dir fsync":     removalEvents("rename", "syncdir", "holders", "rehash", "fullsync", "fsdir", "remove"),
		"unlink before the holder listing": removalEvents("rename", "syncdir", "remove", "holders", "rehash", "fullsync", "fsdir", "qsync"),
		"skipped re-hash":                  removalEvents("rename", "syncdir", "holders", "fullsync", "fsdir", "remove", "qsync"),
	} {
		if err := AssertOrder(wrong, steps...); err == nil {
			t.Errorf("%s: a wrong sequence passed", name)
		}
	}
}
