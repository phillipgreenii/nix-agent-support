package keepertest

import (
	"os"
	"path/filepath"
	"testing"
)

// The pg-keep-awake source is found from the working directory, so it does not
// depend on the compiled-in source path that -trimpath rewrites.
func TestSourceDirIsTheSiblingOfTheEnclosingModule(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "pa-monitor", "internal", "x")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pa-monitor", "go.mod"), []byte("module example.com/pa-monitor\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "pg-keep-awake", "cmd", "pg-keep-awake")
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(pkg)
	got, err := sourceDir()
	if err != nil {
		t.Fatal(err)
	}
	// t.TempDir may sit behind a symlink (/var on macOS): compare resolved paths.
	gotReal, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("sourceDir = %q, which does not exist: %v", got, err)
	}
	wantReal, err := filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatal(err)
	}
	if gotReal != wantReal {
		t.Fatalf("sourceDir = %q, want %q", gotReal, wantReal)
	}
}

func TestSourceDirFailsWithNoEnclosingModule(t *testing.T) {
	t.Chdir(t.TempDir())
	if got, err := sourceDir(); err == nil {
		t.Fatalf("sourceDir = %q with no go.mod above the working directory; want an error", got)
	}
}

// recordingTB records whether Fatal or Skip was called first.
type recordingTB struct {
	testing.TB
	first string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Fatal(...any) {
	if r.first == "" {
		r.first = "fatal"
	}
}

func (r *recordingTB) Skip(...any) {
	if r.first == "" {
		r.first = "skip"
	}
}

// Outside a nix build an unbuilt binary fails the test; inside one it skips, so
// a broken lookup cannot pass the suite by skipping it.
func TestBinFailsOutsideANixBuildAndSkipsInsideOne(t *testing.T) {
	saved := binPath
	t.Cleanup(func() { binPath = saved })
	binPath = ""

	t.Setenv("NIX_BUILD_TOP", "")
	outside := &recordingTB{TB: t}
	Bin(outside)
	if outside.first != "fatal" {
		t.Fatalf("outside a nix build Bin called %q first, want fatal", outside.first)
	}

	t.Setenv("NIX_BUILD_TOP", "/build")
	inside := &recordingTB{TB: t}
	Bin(inside)
	if inside.first != "skip" {
		t.Fatalf("inside a nix build Bin called %q first, want skip", inside.first)
	}
}
