package patchid

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pb/internal/run"
)

func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir) // keep git config writes inside temp (nix sandbox is read-only HOME)
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(
		os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func commit(t *testing.T, dir, file, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", file)
	runGit(t, dir, "commit", "-m", msg)
}

func TestComputeAndScan_findsCommitPatchID(t *testing.T) {
	dir := initRepo(t)
	commit(t, dir, "a.txt", "hello\n", "add a")
	commit(t, dir, "b.txt", "world\n", "add b")
	c := Client{R: run.CLIRunner{}}
	id, err := c.Compute(context.Background(), dir, "HEAD")
	if err != nil || id == "" {
		t.Fatalf("Compute: id=%q err=%v", id, err)
	}
	set, err := c.ScanPatchIDs(context.Background(), dir, "-n 10 HEAD")
	if err != nil {
		t.Fatalf("ScanPatchIDs: %v", err)
	}
	if !set[id] {
		t.Errorf("scan did not find HEAD patch-id %q in %v", id, set)
	}
}

// TestScanPatchIDCommits_mapsPatchIDToItsCommit pins the fact the gate's lock
// condition rests on: `git patch-id` prints the COMMIT sha beside each patch-id
// when fed from `git log -p`, so the gated commit is recoverable from the scan the
// gate check already runs — no extra git call and no new gate metadata. Asserted
// against real git, because it is git's output shape being relied upon.
func TestScanPatchIDCommits_mapsPatchIDToItsCommit(t *testing.T) {
	dir := initRepo(t)
	commit(t, dir, "a.txt", "hello\n", "add a")
	base := strings.TrimSpace(mustGit(t, dir, "rev-parse", "HEAD"))
	commit(t, dir, "b.txt", "world\n", "add b")
	head := strings.TrimSpace(mustGit(t, dir, "rev-parse", "HEAD"))

	c := Client{R: run.CLIRunner{}}
	id, err := c.Compute(context.Background(), dir, "HEAD")
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	byID, err := c.ScanPatchIDCommits(context.Background(), dir, base+"..HEAD")
	if err != nil {
		t.Fatalf("ScanPatchIDCommits: %v", err)
	}
	shas, ok := byID[id]
	if !ok {
		t.Fatalf("scan did not find HEAD patch-id %q in %v", id, byID)
	}
	if len(shas) != 1 || shas[0] != head {
		t.Errorf("patch-id %q maps to %v, want exactly [%s]", id, shas, head)
	}
	// The key set must remain exactly what ScanPatchIDs reports.
	set, err := c.ScanPatchIDs(context.Background(), dir, base+"..HEAD")
	if err != nil {
		t.Fatalf("ScanPatchIDs: %v", err)
	}
	if len(set) != len(byID) || !set[id] {
		t.Errorf("ScanPatchIDs set %v must be the key set of %v", set, byID)
	}
}

func TestScan_emptyRangeYieldsEmptySet(t *testing.T) {
	dir := initRepo(t)
	commit(t, dir, "a.txt", "hello\n", "c1")
	c := Client{R: run.CLIRunner{}}
	// HEAD..HEAD is an empty range.
	set, err := c.ScanPatchIDs(context.Background(), dir, "HEAD..HEAD")
	if err != nil {
		t.Fatalf("ScanPatchIDs: %v", err)
	}
	if len(set) != 0 {
		t.Errorf("empty range set = %v, want empty", set)
	}
}

func TestComputeStableAcrossRebase(t *testing.T) {
	dir := initRepo(t)
	commit(t, dir, "base.txt", "base\n", "base")
	commit(t, dir, "feat.txt", "feature\n", "feat")
	c := Client{R: run.CLIRunner{}}
	before, err := c.Compute(context.Background(), dir, "HEAD")
	if err != nil {
		t.Fatalf("Compute before: %v", err)
	}
	// Rewrite history: amend the base commit's message (changes SHAs of both).
	runGit(t, dir, "rebase", "--exec", "true", "--root") // no-op exec forces re-application
	after, err := c.Compute(context.Background(), dir, "HEAD")
	if err != nil {
		t.Fatalf("Compute after: %v", err)
	}
	if before != after {
		t.Errorf("patch-id changed across rebase: before=%q after=%q", before, after)
	}
}

func TestIsAncestor(t *testing.T) {
	dir := initRepo(t)
	commit(t, dir, "a.txt", "1\n", "c1")
	c := Client{R: run.CLIRunner{}}
	first := strings.TrimSpace(mustGit(t, dir, "rev-parse", "HEAD"))
	commit(t, dir, "a.txt", "2\n", "c2")
	if !c.IsAncestor(context.Background(), dir, first, "HEAD") {
		t.Error("first commit should be ancestor of HEAD")
	}
	if c.IsAncestor(context.Background(), dir, "HEAD", first) {
		t.Error("HEAD should not be ancestor of first commit")
	}
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}
