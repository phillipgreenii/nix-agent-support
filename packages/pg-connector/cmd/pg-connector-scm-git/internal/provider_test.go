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

// wtPath is the path this backend's own WorktreeAdd now builds for a given
// sanitized directory name, under its dedicated namespace
// (backendWorktreeRoot) rather than bare "/repo/.worktrees/<name>"
// [bug: this bead, review finding 22].
func wtPath(name string) string {
	return "/repo/.worktrees/" + scmGitWorktreeNamespace + "/" + name
}

func TestProvider_WorktreeAdd_Success(t *testing.T) {
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {out: "/repo/.git"},
		"worktree add -- " + wtPath("feature") + " feature": {out: ""},
		"branch --show-current":                             {out: "feature"},
	})
	p := New(r)

	info, err := p.WorktreeAdd(context.Background(), "feature")
	if err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if info.Path != wtPath("feature") || info.Branch != "feature" || info.Ref != "feature" {
		t.Fatalf("info = %+v", info)
	}
}

func TestProvider_WorktreeAdd_DetachedRef_EmptyBranch(t *testing.T) {
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir":   {out: "/repo/.git"},
		"worktree add -- " + wtPath("deadbeef") + " deadbeef": {out: ""},
		"branch --show-current":                               {out: ""},
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
	// being unhealthy (INV-ERR-2; bug pg2-r9iok) — it must not share
	// ErrUnavailable's "this backend cannot currently be used" meaning.
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

// TestProvider_WorktreeAdd_SlashBearingRef_FlattenedNotNested proves a
// slash-bearing branchOrRef (an entirely ordinary git branch name, e.g.
// "team/feature") no longer nests its worktree directory inside a sibling
// worktree's own directory via filepath.Join's path-segment semantics
// [bug: this bead, review finding 22] — the "/" is flattened into the
// directory name (though NOT into the literal git ref argument, which
// this test also pins).
func TestProvider_WorktreeAdd_SlashBearingRef_FlattenedNotNested(t *testing.T) {
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir":           {out: "/repo/.git"},
		"worktree add -- " + wtPath("team-feature") + " team/feature": {out: ""},
		"branch --show-current":                                       {out: "team/feature"},
	})
	p := New(r)

	info, err := p.WorktreeAdd(context.Background(), "team/feature")
	if err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if info.Path != wtPath("team-feature") {
		t.Fatalf("info.Path = %q, want a flattened single path component, not a nested one", info.Path)
	}
	if strings.Contains(strings.TrimPrefix(info.Path, wtPath("")), "/") {
		t.Fatalf("info.Path = %q still nests a sub-directory after the namespace root", info.Path)
	}
	// The literal git ref argument is passed through unmodified — only the
	// directory NAME is sanitized.
	if info.Ref != "team/feature" {
		t.Fatalf("info.Ref = %q, want the unmodified ref \"team/feature\"", info.Ref)
	}
}

// TestProvider_WorktreeAdd_DotDotRef_Rejected proves a branchOrRef that
// flattens to ".." (impossible to express with a "/" in it that survives
// flattening, but reachable via the literal two-character ref "..") is
// rejected before any git invocation, rather than being resolved by
// filepath.Join's own Clean-based traversal into a directory outside this
// backend's own worktree namespace [bug: this bead, review finding 22].
func TestProvider_WorktreeAdd_DotDotRef_Rejected(t *testing.T) {
	p := New(newFakeRunner(t, nil))
	_, err := p.WorktreeAdd(context.Background(), "..")
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument) with no git invocation", err)
	}
}

// TestProvider_WorktreeAdd_BadRef_NotFound proves a branchOrRef that
// doesn't resolve to any real git ref/branch/commit — `git worktree add`
// failing with "fatal: invalid reference: ..." — is now reachable as a
// well-formed not_found response (INV-ERR-2; bug pg2-r9iok),
// rather than being misreported as this backend being unhealthy
// (ErrUnavailable). Before the fix, this exact scenario was the codebase's
// own TestProvider_WorktreeAdd_GitFailure_WrapsUnavailable, which asserted
// the buggy behavior as correct.
func TestProvider_WorktreeAdd_BadRef_NotFound(t *testing.T) {
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {out: "/repo/.git"},
		"worktree add -- " + wtPath("bad") + " bad":         {err: errors.New("fatal: invalid reference: bad")},
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
		"worktree add -- " + wtPath("feature") + " feature": {err: errors.New("fatal: unable to write new_index file")},
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

// TestProvider_WorktreeList_LockedWorktree_StillReturned and its prunable/
// bare siblings below prove parseWorktreePorcelain does not drop an entry
// just because it carries a status line the parser has no dedicated field
// for [bug: this bead, review finding 34] — see parseWorktreePorcelain's
// own doc comment for why schema.WorktreeInfo gains no new field here.
func TestProvider_WorktreeList_LockedWorktree_StillReturned(t *testing.T) {
	porcelain := "worktree /repo\n" +
		"HEAD aaaaaaaa\n" +
		"branch refs/heads/main\n" +
		"\n" +
		"worktree /repo/.worktrees/pg-connector-scm-git/locked-one\n" +
		"HEAD bbbbbbbb\n" +
		"branch refs/heads/locked-one\n" +
		"locked manual lock for review\n"
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {out: "/repo/.git"},
		"worktree list --porcelain":                         {out: porcelain},
	})
	p := New(r)

	infos, err := p.WorktreeList(context.Background())
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("len(infos) = %d, want 2 (a locked entry must not be dropped): %+v", len(infos), infos)
	}
	if infos[1].Path != "/repo/.worktrees/pg-connector-scm-git/locked-one" || infos[1].Branch != "locked-one" {
		t.Fatalf("infos[1] = %+v, want the locked worktree's own path/branch preserved", infos[1])
	}
}

func TestProvider_WorktreeList_PrunableWorktree_StillReturned(t *testing.T) {
	porcelain := "worktree /repo\n" +
		"HEAD aaaaaaaa\n" +
		"branch refs/heads/main\n" +
		"\n" +
		"worktree /repo/.worktrees/pg-connector-scm-git/gone\n" +
		"HEAD bbbbbbbb\n" +
		"branch refs/heads/gone\n" +
		"prunable gitdir file points to non-existent location\n"
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {out: "/repo/.git"},
		"worktree list --porcelain":                         {out: porcelain},
	})
	p := New(r)

	infos, err := p.WorktreeList(context.Background())
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("len(infos) = %d, want 2 (a prunable entry must not be dropped): %+v", len(infos), infos)
	}
	if infos[1].Path != "/repo/.worktrees/pg-connector-scm-git/gone" || infos[1].Branch != "gone" {
		t.Fatalf("infos[1] = %+v, want the prunable worktree's own path/branch preserved", infos[1])
	}
}

func TestProvider_WorktreeList_BareMainEntry_EmptyBranchAndRef(t *testing.T) {
	// A bare repository's own pseudo-worktree entry has no HEAD/branch
	// line at all — verified empirically against real git 2.54.
	porcelain := "worktree /repo.git\n" +
		"bare\n" +
		"\n" +
		"worktree /repo.git/.worktrees/pg-connector-scm-git/feature\n" +
		"HEAD bbbbbbbb\n" +
		"branch refs/heads/feature\n"
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {out: "/repo.git"},
		"rev-parse --is-bare-repository":                    {out: "true"},
		"worktree list --porcelain":                         {out: porcelain},
	})
	p := New(r)

	infos, err := p.WorktreeList(context.Background())
	if err != nil {
		t.Fatalf("WorktreeList: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("len(infos) = %d, want 2 (the bare main entry must not be dropped): %+v", len(infos), infos)
	}
	if infos[0].Path != "/repo.git" || infos[0].Branch != "" || infos[0].Ref != "" {
		t.Fatalf("infos[0] = %+v, want the bare entry with empty Branch and Ref", infos[0])
	}
}

func worktreeRemovePorcelain(backendPath string) string {
	return "worktree /repo\n" +
		"HEAD aaaaaaaa\n" +
		"branch refs/heads/main\n" +
		"\n" +
		"worktree " + backendPath + "\n" +
		"HEAD bbbbbbbb\n" +
		"branch refs/heads/feature\n" +
		"\n" +
		// A worktree this backend did NOT create — e.g. a drain/workforest
		// worktree living directly under `.worktrees/`, sharing that
		// directory with (but never nested under) this backend's own
		// namespace [bug: this bead, review finding 22].
		"worktree /repo/.worktrees/some-bead-id\n" +
		"HEAD cccccccc\n" +
		"branch refs/heads/drain/some-bead-id\n"
}

func TestProvider_WorktreeRemove_Success(t *testing.T) {
	backendPath := wtPath("feature")
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {out: "/repo/.git"},
		"worktree list --porcelain":                         {out: worktreeRemovePorcelain(backendPath)},
		"worktree remove -- " + backendPath:                 {out: ""},
	})
	p := New(r)

	if err := p.WorktreeRemove(context.Background(), backendPath); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
}

func TestProvider_WorktreeRemove_UnknownPath_NotFound(t *testing.T) {
	// A path that isn't a known worktree is a well-formed negative answer
	// (INV-ERR-2) — checked before ever attempting `git worktree
	// remove`, so no such invocation should occur.
	backendPath := wtPath("feature")
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {out: "/repo/.git"},
		"worktree list --porcelain":                         {out: worktreeRemovePorcelain(backendPath)},
	})
	p := New(r)

	err := p.WorktreeRemove(context.Background(), wtPath("missing"))
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

// TestProvider_WorktreeRemove_NotCreatedByThisBackend_NotFound proves a
// path git DOES know about (so it would have passed the old, sole check)
// but which does not live under this backend's own worktree namespace is
// still refused as not_found, and — critically — `git worktree remove` is
// NEVER invoked for it: fakeRunner has no canned answer for that
// invocation, so calling it would fail the test loudly [bug: this bead,
// review finding 22]. This is the regression test for "worktree remove
// accepts any git-known worktree, not only ones this backend created."
func TestProvider_WorktreeRemove_NotCreatedByThisBackend_NotFound(t *testing.T) {
	backendPath := wtPath("feature")
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {out: "/repo/.git"},
		"worktree list --porcelain":                         {out: worktreeRemovePorcelain(backendPath)},
	})
	p := New(r)

	err := p.WorktreeRemove(context.Background(), "/repo/.worktrees/some-bead-id")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound) — a worktree this backend never created must be refused", err)
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
	// being unhealthy (INV-ERR-2; bug pg2-r9iok).
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

// TestProvider_BranchDetect_NotAGitRepo_NotFound proves cwd resolving to no
// git repository at all ("fatal: not a git repository (or any of the
// parent directories): .git") is now reachable as a well-formed not_found
// response (INV-ERR-2; bug pg2-r9iok) rather than being
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

// TestProvider_BranchDetect_DetachedHEAD_EmptyBranch_NoError pins detached
// HEAD as a deliberate, well-formed empty-Branch answer for BranchDetect,
// mirroring WorktreeAdd/WorktreeList's own already-tested treatment of the
// identical underlying git condition — not an accident, and not an error
// [bug: this bead, review finding 34]. See BranchDetect's own doc comment
// for why schema.BranchInfo gains no new field here.
func TestProvider_BranchDetect_DetachedHEAD_EmptyBranch_NoError(t *testing.T) {
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {out: "/repo/.git"},
		"branch --show-current":                             {out: ""},
	})
	p := New(r)

	info, err := p.BranchDetect(context.Background(), "/repo")
	if err != nil {
		t.Fatalf("BranchDetect: %v, want no error on detached HEAD", err)
	}
	if info.Branch != "" || info.Repo != "repo" {
		t.Fatalf("info = %+v, want empty Branch and Repo = \"repo\"", info)
	}
}

// TestProvider_RepoRootFor_BareRepo_RootIsCommonDirItself proves the root
// for a bare repository is the git directory itself, not its parent
// [bug: this bead, review finding 22/34] — the buggy filepath.Dir(...)
// behavior this replaces would have returned the bare repo's own PARENT
// directory instead.
func TestProvider_RepoRootFor_BareRepo_RootIsCommonDirItself(t *testing.T) {
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {out: "/srv/repos/myproject.git"},
		"rev-parse --is-bare-repository":                    {out: "true"},
		"branch --show-current":                             {out: "main"},
	})
	p := New(r)

	info, err := p.BranchDetect(context.Background(), "/srv/repos/myproject.git")
	if err != nil {
		t.Fatalf("BranchDetect: %v", err)
	}
	if info.Repo != "myproject.git" {
		t.Fatalf("info.Repo = %q, want %q (the bare repo's own directory name, not its parent)", info.Repo, "myproject.git")
	}
	// The is-bare-repository query MUST run scoped to the common dir
	// itself (via dir=commonDir), not to the caller-supplied cwd — see
	// repoRootForNonStandardLayout's own doc comment for why that
	// distinction matters when called from a linked worktree.
	found := false
	for _, c := range r.calls {
		if argsKey(c.args) == "rev-parse --is-bare-repository" && c.dir == "/srv/repos/myproject.git" {
			found = true
		}
	}
	if !found {
		t.Fatalf("calls = %+v, want the is-bare-repository query run with dir = the common dir", r.calls)
	}
}

// TestProvider_RepoRootFor_SeparateGitDir_FromMainWorktree_UsesShowToplevel
// proves the root for a repo whose MAIN worktree uses `--separate-git-dir`
// is resolved correctly (the working-tree root, not the relocated git
// directory or its parent) when called from that main worktree itself
// [bug: this bead, review finding 22/34].
func TestProvider_RepoRootFor_SeparateGitDir_FromMainWorktree_UsesShowToplevel(t *testing.T) {
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {out: "/elsewhere/actual-git-dir"},
		"rev-parse --is-bare-repository":                    {out: "false"},
		"rev-parse --path-format=absolute --git-dir":        {out: "/elsewhere/actual-git-dir"},
		"rev-parse --show-toplevel":                         {out: "/home/u/sepwork"},
		"branch --show-current":                             {out: "main"},
	})
	p := New(r)

	info, err := p.BranchDetect(context.Background(), "/home/u/sepwork")
	if err != nil {
		t.Fatalf("BranchDetect: %v", err)
	}
	if info.Repo != "sepwork" {
		t.Fatalf("info.Repo = %q, want %q", info.Repo, "sepwork")
	}
}

// TestProvider_RepoRootFor_SeparateGitDir_FromLinkedWorktree_FailsLoud
// proves that when the main worktree of a --separate-git-dir repo cannot
// be located from a LINKED worktree's own vantage point, this fails loud
// (ErrUnavailable) rather than silently returning some other, wrong,
// plausible-looking directory — the conservative choice documented on
// repoRootForNonStandardLayout, verified empirically against real git
// 2.54 to be genuinely unresolvable from there.
func TestProvider_RepoRootFor_SeparateGitDir_FromLinkedWorktree_FailsLoud(t *testing.T) {
	r := newFakeRunner(t, map[string]fakeAnswer{
		"rev-parse --path-format=absolute --git-common-dir": {out: "/elsewhere/actual-git-dir"},
		"rev-parse --is-bare-repository":                    {out: "false"},
		"rev-parse --path-format=absolute --git-dir":        {out: "/elsewhere/actual-git-dir/worktrees/linked"},
	})
	p := New(r)

	_, err := p.BranchDetect(context.Background(), "/home/u/linked")
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable) — a genuinely unresolvable root must fail loud, not guess", err)
	}
}
