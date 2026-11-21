package worktree

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/gitclient"
)

// interruptedFixture builds a real repo and a bead worktree holding one unique
// commit plus the killed-checkout residue (aged zero-byte index.lock), after
// applying dirty to the worktree.
func interruptedFixture(t *testing.T, dirty func(path string)) (open Opener, repoRoot, wtDir, path string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()
	repoRoot = newCommittedRepo(t)
	wtDir = filepath.Join(t.TempDir(), "worktrees")
	open = realOpener(t)
	var err error
	path, err = Ensure(ctx, open, wtDir, repoRoot, "zr-man")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "work.txt"), []byte("committed"), 0o644); err != nil {
		t.Fatal(err)
	}
	wc, err := open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	cm := wc.(gitclient.Committer)
	if err := cm.Add(ctx, "work.txt"); err != nil {
		t.Fatal(err)
	}
	h, err := cm.Commit(ctx, "unique work")
	if err == nil {
		err = h.Wait()
	}
	if err != nil {
		t.Fatal(err)
	}
	dirty(path)
	admin, _ := adminDir(path)
	lock := filepath.Join(admin, "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * StaleIndexLockAge)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	return open, repoRoot, wtDir, path
}

func heldDir(t *testing.T, wtDir string) string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(wtDir+"-interrupted", "zr-man-*"))
	if len(m) != 1 {
		t.Fatalf("holding entries = %v, want exactly one", m)
	}
	return m[0]
}

func readManifest(t *testing.T, dir string) (RecoveryManifest, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	var m RecoveryManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	p, err := os.ReadFile(filepath.Join(dir, m.PatchFile))
	if err != nil {
		t.Fatal(err)
	}
	return m, string(p)
}

func TestDiscardInterrupted_manifestForTrackedAndUntrackedEdits(t *testing.T) {
	open, repoRoot, wtDir, path := interruptedFixture(t, func(p string) {
		if err := os.WriteFile(filepath.Join(p, "work.txt"), []byte("edited"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "new.txt"), []byte("untracked"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := Ensure(context.Background(), open, wtDir, repoRoot, "zr-man"); err != nil {
		t.Fatal(err)
	}
	dir := heldDir(t, wtDir)
	m, patch := readManifest(t, dir)
	if m.BaseCommit == "" || m.Branch != "pg-router/zr-man" || m.OriginalPath != path {
		t.Errorf("manifest = %+v", m)
	}
	if !strings.Contains(m.StatusPorcels, "work.txt") || !strings.Contains(m.StatusPorcels, "?? new.txt") {
		t.Errorf("status = %q", m.StatusPorcels)
	}
	for _, want := range []string{"+edited", "new.txt", "+untracked"} {
		if !strings.Contains(patch, want) {
			t.Errorf("patch lacks %q:\n%s", want, patch)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("only manifest and patch should remain, got %v", entries)
	}
}

func TestDiscardInterrupted_manifestForUntrackedOnly(t *testing.T) {
	open, repoRoot, wtDir, _ := interruptedFixture(t, func(p string) {
		if err := os.WriteFile(filepath.Join(p, "only.txt"), []byte("solo"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := Ensure(context.Background(), open, wtDir, repoRoot, "zr-man"); err != nil {
		t.Fatal(err)
	}
	m, patch := readManifest(t, heldDir(t, wtDir))
	if !strings.Contains(m.StatusPorcels, "?? only.txt") || !strings.Contains(patch, "+solo") {
		t.Errorf("status %q patch %q", m.StatusPorcels, patch)
	}
}

func TestDiscardInterrupted_manifestForCleanTreeWithUniqueCommits(t *testing.T) {
	open, repoRoot, wtDir, path := interruptedFixture(t, func(string) {})
	if _, err := Ensure(context.Background(), open, wtDir, repoRoot, "zr-man"); err != nil {
		t.Fatal(err)
	}
	m, patch := readManifest(t, heldDir(t, wtDir))
	if m.BaseCommit == "" || patch != "" || m.StatusPorcels != "" {
		t.Errorf("clean tree: manifest %+v patch %q", m, patch)
	}
	// Recreated worktree is on the branch tip (unique commit present).
	if _, err := os.Stat(filepath.Join(path, "work.txt")); err != nil {
		t.Errorf("branch not re-attached: %v", err)
	}
}

func TestDiscardInterrupted_manifestFailureKeepsFullTree(t *testing.T) {
	open, repoRoot, wtDir, _ := interruptedFixture(t, func(p string) {
		if err := os.WriteFile(filepath.Join(p, "scratch.txt"), []byte("keep me"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	orig := manifestWriter
	t.Cleanup(func() { manifestWriter = orig })
	manifestWriter = func(_ context.Context, _, dest, _, _ string) error {
		_ = os.MkdirAll(dest, 0o755)
		_ = os.WriteFile(filepath.Join(dest, ManifestFile), []byte("partial"), 0o644)
		return errors.New("disk full")
	}
	if _, err := Ensure(context.Background(), open, wtDir, repoRoot, "zr-man"); err != nil {
		t.Fatal(err)
	}
	dir := heldDir(t, wtDir)
	if b, err := os.ReadFile(filepath.Join(dir, "scratch.txt")); err != nil || string(b) != "keep me" {
		t.Errorf("full tree not kept: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Errorf("tree lacks .git: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ManifestFile)); string(b) == "partial" {
		t.Error("partial manifest left in kept tree")
	}
}
