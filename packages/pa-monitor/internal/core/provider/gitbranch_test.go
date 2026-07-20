package provider

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeHead writes .git/HEAD under dir and returns the HEAD path.
func writeHead(t *testing.T, dir, contents string) string {
	t.Helper()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	head := filepath.Join(gitDir, "HEAD")
	if err := os.WriteFile(head, []byte(contents+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return head
}

func TestGitBranch_ReadsThenCachesUntilHeadChanges(t *testing.T) {
	dir := t.TempDir()
	head := writeHead(t, dir, "ref: refs/heads/foo")
	c := New(nil)
	fr := &fakeRec{}
	c.SetRecorder(fr)

	if got := c.GitBranch(dir); got != "foo" {
		t.Fatalf("first read: got %q want foo", got)
	}
	if got := c.GitBranch(dir); got != "foo" { // HEAD unchanged → cached
		t.Fatalf("cached read: got %q want foo", got)
	}
	if n := countKind(fr, "git_branch"); n != 1 {
		t.Fatalf("expected 1 git_branch record (miss then cache-hit), got %d", n)
	}

	// Switch branch + bump mtime deterministically.
	writeHead(t, dir, "ref: refs/heads/bar")
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(head, future, future); err != nil {
		t.Fatal(err)
	}
	if got := c.GitBranch(dir); got != "bar" {
		t.Fatalf("after HEAD change: got %q want bar", got)
	}
	if n := countKind(fr, "git_branch"); n != 2 {
		t.Fatalf("expected 2 git_branch records after a HEAD change, got %d", n)
	}
}

func TestGitBranch_SubdirectoryCwd(t *testing.T) {
	dir := t.TempDir()
	writeHead(t, dir, "ref: refs/heads/foo")
	sub := filepath.Join(dir, "a", "b", "c")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	c := New(nil)
	if got := c.GitBranch(sub); got != "foo" { // must walk parents, not return ""
		t.Fatalf("subdirectory cwd: got %q want foo", got)
	}
}

func TestGitBranch_DetachedHeadShortSha(t *testing.T) {
	dir := t.TempDir()
	writeHead(t, dir, "0123456789abcdef0123456789abcdef01234567")
	c := New(nil)
	if got := c.GitBranch(dir); got != "0123456" {
		t.Fatalf("detached HEAD: got %q want 0123456", got)
	}
}

func TestGitBranch_NonRepoNotCachedRewalks(t *testing.T) {
	dir := t.TempDir() // no .git
	c := New(nil)
	calls := 0
	c.FetchGitBranch = func(string) (string, string, bool) {
		calls++
		return "", "", false // not a repo
	}
	if got := c.GitBranch(dir); got != "" {
		t.Fatalf("non-repo: got %q want empty", got)
	}
	if got := c.GitBranch(dir); got != "" {
		t.Fatalf("non-repo (2nd): got %q want empty", got)
	}
	if calls != 2 {
		t.Fatalf("negative resolution must NOT be cached (re-walk each tick): got %d fetches", calls)
	}
}

func countKind(fr *fakeRec, kind string) int {
	n := 0
	for _, k := range fr.kinds {
		if k == kind {
			n++
		}
	}
	return n
}
