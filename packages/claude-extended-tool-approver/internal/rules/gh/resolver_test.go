package gh

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/x/gitclient"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// Fixtures are built through gittest/gitfixture (bead pg2-svfbb's design,
// "gittest -- the isolated-repo fixture") rather than this file's own
// hand-rolled initTestRepo helper: the fixture is hermetic by CONSTRUCTION
// (isolated HOME, no system config, no hooks), so there is no longer a need
// for a bespoke fixture env, nor for this package's own
// hermeticGitEnviron/inheritableGitVars plumbing -- gitclient's child
// environment is built from an explicit allowlist (PATH/HOME/SSH_AUTH_SOCK)
// and never inherits GIT_DIR/GIT_WORK_TREE/etc from the test process's
// environment regardless of what it contains (bead pg2-4xfur, migrating
// pg2-2pokz's hand-rolled hermeticGitEnviron fix onto x/gitclient).

// noTestDeadline is the resolver bound used when `go test -timeout 0` leaves
// the run without any deadline of its own; it only has to be "never the
// limiting factor", not a tuned value.
const noTestDeadline = 24 * time.Hour

// newLoadTolerantResolver returns the production resolver with its per-call
// git deadline lifted to the test binary's own deadline (t.Deadline(), i.e.
// `go test -timeout`).
//
// The tests below pin WHAT the resolver answers (which branch, detached,
// outside a repo), never HOW FAST. The production default
// (defaultResolverTimeout, 3 s, shared by the resolver's two sequential git
// spawns: gitclient.Discover then CurrentBranch) is a hook-latency budget; on
// a host at load average 50+ the first git spawn alone can eat it, and the
// test then fails with "context deadline exceeded: signal: killed" even
// though the resolver is correct (pg2-lktig). The wait is therefore derived
// from an observed signal (the test run's own deadline) instead of a small
// fixed constant, and a genuinely hung git is still bounded by go test's
// -timeout. The deadline behaviour itself has its own deterministic test,
// TestExecBranchResolver_CurrentBranch_DeadlineBoundsGit.
func newLoadTolerantResolver(t *testing.T) *ExecBranchResolver {
	t.Helper()
	r := NewExecResolver()
	r.Timeout = noTestDeadline
	if d, ok := t.Deadline(); ok {
		r.Timeout = time.Until(d)
	}
	return r
}

// TestExecBranchResolver_CurrentBranch_DeadlineBoundsGit pins that the
// resolver's Timeout really bounds the git calls: an already-expired deadline
// must surface as context.DeadlineExceeded without running git, never as a
// resolved branch or a "not a repository" misreport. A negative Timeout is
// expired before the first spawn, so this is deterministic at any host load
// (no race against a short timer).
func TestExecBranchResolver_CurrentBranch_DeadlineBoundsGit(t *testing.T) {
	repo := gittest.New(t, gitfixture.RepoOptions{InitialBranch: "plain-branch"})
	if _, err := repo.Commit(t.Context(), "init", nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	r := NewExecResolver()
	r.Timeout = -time.Second

	branch, err := r.CurrentBranch(repo.Dir)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CurrentBranch(%s) = (%q, %v); want an error wrapping context.DeadlineExceeded", repo.Dir, branch, err)
	}
	if errors.Is(err, gitclient.ErrNotARepository) {
		t.Fatalf("CurrentBranch(%s) error %v misreports an expired deadline as ErrNotARepository", repo.Dir, err)
	}
}

// TestExecBranchResolver_CurrentBranch_IgnoresLeakedGitDir drives the real
// production resolver (not a stub) with GIT_DIR/GIT_WORK_TREE naming a
// DIFFERENT repository than the cwd argument -- exactly what a `git commit`
// from a linked worktree exports into every descendant process -- and
// asserts CurrentBranch resolves the directory it was HANDED, not the
// leaked one. Now proven via x/gitclient's own env allowlist rather than
// this package's former hermeticGitEnviron, which no longer exists.
func TestExecBranchResolver_CurrentBranch_IgnoresLeakedGitDir(t *testing.T) {
	target := gittest.New(t, gitfixture.RepoOptions{Suite: "target", InitialBranch: "target-branch"})
	if _, err := target.Commit(t.Context(), "init", nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	leaked := gittest.New(t, gitfixture.RepoOptions{Suite: "leaked", InitialBranch: "leaked-branch"})
	if _, err := leaked.Commit(t.Context(), "init", nil); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Simulate the leak vector: GIT_DIR/GIT_WORK_TREE set in the ambient
	// environment (e.g. by an invoking git hook) pointing at a DIFFERENT
	// repository than the one CurrentBranch is asked about.
	t.Setenv("GIT_DIR", leaked.Dir+"/.git")
	t.Setenv("GIT_WORK_TREE", leaked.Dir)

	r := newLoadTolerantResolver(t)
	branch, err := r.CurrentBranch(target.Dir)
	if err != nil {
		t.Fatalf("CurrentBranch(%s) error: %v", target.Dir, err)
	}
	if branch != "target-branch" {
		t.Fatalf("CurrentBranch(%s) = %q; want %q -- a leaked GIT_DIR/GIT_WORK_TREE silently overrode target %s", target.Dir, branch, "target-branch", target.Dir)
	}
}

// TestExecBranchResolver_CurrentBranch_StillWorksUnleaked pins the ordinary,
// no-leak path so the fix cannot be "pass by never resolving anything".
func TestExecBranchResolver_CurrentBranch_StillWorksUnleaked(t *testing.T) {
	repo := gittest.New(t, gitfixture.RepoOptions{InitialBranch: "plain-branch"})
	if _, err := repo.Commit(t.Context(), "init", nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	r := newLoadTolerantResolver(t)
	branch, err := r.CurrentBranch(repo.Dir)
	if err != nil {
		t.Fatalf("CurrentBranch(%s) error: %v", repo.Dir, err)
	}
	if branch != "plain-branch" {
		t.Fatalf("CurrentBranch(%s) = %q; want %q", repo.Dir, branch, "plain-branch")
	}
}

// TestExecBranchResolver_CurrentBranch_DetachedHEAD pins DECISION 1 recorded
// on CurrentBranch's doc comment: on a detached checkout, gitclient reports
// the typed gitclient.ErrDetachedHEAD, but this resolver maps that back onto
// the literal string "HEAD" the pre-migration raw `rev-parse --abbrev-ref
// HEAD` call used to return, so gh.go's `gh run rerun` gating is unchanged.
func TestExecBranchResolver_CurrentBranch_DetachedHEAD(t *testing.T) {
	repo := gittest.New(t, gitfixture.RepoOptions{InitialBranch: "main"})
	if _, err := repo.Commit(t.Context(), "init", nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := repo.Client.Run(t.Context(), "checkout", "--detach", "HEAD"); err != nil {
		t.Fatalf("checkout --detach: %v", err)
	}

	r := newLoadTolerantResolver(t)
	branch, err := r.CurrentBranch(repo.Dir)
	if err != nil {
		t.Fatalf("CurrentBranch(%s) on detached HEAD: unexpected error %v (want the literal \"HEAD\", not an error)", repo.Dir, err)
	}
	if branch != "HEAD" {
		t.Fatalf("CurrentBranch(%s) on detached HEAD = %q; want the literal %q", repo.Dir, branch, "HEAD")
	}
}

// TestExecBranchResolver_CurrentBranch_OutsideRepo pins the ErrNotARepository
// path through Discover: a cwd outside any git work tree is a genuine error,
// never the empty string.
func TestExecBranchResolver_CurrentBranch_OutsideRepo(t *testing.T) {
	dir := t.TempDir()
	r := newLoadTolerantResolver(t)
	_, err := r.CurrentBranch(dir)
	if !errors.Is(err, gitclient.ErrNotARepository) {
		t.Fatalf("CurrentBranch(%s) error = %v; want errors.Is(_, gitclient.ErrNotARepository)", dir, err)
	}
}
