package gitlocal

import (
	"context"
	"errors"
	"testing"
)

type fakeRunner struct {
	out  []byte
	err  error
	args [][]string
}

func (f *fakeRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	f.args = append(f.args, append([]string(nil), args...))
	return f.out, f.err
}

func TestChangedFiles_ParsesNumstat(t *testing.T) {
	r := &fakeRunner{out: []byte("10\t2\tmain.go\n5\t0\treadme.md\n-\t-\tfoo.bin\n")}
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
	last := r.args[len(r.args)-1]
	if last[len(last)-1] != "origin/main...HEAD" {
		t.Fatalf("expected default base origin/main, got %v", last)
	}
}

func TestChangedFiles_PropagatesError(t *testing.T) {
	r := &fakeRunner{err: errors.New("git boom")}
	_, err := ChangedFiles(context.Background(), r, "/tmp", "")
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestCommits_ParsesLog(t *testing.T) {
	// Two commits, each is 4 NUL-separated fields. -z separates records by
	// another \x00 byte. Final trailing \x00 is trimmed.
	out := "abc123\x00first subject\x00first body\x00alice <a@x>\x00def456\x00second subject\x00\x00bob <b@x>\x00"
	r := &fakeRunner{out: []byte(out)}
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
	last := r.args[len(r.args)-1]
	if last[1] != "main..HEAD" {
		t.Fatalf("expected main..HEAD, got %v", last)
	}
}

func TestCommits_EmptyOutput(t *testing.T) {
	r := &fakeRunner{out: []byte("")}
	cs, err := Commits(context.Background(), r, "/tmp", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(cs) != 0 {
		t.Fatalf("expected 0 commits, got %d", len(cs))
	}
}
