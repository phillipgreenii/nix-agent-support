package patheval

import (
	"os"
	"path/filepath"
	"testing"
)

// The memo is opt-in: with it off, a changed filesystem is seen at once; with
// it on, the first answer is served until restore (pg2-1w4c4n).
func TestMemoizeRealPaths_OffByDefaultAndScopedToRestore(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target1 := filepath.Join(dir, "t1")
	target2 := filepath.Join(dir, "t2")
	for _, d := range []string{target1, target2} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target1, link); err != nil {
		t.Fatal(err)
	}

	// Off by default: re-pointing the link is observed.
	if got := evalSymlinksWithFallback(link); got != target1 {
		t.Fatalf("memo off: got %q, want %q", got, target1)
	}

	restore := MemoizeRealPaths()
	if got := evalSymlinksWithFallback(link); got != target1 {
		t.Fatalf("memo on, first call: got %q, want %q", got, target1)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target2, link); err != nil {
		t.Fatal(err)
	}
	if got := evalSymlinksWithFallback(link); got != target1 {
		t.Fatalf("memo on after re-point: got %q, want the memoized %q", got, target1)
	}

	// A nonexistent path is memoized too (its fallback walk is the costly one).
	missing := filepath.Join(dir, "no", "such", "leaf")
	want := filepath.Join(dir, "no", "such", "leaf")
	if got := evalSymlinksWithFallback(missing); got != want {
		t.Fatalf("missing path: got %q, want %q", got, want)
	}

	restore()
	if got := evalSymlinksWithFallback(link); got != target2 {
		t.Fatalf("memo restored: got %q, want the live %q", got, target2)
	}
}

func TestExists_MemoizedOnlyWhileOn(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "marker")
	if Exists(p) {
		t.Fatal("Exists on a missing path = true")
	}
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !Exists(p) {
		t.Fatal("memo off: Exists must see the new file")
	}

	restore := MemoizeRealPaths()
	if !Exists(p) {
		t.Fatal("memo on: Exists = false for an existing file")
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if !Exists(p) {
		t.Fatal("memo on: want the memoized answer (true) after removal")
	}
	restore()
	if Exists(p) {
		t.Fatal("memo restored: Exists must see the removal")
	}
}
