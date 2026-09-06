package internal

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeRunner is a mock Runner: each call records the (dir, args) it was
// invoked with and answers from a table keyed by the joined args, so tests
// can assert exactly which git invocation each Provider method issued
// without spawning a real git subprocess.
type fakeRunner struct {
	calls []fakeCall
	// answers maps a joined-args key (see argsKey) to a canned (out, err)
	// pair. A missing key is a test bug (unexpected git invocation) and
	// fails loudly rather than silently returning empty output.
	answers map[string]fakeAnswer
	t       *testing.T
}

type fakeCall struct {
	dir  string
	args []string
}

type fakeAnswer struct {
	out string
	err error
}

func argsKey(args []string) string { return strings.Join(args, " ") }

func newFakeRunner(t *testing.T, answers map[string]fakeAnswer) *fakeRunner {
	t.Helper()
	return &fakeRunner{answers: answers, t: t}
}

func (f *fakeRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	f.calls = append(f.calls, fakeCall{dir: dir, args: args})
	key := argsKey(args)
	ans, ok := f.answers[key]
	if !ok {
		f.t.Fatalf("fakeRunner: unexpected git invocation %v (dir=%q)", args, dir)
	}
	return ans.out, ans.err
}

func TestProvider_WorktreeAdd_Success(t *testing.T) {
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {out: "/repo/.git"},
		"worktree add /repo/.worktrees/feature feature":     {out: ""},
		"branch --show-current":                             {out: "feature"},
	})
	p := New(r)

	info, err := p.WorktreeAdd(context.Background(), "feature")
	if err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if info.Path != "/repo/.worktrees/feature" || info.Branch != "feature" || info.Ref != "feature" {
		t.Fatalf("info = %+v", info)
	}
}

func TestProvider_WorktreeAdd_DetachedRef_EmptyBranch(t *testing.T) {
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {out: "/repo/.git"},
		"worktree add /repo/.worktrees/deadbeef deadbeef":   {out: ""},
		"branch --show-current":                             {out: ""},
	})
	p := New(r)

	info, err := p.WorktreeAdd(context.Background(), "deadbeef")
	if err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if info.Branch != "" || info.Ref != "deadbeef" {
		t.Fatalf("info = %+v, want empty Branch (detached) and Ref = deadbeef", info)
	}
}

func TestProvider_WorktreeAdd_EmptyBranchOrRef_Rejected(t *testing.T) {
	p := New(newFakeRunner(t, nil))
	_, err := p.WorktreeAdd(context.Background(), "")
	if err == nil {
		t.Fatal("WorktreeAdd(\"\") = nil error, want a rejection with no git invocation")
	}
	// An empty required field is the CALLER's mistake, not this backend
	// being unhealthy [design: §4.2, bug pg2-r9iok] — it must not share
	// ErrUnavailable's "this backend cannot currently be used" meaning.
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

// TestProvider_WorktreeAdd_BadRef_NotFound proves a branchOrRef that
// doesn't resolve to any real git ref/branch/commit — `git worktree add`
// failing with "fatal: invalid reference: ..." — is now reachable as a
// well-formed not_found response [design: §4.5, §4.7, bug pg2-r9iok],
// rather than being misreported as this backend being unhealthy
// (ErrUnavailable). Before the fix, this exact scenario was the codebase's
// own TestProvider_WorktreeAdd_GitFailure_WrapsUnavailable, which asserted
// the buggy behavior as correct.
func TestProvider_WorktreeAdd_BadRef_NotFound(t *testing.T) {
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {out: "/repo/.git"},
		"worktree add /repo/.worktrees/bad bad":             {err: errors.New("fatal: invalid reference: bad")},
	})
	p := New(r)

	_, err := p.WorktreeAdd(context.Background(), "bad")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
	if errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, must NOT also be ErrUnavailable — a not_found answer must not share a code with a failure", err)
	}
}

// TestProvider_WorktreeAdd_GenuineGitFailure_StaysUnavailable proves a real
// git-exec failure unrelated to a bad ref (nothing in its message matches
// isGitNotFound's patterns) still classifies as ErrUnavailable — the fix
// narrows the not_found detection to genuine "no such ref" phrasing, it
// does not turn every `git worktree add` failure into not_found.
func TestProvider_WorktreeAdd_GenuineGitFailure_StaysUnavailable(t *testing.T) {
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {out: "/repo/.git"},
		"worktree add /repo/.worktrees/feature feature":     {err: errors.New("fatal: unable to write new_index file")},
	})
	p := New(r)

	_, err := p.WorktreeAdd(context.Background(), "feature")
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
}

const porcelainTwoWorktrees = "worktree /repo\n" +
	"HEAD aaaaaaaa\n" +
	"branch refs/heads/main\n" +
	"\n" +
	"worktree /repo/.worktrees/feature\n" +
	"HEAD bbbbbbbb\n" +
	"branch refs/heads/feature\n"

func TestProvider_WorktreeList_Success(t *testing.T) {
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {out: "/repo/.git"},
		"worktree list --porcelain":                         {out: porcelainTwoWorktrees},
	})
	p := New(r)

	infos, err := p.WorktreeList(context.Background())
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("len(infos) = %d, want 2 (%+v)", len(infos), infos)
	}
	if infos[0].Path != "/repo" || infos[0].Branch != "main" || infos[0].Ref != "main" {
		t.Fatalf("infos[0] = %+v", infos[0])
	}
	if infos[1].Path != "/repo/.worktrees/feature" || infos[1].Branch != "feature" || infos[1].Ref != "feature" {
		t.Fatalf("infos[1] = %+v", infos[1])
	}
}

func TestProvider_WorktreeList_DetachedHEAD_EmptyBranch(t *testing.T) {
	porcelain := "worktree /repo/.worktrees/detached\n" +
		"HEAD cccccccc\n" +
		"detached\n"
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {out: "/repo/.git"},
		"worktree list --porcelain":                         {out: porcelain},
	})
	p := New(r)

	infos, err := p.WorktreeList(context.Background())
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}
	if len(infos) != 1 || infos[0].Branch != "" || infos[0].Ref != "cccccccc" {
		t.Fatalf("infos = %+v, want one detached entry with empty Branch and sha Ref", infos)
	}
}

func TestProvider_WorktreeRemove_Success(t *testing.T) {
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {out: "/repo/.git"},
		"worktree list --porcelain":                         {out: porcelainTwoWorktrees},
		"worktree remove /repo/.worktrees/feature":          {out: ""},
	})
	p := New(r)

	if err := p.WorktreeRemove(context.Background(), "/repo/.worktrees/feature"); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
}

func TestProvider_WorktreeRemove_UnknownPath_NotFound(t *testing.T) {
	// A path that isn't a known worktree is a well-formed negative answer
	// [design: §4.5, §4.7] — checked before ever attempting `git worktree
	// remove`, so no such invocation should occur.
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {out: "/repo/.git"},
		"worktree list --porcelain":                         {out: porcelainTwoWorktrees},
	})
	p := New(r)

	err := p.WorktreeRemove(context.Background(), "/repo/.worktrees/missing")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

func TestProvider_WorktreeRemove_EmptyPath_Rejected(t *testing.T) {
	p := New(newFakeRunner(t, nil))
	err := p.WorktreeRemove(context.Background(), "")
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestProvider_BranchDetect_Success(t *testing.T) {
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {out: "/home/u/repo/.git"},
		"branch --show-current":                             {out: "feature"},
	})
	p := New(r)

	info, err := p.BranchDetect(context.Background(), "/home/u/repo/subdir")
	if err != nil {
		t.Fatalf("BranchDetect: %v", err)
	}
	if info.Repo != "repo" || info.Branch != "feature" {
		t.Fatalf("info = %+v", info)
	}
	if len(r.calls) != 2 || r.calls[0].dir != "/home/u/repo/subdir" || r.calls[1].dir != "/home/u/repo/subdir" {
		t.Fatalf("calls = %+v, want both git invocations run with dir = the given cwd", r.calls)
	}
}

func TestProvider_BranchDetect_EmptyCwd_Rejected(t *testing.T) {
	p := New(newFakeRunner(t, nil))
	_, err := p.BranchDetect(context.Background(), "")
	if err == nil {
		t.Fatal("BranchDetect(\"\") = nil error, want a rejection with no git invocation")
	}
	// An empty required field is the CALLER's mistake, not this backend
	// being unhealthy [design: §4.2, bug pg2-r9iok].
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

// TestProvider_BranchDetect_NotAGitRepo_NotFound proves cwd resolving to no
// git repository at all ("fatal: not a git repository (or any of the
// parent directories): .git") is now reachable as a well-formed not_found
// response [design: §4.5, §4.7, bug pg2-r9iok] rather than being
// misreported as this backend being unhealthy. Before the fix, this exact
// scenario was the codebase's own
// TestProvider_BranchDetect_NotAGitRepo_WrapsUnavailable, which asserted
// the buggy behavior as correct.
func TestProvider_BranchDetect_NotAGitRepo_NotFound(t *testing.T) {
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {err: errors.New("fatal: not a git repository (or any of the parent directories): .git")},
	})
	p := New(r)

	_, err := p.BranchDetect(context.Background(), "/tmp/not-a-repo")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
	if errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, must NOT also be ErrUnavailable — a not_found answer must not share a code with a failure", err)
	}
}

// TestProvider_BranchDetect_MissingDir_NotFound covers the sibling
// isGitNotFound phrasing (a cwd that doesn't exist as a directory at all —
// "fatal: cannot change to '<dir>': No such file or directory") through
// the same repoRootFor path BranchDetect shares with
// WorktreeAdd/Remove/List.
func TestProvider_BranchDetect_MissingDir_NotFound(t *testing.T) {
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {err: errors.New("fatal: cannot change to '/nonexistent/path': No such file or directory")},
	})
	p := New(r)

	_, err := p.BranchDetect(context.Background(), "/nonexistent/path")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

// TestProvider_BranchDetect_GenuineGitFailure_StaysUnavailable proves a
// real git-exec failure whose message matches neither isGitNotFound
// pattern still classifies as ErrUnavailable.
func TestProvider_BranchDetect_GenuineGitFailure_StaysUnavailable(t *testing.T) {
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {err: errors.New("fatal: unable to read current working directory: Permission denied")},
	})
	p := New(r)

	_, err := p.BranchDetect(context.Background(), "/tmp/some-repo")
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
}
