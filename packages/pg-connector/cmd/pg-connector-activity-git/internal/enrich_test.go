package internal

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSingleBranch(t *testing.T) {
	cases := []struct {
		name, refs, want string
	}{
		{"one head", "refs/heads/feature-x", "feature-x"},
		{"slashed name keeps its slashes", "refs/heads/user/topic", "user/topic"},
		{"two heads", "refs/heads/main\nrefs/heads/feature-x", ""},
		{"no heads", "", ""},
		{"non-head refs are ignored", "refs/remotes/origin/main\nrefs/heads/main", "main"},
		{"only a non-head ref", "refs/tags/v1", ""},
	}
	for _, c := range cases {
		if got := singleBranch(c.refs); got != c.want {
			t.Errorf("%s: singleBranch(%q) = %q, want %q", c.name, c.refs, got, c.want)
		}
	}
}

func TestSumNumstat(t *testing.T) {
	cases := []struct {
		name, out string
		ins, del  int
	}{
		{"empty", "", 0, 0},
		{"two files", "3\t1\ta.txt\n4\t0\tdir/b.txt", 7, 1},
		{"binary file reports dashes", "-\t-\tlogo.bin\n2\t5\tc.txt", 2, 5},
		{"path with a tab", "1\t1\ta\tb.txt", 1, 1},
		{"garbage line is skipped", "not numstat\n1\t2\tf", 1, 2},
	}
	for _, c := range cases {
		ins, del := sumNumstat(c.out)
		if ins != c.ins || del != c.del {
			t.Errorf("%s: got +%d -%d, want +%d -%d", c.name, ins, del, c.ins, c.del)
		}
	}
}

func TestEnrichCommit_UsesRunnerForEveryGitCall(t *testing.T) {
	r := &fakeRunner{run: func(_ string, args ...string) (string, error) {
		switch args[0] {
		case "for-each-ref":
			return "refs/heads/only", nil
		case "diff-tree":
			return "5\t2\tf", nil
		}
		return "", errors.New("unexpected git call")
	}}
	d, err := enrichCommit(context.Background(), r, "/clones/x", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if d != (commitDetails{branch: "only", insertions: 5, deletions: 2}) {
		t.Errorf("details = %+v", d)
	}
	if len(r.calls) != 2 {
		t.Fatalf("calls = %v, want exactly two git calls per commit", r.calls)
	}
	for _, c := range r.calls {
		if c[0] != "/clones/x" {
			t.Errorf("call %v not rooted at the repo", c)
		}
	}
}

func TestEnrichCommit_GitFailureIsNotSilent(t *testing.T) {
	for _, failing := range []string{"for-each-ref", "diff-tree"} {
		r := &fakeRunner{run: func(_ string, args ...string) (string, error) {
			if args[0] == failing {
				return "", errors.New("boom")
			}
			return "", nil
		}}
		if _, err := enrichCommit(context.Background(), r, "/r", "abc"); err == nil || !strings.Contains(err.Error(), "boom") {
			t.Errorf("%s failing: err = %v", failing, err)
		}
	}
}

// An empty candidate list answers an empty map without running git.
func TestEnrichCommits_EmptyBatchSpawnsNothing(t *testing.T) {
	r := &fakeRunner{run: func(string, ...string) (string, error) {
		return "", errors.New("must not be called")
	}}
	got, err := enrichCommits(context.Background(), r, t.TempDir(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want an empty map and no error", got, err)
	}
	if len(r.calls) != 0 {
		t.Fatalf("git ran %d times, want 0: %v", len(r.calls), r.calls)
	}
}
