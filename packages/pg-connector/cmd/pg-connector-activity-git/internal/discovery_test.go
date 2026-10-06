package internal

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func mkdirAll(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverRepos_OneLevelDeepOnly(t *testing.T) {
	root := t.TempDir()
	mkdirAll(t, filepath.Join(root, "a-clone", ".git"))
	mkdirAll(t, filepath.Join(root, "b-checkout"))
	if err := os.WriteFile(filepath.Join(root, "b-checkout", ".git"), []byte("gitdir: /elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mkdirAll(t, filepath.Join(root, "c-plain"))
	mkdirAll(t, filepath.Join(root, "d-group", "nested", ".git"))
	if err := os.WriteFile(filepath.Join(root, "e-file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	b := New(Options{Stderr: &stderr, Runner: nil})
	got := b.discoverRepos([]string{root})
	want := []string{filepath.Join(root, "a-clone"), filepath.Join(root, "b-checkout")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("discovered = %v, want %v", got, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestDiscoverRepos_SearchPathItselfIsNotARepo(t *testing.T) {
	root := t.TempDir()
	mkdirAll(t, filepath.Join(root, ".git"))
	b := New(Options{Stderr: &bytes.Buffer{}})
	if got := b.discoverRepos([]string{root}); len(got) != 0 {
		t.Errorf("discovered = %v, want none", got)
	}
}

func TestDiscoverRepos_BadSearchPathsSkippedOneLineEach(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	aFile := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(aFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	good := t.TempDir()
	mkdirAll(t, filepath.Join(good, "r", ".git"))

	var stderr bytes.Buffer
	b := New(Options{Stderr: &stderr})
	got := b.discoverRepos([]string{missing, aFile, good})
	if want := []string{filepath.Join(good, "r")}; !reflect.DeepEqual(got, want) {
		t.Errorf("discovered = %v, want %v", got, want)
	}
	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stderr has %d lines, want 2:\n%s", len(lines), stderr.String())
	}
	for i, p := range []string{missing, aFile} {
		if !strings.Contains(lines[i], p) {
			t.Errorf("line %d = %q, want it to name %q", i, lines[i], p)
		}
	}
}

func TestRepoList_DedupesByResolvedPath(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "r")
	mkdirAll(t, filepath.Join(repo, ".git"))
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	b := New(Options{Stderr: &bytes.Buffer{}})
	got := b.repoList(Config{RepoPaths: []string{link, repo}, RepoSearchPaths: []string{root}})
	if want := []string{link}; !reflect.DeepEqual(got, want) {
		t.Errorf("repoList = %v, want %v (one entry, first configured spelling)", got, want)
	}
}
