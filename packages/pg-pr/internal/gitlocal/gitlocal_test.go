package gitlocal

import (
	"context"
	"errors"
	"testing"

	"github.com/phillipgreenii/x/gitclient"
)

// fakeHistoryReader is a canned x/gitclient.HistoryReader for exercising
// ChangedFiles/Commits' mapping to this package's own FileChange/Commit
// types without touching a real git repository. It also records the
// argument each call was made with, so tests can assert on the default-base
// behavior ChangedFiles/Commits apply before delegating.
type fakeHistoryReader struct {
	changes    []gitclient.FileChange
	changesErr error
	lastBase   string

	commits    []gitclient.Commit
	commitsErr error
	lastOpts   gitclient.LogOptions
}

func (f *fakeHistoryReader) ChangedFiles(_ context.Context, base string) ([]gitclient.FileChange, error) {
	f.lastBase = base
	return f.changes, f.changesErr
}

func (f *fakeHistoryReader) Commits(_ context.Context, opts gitclient.LogOptions) ([]gitclient.Commit, error) {
	f.lastOpts = opts
	return f.commits, f.commitsErr
}

func TestChangedFiles_MapsGitclientFileChanges(t *testing.T) {
	r := &fakeHistoryReader{changes: []gitclient.FileChange{
		{Path: "main.go", Additions: 10, Deletions: 2},
		{Path: "readme.md", Additions: 5, Deletions: 0},
		{Path: "foo.bin", Binary: true},
	}}
	files, err := ChangedFiles(context.Background(), r, "/tmp", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d: %+v", len(files), files)
	}
	if files[0].Path != "main.go" || files[0].Additions != 10 || files[0].Deletions != 2 {
		t.Fatalf("file[0]: %+v", files[0])
	}
	if !files[2].Binary {
		t.Fatalf("file[2] should be binary: %+v", files[2])
	}
	// Default base used.
	if r.lastBase != "origin/main" {
		t.Fatalf("expected default base origin/main, got %q", r.lastBase)
	}
}

func TestChangedFiles_PropagatesError(t *testing.T) {
	r := &fakeHistoryReader{changesErr: errors.New("git boom")}
	_, err := ChangedFiles(context.Background(), r, "/tmp", "")
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestCommits_MapsGitclientCommits(t *testing.T) {
	r := &fakeHistoryReader{commits: []gitclient.Commit{
		{
			SHA: "abc123", Subject: "first subject", Body: "first body",
			Author: gitclient.Signature{Name: "alice", Email: "a@x"},
		},
		{
			SHA: "def456", Subject: "second subject", Body: "",
			Author: gitclient.Signature{Name: "bob", Email: "b@x"},
		},
	}}
	cs, err := Commits(context.Background(), r, "/tmp", "main")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(cs) != 2 {
		t.Fatalf("expected 2 commits, got %d: %+v", len(cs), cs)
	}
	if cs[0].SHA != "abc123" || cs[0].Author != "alice <a@x>" {
		t.Fatalf("commit[0]: %+v", cs[0])
	}
	if cs[1].Body != "" {
		t.Fatalf("commit[1] body should be empty (trimmed), got %q", cs[1].Body)
	}
	// base is threaded through as LogOptions.Base (Base..Head, Head
	// defaulting to HEAD inside x/gitclient).
	if r.lastOpts.Base != "main" {
		t.Fatalf("expected LogOptions.Base = %q, got %+v", "main", r.lastOpts)
	}
}

func TestCommits_EmptyOutput(t *testing.T) {
	r := &fakeHistoryReader{commits: nil}
	cs, err := Commits(context.Background(), r, "/tmp", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(cs) != 0 {
		t.Fatalf("expected 0 commits, got %d", len(cs))
	}
	// Default base used.
	if r.lastOpts.Base != "origin/main" {
		t.Fatalf("expected default base origin/main, got %+v", r.lastOpts)
	}
}
