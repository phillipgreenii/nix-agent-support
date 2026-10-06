package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
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

// enrichFixture builds a repo exercising every enrichment case:
//
//	base         every local head reaches it                  (no branch label)
//	feature-only only feature-x reaches it                    (branch:feature-x)
//	main-only    only main reaches it, +2 -1 lines            (branch:main)
//	binary-add   only bin-branch reaches it, one binary file  (branch:bin-branch)
//	detached     reachable from no local head (a tag only)    (no branch label)
func enrichFixture(f *gitFixture) string {
	repo := f.newRepo()
	write := func(name, content string) {
		f.t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
			f.t.Fatal(err)
		}
		f.git(repo, nil, "add", name)
	}
	const day = "2026-09-05T10:00:00+00:00"

	write("f", "1\n2\n3\n")
	f.commit(repo, meEmail, day, day, "base")

	f.git(repo, nil, "checkout", "-b", "feature-x")
	write("g", "a\nb\nc\n")
	f.commit(repo, meEmail, day, day, "feature-only")

	f.git(repo, nil, "checkout", "main")
	write("f", "1\n3\n4\n5\n")
	f.commit(repo, meEmail, day, day, "main-only")

	f.git(repo, nil, "checkout", "-b", "bin-branch", "main~1")
	write("blob.bin", "\x00\x01\x02binary\x00\xff")
	f.commit(repo, meEmail, day, day, "binary-add")

	f.git(repo, nil, "checkout", "--detach", "main~1")
	write("h", "x\n")
	f.commit(repo, meEmail, day, day, "detached")
	f.git(repo, nil, "tag", "keep-detached")
	f.git(repo, nil, "checkout", "main")

	// Two heads at base: a second branch name for the same commit.
	f.git(repo, nil, "branch", "two-heads", "main~1")
	return repo
}

func TestListActivity_RealRepo_BranchLabelAndLineCounts(t *testing.T) {
	f := newGitFixture(t)
	repo := enrichFixture(f)
	cfg := Config{AuthorEmails: []string{meEmail}, RepoPaths: []string{repo}}
	res := listWith(t, realBackend(&bytes.Buffer{}), cfg, time.Time{}, time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))

	type want struct {
		branch   string // "" means no branch: label at all
		ins, del int
	}
	// base is the first commit on main, so main~1 is base; every other branch
	// forks from it. base is therefore reachable from all of main, feature-x,
	// bin-branch and two-heads.
	wants := map[string]want{
		"base":         {"", 3, 0},
		"feature-only": {"feature-x", 3, 0},
		"main-only":    {"main", 2, 1},
		"binary-add":   {"bin-branch", 0, 0},
	}
	got := map[string]schema.ActivityItem{}
	for _, it := range res.Items {
		got[it.Summary] = it
	}
	// The detached commit is only reachable from a tag; --branches never lists
	// it, so it is not an item at all.
	if _, ok := got["detached"]; ok {
		t.Error("a commit reachable only from a tag must not be read")
	}
	for subject, w := range wants {
		it, ok := got[subject]
		if !ok {
			t.Errorf("%s: missing", subject)
			continue
		}
		var branches []string
		for _, l := range it.Labels {
			if v, ok := strings.CutPrefix(l, "branch:"); ok {
				branches = append(branches, v)
			}
		}
		if w.branch == "" && len(branches) != 0 {
			t.Errorf("%s: labels = %v, want no branch: label", subject, it.Labels)
		}
		if w.branch != "" && !reflect.DeepEqual(branches, []string{w.branch}) {
			t.Errorf("%s: branch labels = %v, want [%s]", subject, branches, w.branch)
		}
		if !strings.HasPrefix(it.Labels[0], "repo:") {
			t.Errorf("%s: labels = %v, want repo: first", subject, it.Labels)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(it.Fields, &raw); err != nil {
			t.Fatal(err)
		}
		var ins, del int
		if err := json.Unmarshal(raw["insertions"], &ins); err != nil {
			t.Errorf("%s: insertions %s is not an integer", subject, raw["insertions"])
		}
		if err := json.Unmarshal(raw["deletions"], &del); err != nil {
			t.Errorf("%s: deletions %s is not an integer", subject, raw["deletions"])
		}
		if ins != w.ins || del != w.del {
			t.Errorf("%s: +%d -%d, want +%d -%d", subject, ins, del, w.ins, w.del)
		}
	}
}

// TestEnrichCommit_RealRepo_LeakedGitDirStillReadsConfiguredRepo is the
// pg2-67h4y class for the calls this step adds.
func TestEnrichCommit_RealRepo_LeakedGitDirStillReadsConfiguredRepo(t *testing.T) {
	f := newGitFixture(t)
	repo := enrichFixture(f)
	decoy := f.newRepo()
	f.commit(decoy, meEmail, "2026-09-07T10:00:00+00:00", "2026-09-07T10:00:00+00:00", "decoy-commit")
	sha := f.git(repo, nil, "rev-parse", "main")

	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoy, ".git", "index"))
	t.Setenv("GIT_WORK_TREE", decoy)

	d, err := enrichCommit(context.Background(), NewExecRunner(), repo, sha)
	if err != nil {
		t.Fatal(err)
	}
	if d.branch != "main" || d.insertions != 2 || d.deletions != 1 {
		t.Errorf("details = %+v, want main +2 -1 (the configured repo, not the decoy)", d)
	}
}
