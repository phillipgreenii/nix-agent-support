// provider.go: Provider implements pkg/provider/scm.Provider against real
// local git plumbing — worktrees and cwd->branch resolution, no remote
// sync concept [design: §4.7]. It deliberately does NOT implement
// pkg/provider.AuthChecker: local git has no remote credentials concept at
// all [design: §4.6, §4.7] — see this backend's own main.go for how that
// absence surfaces as "disabled: not applicable" through pg-connector's
// generic auth_status fan-out, with no special-casing needed here.
//
// Nothing in this package is exported outside cmd/pg-connector-scm-git: per
// this module's own layout convention (cmd/pg-connector's
// TestBackendLayoutConvention), a backend's own code lives in main or its
// own internal/ — nothing it exports is importable by any other backend
// [design: §5.2].
package internal

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/scm"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Provider is the concrete pkg/provider/scm.Provider backing this binary.
type Provider struct {
	runner Runner
}

// Compile-time assertion that Provider implements every method of
// pkg/provider/scm.Provider — mirrors the sibling packet's own `var _
// Provider = (*fakeProvider)(nil)` pattern in dispatch_test.go.
var _ scm.Provider = (*Provider)(nil)

// New returns a Provider backed by runner.
func New(runner Runner) *Provider {
	return &Provider{runner: runner}
}

// repoRootFor resolves the repository root for the git working copy
// containing dir (an empty dir inherits this backend process's own
// working directory — how WorktreeAdd/WorktreeRemove/WorktreeList resolve
// "the current repository", since none of those three carry a repo/cwd
// wire argument of their own). It goes via `git rev-parse
// --path-format=absolute --git-common-dir` rather than `--show-toplevel`
// deliberately: --show-toplevel reports the CALLING worktree's own
// directory, which differs for every linked worktree of the same repo,
// whereas --git-common-dir resolves to the one shared .git directory
// every worktree of that repo has in common (verified empirically against
// real git 2.54: from inside a linked worktree, --show-toplevel returns
// that worktree's own path while --git-common-dir still returns the
// shared .git). Resolving via the shared common-dir means a caller
// running any of these ops from inside an existing linked worktree still
// gets the one true repo root — e.g. a new worktree_add lands under the
// MAIN repo's own .worktrees/, never nested under whichever worktree
// happened to be dir at the time.
// repoRootFor classifies a `rev-parse --git-common-dir` failure as
// not_found when it's a definitive "there is no repository here" answer —
// dir doesn't exist at all (`fatal: cannot change to '<dir>': No such file
// or directory`) or dir exists but isn't inside any git repo (`fatal: not
// a git repository (or any of the parent directories): .git`), both
// verified empirically against real git 2.54 — rather than the previous
// unconditional ErrUnavailable, which conflated "no such repo" with a
// genuine backend health problem [design: §4.5, bug pg2-r9iok].
func (p *Provider) repoRootFor(ctx context.Context, dir string) (string, error) {
	commonDir, err := p.runner.Run(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		if isGitNotFound(err) {
			return "", scriptout.WrapError(scriptout.ErrNotFound, "resolve git repository: "+err.Error())
		}
		return "", scriptout.WrapError(scriptout.ErrUnavailable, "resolve git repository: "+err.Error())
	}
	return filepath.Dir(commonDir), nil
}

// isGitNotFound reports whether err's message carries one of the two error
// phrasings verified empirically against real git 2.54 for "there is no
// such repository/ref here" (as opposed to a genuine backend/exec
// failure): a missing directory (`fatal: cannot change to '<dir>': No such
// file or directory`), a directory that exists but isn't a git repo
// (`fatal: not a git repository (or any of the parent directories):
// .git`), or an unresolvable ref/branch passed to `git worktree add`
// (`fatal: invalid reference: <ref>`) [bug pg2-r9iok].
func isGitNotFound(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "not a git repository") ||
		strings.Contains(msg, "invalid reference")
}

// currentRepoRoot is repoRootFor with an empty dir — this backend
// process's own working directory. BranchDetect is the one op that
// instead receives an explicit cwd over the wire and calls repoRootFor
// directly.
func (p *Provider) currentRepoRoot(ctx context.Context) (string, error) {
	return p.repoRootFor(ctx, "")
}

// WorktreeAdd execs `git worktree add <path> <branchOrRef>` under the
// current repo's `.worktrees/` directory (this workspace's own existing
// worktree-root convention, elsewhere applied per-bead-id rather than
// per-branch; the exact path choice is a stated freedom boundary for this
// packet — it does not need to match, and is not required to match, any
// other tool's own worktree-path convention [design: §4.7]).
func (p *Provider) WorktreeAdd(ctx context.Context, branchOrRef string) (*schema.WorktreeInfo, error) {
	if branchOrRef == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "worktree_add: branch_or_ref is required")
	}
	repoRoot, err := p.currentRepoRoot(ctx)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(repoRoot, ".worktrees", branchOrRef)
	if _, err := p.runner.Run(ctx, repoRoot, "worktree", "add", path, branchOrRef); err != nil {
		// branchOrRef not resolving to any real ref/branch/commit (`fatal:
		// invalid reference: ...`) is a well-formed not_found answer, not a
		// broken call [design: §4.5, §4.7, bug pg2-r9iok] — the same
		// distinction WorktreeRemove already draws below for a path that
		// isn't a known worktree.
		if isGitNotFound(err) {
			return nil, scriptout.WrapError(scriptout.ErrNotFound, "git worktree add: "+err.Error())
		}
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "git worktree add: "+err.Error())
	}
	// Best-effort: a branch-or-ref that resolves to a detached HEAD (a
	// tag, a sha, a non-branch ref) leaves this empty — `branch
	// --show-current` prints nothing on detached HEAD, which is a
	// well-formed empty result here, not an error worth failing the call
	// over (the worktree itself was already created successfully above).
	branch, _ := p.runner.Run(ctx, path, "branch", "--show-current")
	return &schema.WorktreeInfo{Path: path, Branch: branch, Ref: branchOrRef}, nil
}

// WorktreeRemove execs `git worktree remove <path>`. A path that is not a
// worktree this repo currently knows about is a well-formed not_found
// answer, not a broken call [design: §4.5, §4.7] — checked against `git
// worktree list --porcelain` first so a bad path never even reaches a real
// `git worktree remove` invocation.
func (p *Provider) WorktreeRemove(ctx context.Context, path string) error {
	if path == "" {
		return scriptout.WrapError(scriptout.ErrInvalidArgument, "worktree_remove: path is required")
	}
	repoRoot, err := p.currentRepoRoot(ctx)
	if err != nil {
		return err
	}
	worktrees, err := p.listWorktrees(ctx, repoRoot)
	if err != nil {
		return err
	}
	known := false
	for _, w := range worktrees {
		if w.Path == path {
			known = true
			break
		}
	}
	if !known {
		return scriptout.WrapError(scriptout.ErrNotFound, fmt.Sprintf("worktree %s not found", path))
	}
	if _, err := p.runner.Run(ctx, repoRoot, "worktree", "remove", path); err != nil {
		return scriptout.WrapError(scriptout.ErrUnavailable, "git worktree remove: "+err.Error())
	}
	return nil
}

// WorktreeList execs `git worktree list --porcelain` and parses it into
// []schema.WorktreeInfo. No existing Go git-porcelain-parsing helper in
// this repo is importable here (packages/pb/internal/drain's own
// worktreeBranches parses the identical porcelain format but is a
// different Go module's unexported helper — confirmed by search), so this
// packet's own freedom boundary is exercised by hand-rolling the parse
// below rather than introducing a new shared dependency for it.
func (p *Provider) WorktreeList(ctx context.Context) ([]schema.WorktreeInfo, error) {
	repoRoot, err := p.currentRepoRoot(ctx)
	if err != nil {
		return nil, err
	}
	return p.listWorktrees(ctx, repoRoot)
}

func (p *Provider) listWorktrees(ctx context.Context, repoRoot string) ([]schema.WorktreeInfo, error) {
	out, err := p.runner.Run(ctx, repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "git worktree list: "+err.Error())
	}
	return parseWorktreePorcelain(out), nil
}

// parseWorktreePorcelain parses `git worktree list --porcelain` output
// (blocks of "worktree <path>" / "HEAD <sha>" / then either "branch
// refs/heads/<name>" or the bare word "detached", separated by blank
// lines) into []schema.WorktreeInfo. A detached-HEAD worktree has no
// branch line, so it comes back with an empty Branch and its checked-out
// commit sha as Ref — a deliberate, honest distinction from a real branch
// checkout, not an omission.
func parseWorktreePorcelain(out string) []schema.WorktreeInfo {
	var infos []schema.WorktreeInfo
	var cur *schema.WorktreeInfo
	flush := func() {
		if cur != nil {
			infos = append(infos, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = &schema.WorktreeInfo{Path: strings.TrimPrefix(line, "worktree ")}
		case cur == nil:
			// Stray line before any "worktree " header — ignore.
		case strings.HasPrefix(line, "branch "):
			branch := strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
			cur.Branch = branch
			cur.Ref = branch
		case strings.HasPrefix(line, "HEAD ") && cur.Ref == "":
			// Tentative Ref for a possibly-detached worktree; a following
			// "branch " line (case above) overrides it with the branch
			// name. Left as-is (the commit sha) when no branch line
			// follows, i.e. genuinely detached HEAD.
			cur.Ref = strings.TrimPrefix(line, "HEAD ")
		case line == "":
			flush()
		}
	}
	flush()
	return infos
}

// BranchDetect resolves cwd to its repo and currently checked-out branch
// [design: §4.7]. Unlike WorktreeAdd/Remove/List, it receives cwd as an
// explicit wire argument rather than resolving it from this process's own
// working directory.
//
// Repo is the repo root's basename (via repoRootFor, so it is the same
// value regardless of which one of the repo's own worktrees cwd happens
// to sit inside) — purely local git state, with no assumption of any
// particular remote/hosting convention: scm has no remote-sync concept at
// all [design: §4.7], so deriving Repo from a remote's URL (as pg-pr's
// own GitHub-flavored `branch detect` does) would reintroduce exactly the
// remote-awareness this capability is designed without.
func (p *Provider) BranchDetect(ctx context.Context, cwd string) (*schema.BranchInfo, error) {
	if cwd == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "branch_detect: cwd is required")
	}
	root, err := p.repoRootFor(ctx, cwd)
	if err != nil {
		return nil, err
	}
	branch, err := p.runner.Run(ctx, cwd, "branch", "--show-current")
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "resolve current branch for cwd "+cwd+": "+err.Error())
	}
	return &schema.BranchInfo{Repo: filepath.Base(root), Branch: branch}, nil
}
