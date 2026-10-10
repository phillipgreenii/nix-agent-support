package internal

// Batched enrichment (pg2-sgj06). enrichCommit (one commit, two git spawns)
// stays in the package as the reference for the batch: every test here pins
// enrichCommits against it on a real repository, so the batch is proven to
// return exactly what the per-commit reads return, merge commits, root
// commits and binary files included.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingRunner delegates to the real git Runner and records every call
// (thread-safe: the per-commit fallback runs workers).
type countingRunner struct {
	inner Runner
	// hook, when set, runs before each call and may return an error to fail it.
	hook func(args []string) error

	mu       sync.Mutex
	calls    [][]string
	inflight int32
	maxSeen  int32
}

func newCountingRunner() *countingRunner { return &countingRunner{inner: NewExecRunner()} }

func (c *countingRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	c.mu.Lock()
	c.calls = append(c.calls, append([]string(nil), args...))
	c.mu.Unlock()
	n := atomic.AddInt32(&c.inflight, 1)
	defer atomic.AddInt32(&c.inflight, -1)
	for {
		m := atomic.LoadInt32(&c.maxSeen)
		if n <= m || atomic.CompareAndSwapInt32(&c.maxSeen, m, n) {
			break
		}
	}
	if c.hook != nil {
		if err := c.hook(args); err != nil {
			return "", err
		}
	}
	return c.inner.Run(ctx, dir, args...)
}

func (c *countingRunner) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

// batchFixture builds a repo with every shape the per-commit reads must be
// matched on: a root commit, a rename with edits, a binary file, a deletion,
// an empty commit, two feature branches, a plain merge, an "evil" merge whose
// own diff against its first parent adds a file, two heads on one commit, a
// tag-only commit, and an unmerged topic branch. Returns the repo and every
// commit sha reachable from a local head plus the tag-only one.
func batchFixture(f *gitFixture) (repo string, shas []string) {
	repo = f.newRepo()
	write := func(name, content string) {
		f.t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
			f.t.Fatal(err)
		}
		f.git(repo, nil, "add", name)
	}
	const day = "2026-09-05T10:00:00+00:00"
	commit := func(subject string) string { return f.commit(repo, meEmail, day, day, subject) }

	write("a.txt", "1\n2\n3\n")
	write("old.txt", "x\ny\nz\n")
	write("gone.txt", "bye\n")
	commit("root")

	f.git(repo, nil, "mv", "old.txt", "new.txt")
	write("new.txt", "x\ny\nz\nw\n")
	commit("rename-with-edit")

	write("blob.bin", "\x00\x01\x02binary\x00\xff")
	write("a.txt", "1\n2\n3\n4\n5\n")
	commit("binary-and-edit")

	f.git(repo, nil, "rm", "-q", "gone.txt")
	commit("delete")

	commit("empty")

	f.git(repo, nil, "checkout", "-b", "feat-1", "main~2")
	write("feat1.txt", "f1\nf1b\n")
	commit("feat-1-only")
	write("blob.bin", "\x00\x09\x09other\x00\xfe")
	commit("feat-1-binary-change")

	f.git(repo, nil, "checkout", "-b", "feat-2", "main~3")
	write("feat2.txt", "f2\n")
	commit("feat-2-only")

	f.git(repo, nil, "checkout", "main")
	f.git(repo, []string{"GIT_AUTHOR_DATE=" + day, "GIT_COMMITTER_DATE=" + day},
		"merge", "--no-ff", "-m", "merge-feat-1", "feat-1")
	// An evil merge: the merge commit itself carries an extra file, so its
	// first-parent diff is feat-2's content plus this one.
	f.git(repo, []string{"GIT_AUTHOR_DATE=" + day, "GIT_COMMITTER_DATE=" + day},
		"merge", "--no-ff", "--no-commit", "feat-2")
	write("evil.txt", "e1\ne2\ne3\n")
	commit("merge-feat-2-evil")

	f.git(repo, nil, "checkout", "-b", "topic", "main~1")
	write("topic.txt", "t\n")
	commit("topic-only")
	f.git(repo, nil, "branch", "same-as-topic", "topic")
	f.git(repo, nil, "checkout", "main")

	f.git(repo, nil, "checkout", "--detach", "main~1")
	write("tagged.txt", "tag\n")
	tagOnly := commit("tag-only")
	f.git(repo, nil, "tag", "keep-tag-only")
	f.git(repo, nil, "checkout", "main")

	shas = strings.Fields(f.git(repo, nil, "rev-list", "--branches"))
	shas = append(shas, tagOnly)
	sort.Strings(shas)
	return repo, shas
}

// perCommitOracle runs the reference one-commit enrichment for each sha.
func perCommitOracle(t *testing.T, repo string, shas []string) map[string]commitDetails {
	t.Helper()
	want := map[string]commitDetails{}
	for _, sha := range shas {
		d, err := enrichCommit(context.Background(), NewExecRunner(), repo, sha)
		if err != nil {
			t.Fatalf("oracle %s: %v", sha, err)
		}
		want[sha] = d
	}
	return want
}

func TestEnrichCommits_RealRepo_EqualsPerCommitOracle(t *testing.T) {
	f := newGitFixture(t)
	repo, shas := batchFixture(f)
	want := perCommitOracle(t, repo, shas)

	// The fixture must actually exercise the interesting cases, or equality
	// proves nothing.
	var sawBranch, sawNoBranch, sawZeroCounts, sawCounts bool
	for _, d := range want {
		sawBranch = sawBranch || d.branch != ""
		sawNoBranch = sawNoBranch || d.branch == ""
		sawZeroCounts = sawZeroCounts || (d.insertions == 0 && d.deletions == 0)
		sawCounts = sawCounts || d.insertions > 0 || d.deletions > 0
	}
	if !sawBranch || !sawNoBranch || !sawZeroCounts || !sawCounts {
		t.Fatalf("fixture too weak: oracle = %+v", want)
	}

	subsets := map[string][]string{
		"every commit": shas,
		"first three":  shas[:3],
		"single":       shas[len(shas)-1:],
	}
	for name, subset := range subsets {
		got, err := enrichCommits(context.Background(), NewExecRunner(), repo, subset)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		wantSub := map[string]commitDetails{}
		for _, s := range subset {
			wantSub[s] = want[s]
		}
		if !reflect.DeepEqual(got, wantSub) {
			t.Errorf("%s: batch != per-commit\n got  %+v\n want %+v", name, got, wantSub)
		}
	}
}

func TestEnrichCommits_SpawnCountDoesNotScaleWithCommits(t *testing.T) {
	f := newGitFixture(t)
	repo := f.newRepo()
	const day = "2026-09-05T10:00:00+00:00"
	var shas []string
	for i := 0; i < 60; i++ {
		if err := os.WriteFile(filepath.Join(repo, "f"), []byte(strings.Repeat("l\n", i+1)), 0o600); err != nil {
			t.Fatal(err)
		}
		f.git(repo, nil, "add", "f")
		shas = append(shas, f.commit(repo, meEmail, day, day, "c"))
	}
	f.git(repo, nil, "branch", "other", shas[30])

	small, large := newCountingRunner(), newCountingRunner()
	if _, err := enrichCommits(context.Background(), small, repo, shas[:6]); err != nil {
		t.Fatal(err)
	}
	got, err := enrichCommits(context.Background(), large, repo, shas)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 60 {
		t.Fatalf("got %d details, want 60", len(got))
	}
	if small.count() != large.count() {
		t.Errorf("spawns: 6 commits = %d, 60 commits = %d; must not scale with commit count", small.count(), large.count())
	}
	if large.count() > 4 {
		t.Errorf("spawns for 60 commits = %d (%v), want at most 4", large.count(), large.calls)
	}
}

func TestEnrichCommits_ChunksBigBatchesWithoutPerCommitSpawns(t *testing.T) {
	f := newGitFixture(t)
	repo := f.newRepo()
	const day = "2026-09-05T10:00:00+00:00"
	var shas []string
	for i := 0; i < 5; i++ {
		shas = append(shas, f.commit(repo, meEmail, day, day, "c"))
	}
	old := enrichChunk
	enrichChunk = 2
	t.Cleanup(func() { enrichChunk = old })

	r := newCountingRunner()
	got, err := enrichCommits(context.Background(), r, repo, shas)
	if err != nil {
		t.Fatal(err)
	}
	if want := perCommitOracle(t, repo, shas); !reflect.DeepEqual(got, want) {
		t.Errorf("chunked batch != oracle:\n got  %+v\n want %+v", got, want)
	}
	for _, c := range r.calls {
		if c[0] == "for-each-ref" && len(c) > 3 && c[2] == "--contains" {
			t.Errorf("chunked batch fell back to a per-commit call: %v", c)
		}
	}
}

// failBatch makes every batched read fail so the per-commit fallback runs.
func failBatch(args []string) error {
	if args[0] == "merge-base" || (args[0] == "log" && len(args) > 1 && args[1] == "--no-walk=unsorted") {
		return errors.New("simulated batch failure")
	}
	return nil
}

func TestEnrichCommits_FallbackIsBoundedAndEqualsOracle(t *testing.T) {
	f := newGitFixture(t)
	repo, shas := batchFixture(f)
	want := perCommitOracle(t, repo, shas)

	r := newCountingRunner()
	r.hook = func(args []string) error {
		time.Sleep(15 * time.Millisecond) // make overlap observable
		return failBatch(args)
	}
	got, err := enrichCommits(context.Background(), r, repo, shas)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fallback != oracle:\n got  %+v\n want %+v", got, want)
	}
	if r.maxSeen < 2 {
		t.Errorf("max in-flight = %d, fallback should overlap calls", r.maxSeen)
	}
	if r.maxSeen > int32(enrichWorkers) {
		t.Errorf("max in-flight = %d, want at most %d", r.maxSeen, enrichWorkers)
	}
}

func TestEnrichCommits_UnrelatedRootsFallBackAndStillMatch(t *testing.T) {
	f := newGitFixture(t)
	repo := f.newRepo()
	const day = "2026-09-05T10:00:00+00:00"
	a := f.commit(repo, meEmail, day, day, "main-root")
	f.git(repo, nil, "checkout", "--orphan", "island")
	b := f.commit(repo, meEmail, day, day, "island-root")
	f.git(repo, nil, "checkout", "main")
	shas := []string{a, b}
	sort.Strings(shas)

	got, err := enrichCommits(context.Background(), NewExecRunner(), repo, shas)
	if err != nil {
		t.Fatal(err)
	}
	if want := perCommitOracle(t, repo, shas); !reflect.DeepEqual(got, want) {
		t.Errorf("unrelated roots: got %+v want %+v", got, want)
	}
}

func TestEnrichCommits_OversizedRegionFallsBackAndStillMatches(t *testing.T) {
	f := newGitFixture(t)
	repo, shas := batchFixture(f)
	old := maxRegionCommits
	maxRegionCommits = 2
	t.Cleanup(func() { maxRegionCommits = old })

	got, err := enrichCommits(context.Background(), NewExecRunner(), repo, shas)
	if err != nil {
		t.Fatal(err)
	}
	if want := perCommitOracle(t, repo, shas); !reflect.DeepEqual(got, want) {
		t.Errorf("oversized region: got %+v want %+v", got, want)
	}
}

func TestEnrichCommits_CancelledContextIsAnErrorNotAFallback(t *testing.T) {
	f := newGitFixture(t)
	repo, shas := batchFixture(f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := newCountingRunner()
	if _, err := enrichCommits(ctx, r, repo, shas); err == nil {
		t.Fatal("want an error from a cancelled context")
	}
	if r.count() > 2 {
		t.Errorf("a cancelled context still issued %d git calls", r.count())
	}
}

func TestEnrichCommits_RootCommitRepoOnly(t *testing.T) {
	// A single root commit: merge-base --octopus of one commit, ^@ of a root.
	f := newGitFixture(t)
	repo := f.newRepo()
	if err := os.WriteFile(filepath.Join(repo, "r"), []byte("a\nb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.git(repo, nil, "add", "r")
	sha := f.commit(repo, meEmail, "2026-09-05T10:00:00+00:00", "2026-09-05T10:00:00+00:00", "only")
	got, err := enrichCommits(context.Background(), NewExecRunner(), repo, []string{sha})
	if err != nil {
		t.Fatal(err)
	}
	want := commitDetails{branch: "main", insertions: 2}
	if got[sha] != want {
		t.Errorf("got %+v want %+v", got[sha], want)
	}
}

// TestEnrichCommits_RealRepo_PorcelainDiffConfigDoesNotChangeCounts: diff-tree
// is plumbing and ignores the operator's porcelain diff settings, but log is
// porcelain and honors them. A real clone's config (diff.algorithm=histogram)
// moved 26 of 226 real counts by one line each before the batch pinned the
// plumbing defaults explicitly. HOME is the fixture's, which is the config
// the code under test reads.
func TestEnrichCommits_RealRepo_PorcelainDiffConfigDoesNotChangeCounts(t *testing.T) {
	f := newGitFixture(t)
	cfg := "[diff]\n\talgorithm = histogram\n\tindentHeuristic = false\n\trenames = copies\n\tignoreSubmodules = all\n"
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".gitconfig"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := f.newRepo()
	const day = "2026-09-05T10:00:00+00:00"
	// Two 30-line files over a 4-letter alphabet for which Myers and
	// histogram report different line counts.
	before := "d\nb\nd\nd\nd\na\nb\nd\nb\nc\nb\nc\nb\nc\nd\nd\nc\nc\na\nc\na\na\na\nd\na\nb\na\na\nd\na\n"
	after := "a\nd\nb\nd\nb\nc\nb\nb\nb\na\nb\na\na\nd\nc\nc\nb\na\nd\nd\nd\nc\nc\nb\nb\nd\nd\nb\na\na\n"
	write := func(content string) {
		if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		f.git(repo, nil, "add", "f.txt")
	}
	write(before)
	first := f.commit(repo, meEmail, day, day, "before")
	write(after)
	second := f.commit(repo, meEmail, day, day, "after")
	// A gitlink change that ignoreSubmodules=all would hide from log.
	f.git(repo, nil, "update-index", "--add", "--cacheinfo", "160000,"+first+",sub")
	third := f.commit(repo, meEmail, day, day, "add-submodule")
	f.git(repo, nil, "update-index", "--cacheinfo", "160000,"+second+",sub")
	fourth := f.commit(repo, meEmail, day, day, "move-submodule")
	shas := []string{first, second, third, fourth}
	sort.Strings(shas)

	want := perCommitOracle(t, repo, shas)
	got, err := enrichCommits(context.Background(), NewExecRunner(), repo, shas)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("config-sensitive counts differ:\n got  %+v\n want %+v", got, want)
	}
}
