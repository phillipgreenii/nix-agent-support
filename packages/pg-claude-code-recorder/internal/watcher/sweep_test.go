package watcher

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// The sweep (getattrlistbulk on darwin with cgo, a directory walk elsewhere)
// MUST report the same identity an lstat does.
func TestSweepMatchesLstatIdentity(t *testing.T) {
	root := t.TempDir()
	want := map[string]string{
		"example-a.jsonl":                 "{}\n",
		"example-p/example-b.jsonl":       "hello world\n",
		"example-p/deep/example-c.jsonl":  "",
		"example-q/deep/x/example-d.json": "12345",
	}
	for rel, body := range want {
		write(t, filepath.Join(root, rel), body)
	}
	if err := os.Symlink(filepath.Join(root, "example-a.jsonl"), filepath.Join(root, "example-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "example-empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := platformSweep(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("swept %d files, want %d: %+v", len(got), len(want), got)
	}
	sort.Slice(got, func(i, j int) bool { return got[i].Path < got[j].Path })
	for _, fi := range got {
		ref, ok, err := lstatInfo(fi.Path)
		if err != nil || !ok {
			t.Fatalf("lstat %s: ok=%v err=%v", fi.Path, ok, err)
		}
		if fi.Dev != ref.Dev || fi.Ino != ref.Ino || fi.Size != ref.Size ||
			!fi.Birth.Equal(ref.Birth) || !fi.ModTime.Equal(ref.ModTime) {
			t.Errorf("identity mismatch for %s:\n sweep %+v\n lstat %+v", fi.Path, fi, ref)
		}
		if fi.Ino == 0 || fi.ModTime.IsZero() {
			t.Errorf("incomplete identity for %s: %+v", fi.Path, fi)
		}
	}
}

func TestWalkSweepSkipsMissingRootError(t *testing.T) {
	if _, err := platformSweep(filepath.Join(t.TempDir(), "example-nope")); err == nil {
		t.Fatal("expected an error for a missing root")
	}
}
