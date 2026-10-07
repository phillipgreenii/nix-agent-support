package tail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendTo(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, _ := os.ReadFile(p)
	return string(b)
}

func newT(dir string) (*Tailer, *State) {
	return &Tailer{Src: filepath.Join(dir, "src.log"), Dst: filepath.Join(dir, "dst.log"), Now: func() time.Time { return time.Unix(0, 0) }}, &State{}
}

func TestIncrementalCopyOnlyCompleteLines(t *testing.T) {
	dir := t.TempDir()
	tl, st := newT(dir)
	write(t, tl.Src, "a\nb\npartial")
	if n, err := tl.Sync(st); err != nil || n != 4 {
		t.Fatalf("first sync %d %v", n, err)
	}
	appendTo(t, tl.Src, "-rest\nc\n")
	if _, err := tl.Sync(st); err != nil {
		t.Fatal(err)
	}
	if got := read(t, tl.Dst); got != "a\nb\npartial-rest\nc\n" {
		t.Fatalf("copy = %q", got)
	}
	if n, _ := tl.Sync(st); n != 0 {
		t.Errorf("idle sync copied %d", n)
	}
}

func TestStartAtEndSkipsHistoryButNotAPartialRow(t *testing.T) {
	dir := t.TempDir()
	tl, st := newT(dir)
	tl.StartAtEnd = true
	write(t, tl.Src, "old1\nold2\nhalf")
	if _, err := tl.Sync(st); err != nil {
		t.Fatal(err)
	}
	appendTo(t, tl.Src, "-done\nnew\n")
	if _, err := tl.Sync(st); err != nil {
		t.Fatal(err)
	}
	if got := read(t, tl.Dst); got != "half-done\nnew\n" {
		t.Fatalf("copy = %q (history must be skipped, the in-progress row kept whole)", got)
	}
}

func TestRotationDrainsRotatedInodeFirst(t *testing.T) {
	dir := t.TempDir()
	tl, st := newT(dir)
	write(t, tl.Src, "r1\nr2\n")
	if _, err := tl.Sync(st); err != nil {
		t.Fatal(err)
	}
	// Rows land in the file, then it is rotated (renamed) before the next tick,
	// and a fresh file starts.
	appendTo(t, tl.Src, "r3\nr4\n")
	if err := os.Rename(tl.Src, tl.Src+".1"); err != nil {
		t.Fatal(err)
	}
	write(t, tl.Src, "n1\nn2\n")
	if _, err := tl.Sync(st); err != nil {
		t.Fatal(err)
	}
	if got := read(t, tl.Dst); got != "r1\nr2\nr3\nr4\nn1\nn2\n" {
		t.Fatalf("copy = %q: the rotated inode's tail must be drained before the new file", got)
	}
	if st.PossibleLoss != 0 {
		t.Errorf("a matched rotation loses nothing, PossibleLoss=%d", st.PossibleLoss)
	}
}

func TestCompactionResetsOffsetBehindAMarker(t *testing.T) {
	dir := t.TempDir()
	tl, st := newT(dir)
	write(t, tl.Src, "q1\nq2\nq3\n")
	if _, err := tl.Sync(st); err != nil {
		t.Fatal(err)
	}
	// Compaction: the file is replaced (new inode) by a shorter one, no .1.
	tmp := tl.Src + ".compact.tmp"
	write(t, tmp, "q3\n")
	if err := os.Rename(tmp, tl.Src); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.Sync(st); err != nil {
		t.Fatal(err)
	}
	got := read(t, tl.Dst)
	if !strings.Contains(got, `"_shadow":"reset"`) || !strings.HasSuffix(got, "q3\n") {
		t.Fatalf("copy = %q", got)
	}
	if st.Resets != 1 || st.PossibleLoss != 1 {
		t.Errorf("state %+v", st)
	}
}

func TestCopyTruncateResetsOffset(t *testing.T) {
	dir := t.TempDir()
	tl, st := newT(dir)
	write(t, tl.Src, "t1\nt2\nt3\n")
	if _, err := tl.Sync(st); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(tl.Src, os.O_WRONLY|os.O_TRUNC, 0o600) // same inode, now empty
	_ = f.Close()
	appendTo(t, tl.Src, "u1\n")
	if _, err := tl.Sync(st); err != nil {
		t.Fatal(err)
	}
	if got := read(t, tl.Dst); !strings.Contains(got, `"reason":"truncated"`) || !strings.HasSuffix(got, "u1\n") {
		t.Fatalf("copy = %q", got)
	}
}

func TestResumeAfterCrashIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	tl, st := newT(dir)
	write(t, tl.Src, "a\nb\n")
	if _, err := tl.Sync(st); err != nil {
		t.Fatal(err)
	}
	saved := *st // the state the collector last persisted
	appendTo(t, tl.Src, "c\nd\n")
	if _, err := tl.Sync(st); err != nil { // the append happens, the state write is lost
		t.Fatal(err)
	}
	resumed := saved
	tl2 := &Tailer{Src: tl.Src, Dst: tl.Dst}
	if _, err := tl2.Sync(&resumed); err != nil {
		t.Fatal(err)
	}
	if got := read(t, tl.Dst); got != "a\nb\nc\nd\n" {
		t.Fatalf("copy = %q: the repeated append must not duplicate rows", got)
	}
}

func TestMissingSourceIsQuiet(t *testing.T) {
	tl, st := newT(t.TempDir())
	if n, err := tl.Sync(st); err != nil || n != 0 {
		t.Fatalf("%d %v", n, err)
	}
}
