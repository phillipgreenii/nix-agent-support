package livestate

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{
		"-C", dir,
		"-c", "user.name=t", "-c", "user.email=t@example.com",
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
	}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo makes a repo with commit A on main and a conflicting topic branch.
func newRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "-b", "main")
	write(t, filepath.Join(dir, "f.txt"), "base\n")
	git(t, dir, "add", "f.txt")
	git(t, dir, "commit", "-q", "-m", "base")
	git(t, dir, "checkout", "-q", "-b", "topic")
	write(t, filepath.Join(dir, "f.txt"), "topic\n")
	git(t, dir, "commit", "-qam", "topic")
	git(t, dir, "checkout", "-q", "main")
	write(t, filepath.Join(dir, "f.txt"), "main\n")
	git(t, dir, "commit", "-qam", "main")
	return dir
}

// stopRebase leaves dir stopped in a conflicting rebase of topic onto main.
func stopRebase(t *testing.T, dir string) {
	t.Helper()
	git(t, dir, "checkout", "-q", "topic")
	cmd := exec.Command("git", "-C", dir, "rebase", "main")
	if err := cmd.Run(); err == nil {
		t.Fatal("rebase unexpectedly succeeded")
	}
}

// fingerprint hashes every path, mode, mtime and content under dir, .git included.
func fingerprint(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		h := ""
		if fi.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			h = fmt.Sprintf("%x", sha256.Sum256(b))
		}
		lines = append(lines, fmt.Sprintf("%s %v %d %s", p, fi.Mode(), fi.ModTime().UnixNano(), h))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func verifyUnchanged(t *testing.T, dir string, snap Snapshot, opt Options) Result {
	t.Helper()
	before := fingerprint(t, dir)
	r := Verify(dir, snap, opt)
	if after := fingerprint(t, dir); after != before {
		t.Errorf("Verify changed files on disk")
	}
	return r
}

func capture(t *testing.T, dir string) Snapshot {
	t.Helper()
	s, err := Capture(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStoppedRebaseIsIntact(t *testing.T) {
	dir := newRepo(t)
	stopRebase(t, dir)
	snap := capture(t, dir)
	if snap.Operation != "rebase" || snap.StatusCount == 0 || snap.Kind != KindCanonicalClone {
		t.Fatalf("unexpected snapshot %+v", snap)
	}
	r := verifyUnchanged(t, dir, snap, Options{})
	if r.Outcome != Intact {
		t.Fatalf("got %s (%s), want intact", r.Outcome, r.Reason)
	}
}

func TestCleanTreeIsResolved(t *testing.T) {
	dir := newRepo(t)
	stopRebase(t, dir)
	snap := capture(t, dir)
	git(t, dir, "rebase", "--abort")
	r := verifyUnchanged(t, dir, snap, Options{})
	if r.Outcome != Resolved {
		t.Fatalf("got %s (%s), want resolved", r.Outcome, r.Reason)
	}
}

func TestDriftedHead(t *testing.T) {
	dir := newRepo(t)
	stopRebase(t, dir)
	snap := capture(t, dir)
	snap.Head = strings.Repeat("0", 40) // the recorded HEAD no longer matches
	r := verifyUnchanged(t, dir, snap, Options{})
	if r.Outcome != Drifted || !strings.Contains(r.Reason, "HEAD") {
		t.Fatalf("got %s (%s), want drifted on HEAD", r.Outcome, r.Reason)
	}
}

func TestDriftedStatusDigest(t *testing.T) {
	dir := newRepo(t)
	stopRebase(t, dir)
	snap := capture(t, dir)
	write(t, filepath.Join(dir, "extra.txt"), "new\n")
	r := verifyUnchanged(t, dir, snap, Options{})
	if r.Outcome != Drifted {
		t.Fatalf("got %s (%s), want drifted", r.Outcome, r.Reason)
	}
}

func TestDriftedOtherOperation(t *testing.T) {
	dir := newRepo(t)
	stopRebase(t, dir)
	snap := capture(t, dir)
	snap.Operation = "merge"
	if r := verifyUnchanged(t, dir, snap, Options{}); r.Outcome != Drifted {
		t.Fatalf("got %s (%s), want drifted", r.Outcome, r.Reason)
	}
}

func TestMissingPathIsResolved(t *testing.T) {
	dir := newRepo(t)
	stopRebase(t, dir)
	snap := capture(t, dir)
	r := Verify(filepath.Join(dir, "gone"), snap, Options{})
	if r.Outcome != Resolved {
		t.Fatalf("got %s (%s), want resolved", r.Outcome, r.Reason)
	}
}

func TestOtherHostIsUnreachable(t *testing.T) {
	dir := newRepo(t)
	stopRebase(t, dir)
	snap := capture(t, dir)
	snap.Host = "elsewhere"
	r := verifyUnchanged(t, dir, snap, Options{Host: "here"})
	if r.Outcome != Unreachable {
		t.Fatalf("got %s (%s), want unreachable", r.Outcome, r.Reason)
	}
}

func TestUnreadablePathIsUnreachable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions are not enforced for root")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	r := Verify(filepath.Join(parent, "x"), Snapshot{}, Options{})
	if r.Outcome != Unreachable {
		t.Fatalf("got %s (%s), want unreachable", r.Outcome, r.Reason)
	}
}

func TestNotARepoIsDrifted(t *testing.T) {
	dir := newRepo(t)
	stopRebase(t, dir)
	snap := capture(t, dir)
	plain := t.TempDir()
	if r := verifyUnchanged(t, plain, snap, Options{}); r.Outcome != Drifted {
		t.Fatalf("got %s (%s), want drifted", r.Outcome, r.Reason)
	}
}

func TestLinkedWorktreeKind(t *testing.T) {
	dir := newRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, dir, "worktree", "add", "-q", "-b", "wtb", wt, "main")
	wt, _ = filepath.EvalSymlinks(wt)
	s := capture(t, wt)
	if s.Kind != KindWorktree || s.Branch != "wtb" || s.Toplevel != wt {
		t.Fatalf("unexpected snapshot %+v", s)
	}
}
