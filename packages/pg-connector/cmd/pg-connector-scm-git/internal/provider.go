// provider.go: Provider implements pkg/provider/scm.Provider against real
// local git plumbing — worktrees and cwd->branch resolution, no remote
// sync concept (GOAL-MIN-1). It deliberately does NOT implement
// pkg/provider.AuthChecker: local git has no remote credentials concept at
// all (INV-AUTH-1) — see this backend's own main.go for how that
// absence surfaces as "disabled: not applicable" through pg-connector's
// generic auth_status fan-out, with no special-casing needed here.
//
// Nothing in this package is exported outside cmd/pg-connector-scm-git: per
// this module's own layout convention (cmd/pg-connector's
// TestBackendLayoutConvention), a backend's own code lives in main or its
// own internal/ — nothing it exports is importable by any other backend
// (layout_convention_test.go).
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
//
// The standard layout (a `.git` entry directly under the root) is the fast
// path: filepath.Base(commonDir) == ".git" and root is just
// filepath.Dir(commonDir), with no further git calls, exactly as before.
// A bare repository or a repository whose MAIN worktree was created with
// `--separate-git-dir` does NOT have that layout at all — commonDir is
// either the bare repo's own directory (any name) or an unrelated
// relocated path — so Dir(commonDir) silently computed the wrong root for
// both [bug: this bead, review finding 22/34]. Both are handled by
// repoRootForNonStandardLayout, which
// is reached only for that rarer layout so the extra git round-trips are
// never paid on the common path.
//
// repoRootFor classifies a `rev-parse --git-common-dir` failure as
// not_found when it's a definitive "there is no repository here" answer —
// dir doesn't exist at all (`fatal: cannot change to '<dir>': No such file
// or directory`) or dir exists but isn't inside any git repo (`fatal: not
// a git repository (or any of the parent directories): .git`), both
// verified empirically against real git 2.54 — rather than the previous
// unconditional ErrUnavailable, which conflated "no such repo" with a
// genuine backend health problem (INV-ERR-2; bug pg2-r9iok).
func (p *Provider) repoRootFor(ctx context.Context, dir string) (string, error) {
	commonDir, err := p.runner.Run(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		if isGitNotFound(err) {
			return "", scriptout.WrapError(scriptout.ErrNotFound, "resolve git repository: "+err.Error())
		}
		return "", scriptout.WrapError(scriptout.ErrUnavailable, "resolve git repository: "+err.Error())
	}
	if filepath.Base(commonDir) == ".git" {
		return filepath.Dir(commonDir), nil
	}
	return p.repoRootForNonStandardLayout(ctx, dir, commonDir)
}

// repoRootForNonStandardLayout resolves the repo root when --git-common-dir
// does not look like a standard "<root>/.git" entry — either a BARE
// repository (no working tree at all; commonDir already IS the root,
// under whatever name the operator gave that directory) or a repository
// whose MAIN worktree was created with `git init/clone --separate-git-dir`
// (the git directory can live anywhere, with no path relationship to the
// working tree at all) [bug: this bead, review finding 22/34].
func (p *Provider) repoRootForNonStandardLayout(ctx context.Context, dir, commonDir string) (string, error) {
	// Ask bare-ness of commonDir itself via -C (bypassing dir's own
	// working-copy context entirely), not of dir: dir may be a perfectly
	// ordinary, non-bare LINKED worktree of a repo whose shared common dir
	// is itself bare. Verified empirically against real git 2.54: `git
	// rev-parse --is-bare-repository` run from inside such a linked
	// worktree answers "false" — about that worktree, not the shared
	// common dir it belongs to — while the same query run with `-C
	// <commonDir>` correctly answers "true".
	isBare, err := p.runner.Run(ctx, commonDir, "rev-parse", "--is-bare-repository")
	if err != nil {
		return "", scriptout.WrapError(scriptout.ErrUnavailable, "resolve git repository: "+err.Error())
	}
	if strings.TrimSpace(isBare) == "true" {
		return commonDir, nil
	}
	// Non-bare with a relocated git directory: --git-common-dir cannot be
	// mapped back to the working tree's root by path arithmetic at all.
	// --show-toplevel CAN answer it correctly, but only when dir sits
	// inside the repo's own MAIN worktree — from a LINKED worktree of a
	// --separate-git-dir main, git's own worktree bookkeeping has no path
	// back to the main worktree's real location (verified empirically:
	// even `git worktree list --porcelain`'s own main-worktree entry
	// misreports the git directory's own path as if it were the worktree
	// path, in that exact configuration — there is no git command that
	// recovers the real answer from there). Detect "dir is the main
	// worktree" by comparing dir's own --git-dir against commonDir: equal
	// only for the main worktree, never for a linked one.
	gitDir, err := p.runner.Run(ctx, dir, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return "", scriptout.WrapError(scriptout.ErrUnavailable, "resolve git repository: "+err.Error())
	}
	if gitDir != commonDir {
		// Conservative, deliberate choice: fail loud rather than guess.
		// filepath.Dir(commonDir) (the old behavior) or --show-toplevel
		// from dir would both return a plausible-looking directory here —
		// neither is the actual main-worktree root, and there is no git
		// command available from this vantage point that is.
		return "", scriptout.WrapError(scriptout.ErrUnavailable, "resolve git repository: cannot determine the main worktree's root for a --separate-git-dir repository from a linked worktree")
	}
	top, err := p.runner.Run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", scriptout.WrapError(scriptout.ErrUnavailable, "resolve git repository: "+err.Error())
	}
	return top, nil
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

// resolvePathForCompare returns the best available normalized form of p
// for comparing worktree paths: filepath.Clean, then filepath.Abs (a
// worktree path is always already absolute in practice, but Abs is cheap
// insurance), then filepath.EvalSymlinks — resolving through, e.g.,
// macOS's own /tmp -> /private/tmp symlink, so a caller-supplied path that
// differs from git's own porcelain-reported path only by an unresolved
// symlink component still compares equal [bug: this bead, review finding
// 22 — this exact packet's own real-git test (provider_realgit_test.go's
// newRealGitFixture) masks this precise gap by pre-resolving symlinks by
// hand before ever calling this Provider]. A path that does not currently
// exist on disk (e.g. a prunable worktree whose directory was removed out
// from under git) cannot be resolved through EvalSymlinks at all — that
// failure is not itself a reason to abandon the comparison, so this falls
// back to the Clean+Abs form for exactly that case.
func resolvePathForCompare(p string) string {
	cleaned := filepath.Clean(p)
	if abs, err := filepath.Abs(cleaned); err == nil {
		cleaned = abs
	}
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		return resolved
	}
	return cleaned
}

// currentRepoRoot is repoRootFor with an empty dir — this backend
// process's own working directory. BranchDetect is the one op that
// instead receives an explicit cwd over the wire and calls repoRootFor
// directly.
func (p *Provider) currentRepoRoot(ctx context.Context) (string, error) {
	return p.repoRootFor(ctx, "")
}

// scmGitWorktreeNamespace is the dedicated subdirectory this backend
// nests every worktree it creates under, inside the repo's own
// `.worktrees/` directory: `<root>/.worktrees/pg-connector-scm-git/<name>`,
// never bare `<root>/.worktrees/<name>` [bug: this bead, review finding
// 22]. Two independent reasons converge on the same fix:
//   - This exact workspace ALSO uses `<repo>/.worktrees/<bead-id>` as its
//     own, unrelated drain/workforest worktree convention — a
//     branch/ref that happens to collide with an active bead id (or any
//     other tool's own bare `.worktrees/<name>` entry) would otherwise
//     land at the identical path.
//   - It gives WorktreeRemove a structural way to recognize "a worktree
//     THIS backend created" (see its own doc comment) without inventing
//     any new persistent state: only paths under this one namespace were
//     ever created by WorktreeAdd.
const scmGitWorktreeNamespace = "pg-connector-scm-git"

// backendWorktreeRoot is the directory every worktree this backend creates
// lives directly under.
func backendWorktreeRoot(repoRoot string) string {
	return filepath.Join(repoRoot, ".worktrees", scmGitWorktreeNamespace)
}

// sanitizeWorktreeDirName turns branchOrRef into a single, safe filesystem
// path component for naming the directory `git worktree add` checks
// branchOrRef out into — used ONLY for that directory name; the
// unmodified branchOrRef is still the literal git ref argument handed to
// git itself below. A slash-bearing ref (an entirely ordinary git branch
// name, e.g. "team/feature") would otherwise nest one worktree's
// directory inside another's via filepath.Join's own path-segment
// semantics [bug: this bead, review finding 22], so every "/" is
// flattened to "-", guaranteeing the result is always exactly one path
// component. A branchOrRef that, after flattening, is empty, ".", or ".."
// is rejected outright rather than silently resolved by filepath.Join's
// own Clean-based traversal — git's own ref-name rules already forbid all
// three as real refs, but this function must be safe on its own,
// independent of git ever seeing the value.
func sanitizeWorktreeDirName(branchOrRef string) (string, error) {
	safe := strings.ReplaceAll(branchOrRef, "/", "-")
	switch safe {
	case "", ".", "..":
		return "", fmt.Errorf("branch_or_ref %q does not yield a safe worktree directory name", branchOrRef)
	}
	return safe, nil
}

// WorktreeAdd execs `git worktree add -- <path> <branchOrRef>` under
// backendWorktreeRoot (this backend's own dedicated namespace inside the
// repo's `.worktrees/` directory — the exact path choice beneath
// `.worktrees/` is a stated freedom boundary for this packet — it does
// not need to match, and is not required to match, any other tool's own
// worktree-path convention). The `--` terminator guards
// against a branchOrRef that happens to look like a git flag (e.g.
// "--upload-pack=...") being parsed as one instead of as a literal ref
// [bug: this bead, review finding 22 — mirrors the sibling
// pg-connector-issue-beads backend's own already-fixed "--" convention,
// bead pg2-usu5b]; verified empirically against real git 2.54 that `git
// worktree add -- <path> <ref>` behaves identically to the unescaped form
// for a well-formed ref, and that a dash-prefixed ref after `--` correctly
// fails as `invalid reference` (a well-formed not_found answer below)
// rather than being swallowed as a flag.
func (p *Provider) WorktreeAdd(ctx context.Context, branchOrRef string) (*schema.WorktreeInfo, error) {
	if branchOrRef == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "worktree_add: branch_or_ref is required")
	}
	dirName, err := sanitizeWorktreeDirName(branchOrRef)
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "worktree_add: "+err.Error())
	}
	repoRoot, err := p.currentRepoRoot(ctx)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(backendWorktreeRoot(repoRoot), dirName)
	if _, err := p.runner.Run(ctx, repoRoot, "worktree", "add", "--", path, branchOrRef); err != nil {
		// branchOrRef not resolving to any real ref/branch/commit (`fatal:
		// invalid reference: ...`) is a well-formed not_found answer, not a
		// broken call (INV-ERR-2; bug pg2-r9iok) — the same
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

// WorktreeRemove execs `git worktree remove -- <path>`. Two independent
// conditions must hold before this ever calls real git, and BOTH answers
// are reported to the caller as the SAME well-formed not_found response
// (this op's own established "not a known worktree" shape; INV-ERR-2)
// rather than a new caller-visible distinction outside pkg/scriptout's
// closed six-value taxonomy:
//   - path must be a worktree git itself currently knows about (the
//     original check) — compared via resolvePathForCompare, not raw
//     string equality, so a caller-supplied path that differs from git's
//     own porcelain-reported path only by an unresolved symlink component
//     still matches [bug: this bead, review finding 22].
//   - path must live under THIS backend's own worktree namespace
//     (backendWorktreeRoot) — i.e. a worktree WorktreeAdd itself created —
//     never some other git-known worktree this backend had no hand in
//     creating (a drain/workforest worktree, or anything else living
//     directly under `.worktrees/`) [bug: this bead, review finding 22].
//     git's own refusals (dirty, locked, is the main worktree) are useful
//     but incidental protection, not a scoping guarantee, since none of
//     them are about WHO created a worktree.
//
// The `--` terminator before path guards the same class of bug as
// WorktreeAdd's own [bug: this bead, review finding 22].
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
	wantPath := resolvePathForCompare(path)
	known := false
	for _, w := range worktrees {
		if resolvePathForCompare(w.Path) == wantPath {
			known = true
			break
		}
	}
	if !known {
		return scriptout.WrapError(scriptout.ErrNotFound, fmt.Sprintf("worktree %s not found", path))
	}
	nsRoot := resolvePathForCompare(backendWorktreeRoot(repoRoot))
	if wantPath != nsRoot && !strings.HasPrefix(wantPath, nsRoot+string(filepath.Separator)) {
		return scriptout.WrapError(scriptout.ErrNotFound, fmt.Sprintf("worktree %s not found: not created by this backend", path))
	}
	if _, err := p.runner.Run(ctx, repoRoot, "worktree", "remove", "--", path); err != nil {
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
// (blocks of "worktree <path>" / "HEAD <sha>" / then one of "branch
// refs/heads/<name>", the bare word "detached", or the bare word "bare",
// optionally followed by "locked [<reason>]" and/or "prunable [<reason>]"
// — separated by blank lines) into []schema.WorktreeInfo.
//
// Every status line git can emit here (detached, bare, locked, prunable)
// is matched by its own explicit case below, even where the resulting
// behavior is "do nothing further to cur" — making that a deliberate,
// documented no-op rather than an accidental one that happened to fall
// through no matching case [bug: this bead, review finding 34]. In
// particular: no entry is ever dropped by any of these lines — Path (and
// Branch/Ref, when a HEAD/branch line already set them) survive from
// earlier lines in the same block regardless. What IS genuinely missing
// is a way to SURFACE bare/locked/prunable status on the returned
// schema.WorktreeInfo at all: that type has no field for any of the three,
// and neither the interface doc (`worktree_list` → "every local worktree
// this backend manages") nor any existing caller (cmd/pg-connector/scm.go's
// humanizeWorktreeList prints only path/branch/ref) asks for one — adding
// one is a real wire-schema decision with no doc/caller authority behind
// it, so this fix stops at making today's fall-through deliberate and
// tested rather than guessing at a schema change. A detached-HEAD entry
// keeps its checked-out commit sha as Ref (via the HEAD-line case below) —
// a deliberate, honest distinction from a real branch checkout, not an
// omission. A bare entry has no HEAD line at all, so Branch and Ref both
// stay empty — there being no checked-out ref for a bare repository to
// report — which is likewise deliberate, not an omission.
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
		case line == "detached":
			// Confirms the detached-HEAD case the HEAD-line case above
			// already handled (no branch line follows a "detached" line,
			// ever) — deliberate no-op, see doc comment.
		case line == "bare":
			// The bare-repository pseudo-worktree entry — deliberate
			// no-op, see doc comment.
		case strings.HasPrefix(line, "locked"):
			// A locked worktree (optionally "locked <reason>") —
			// deliberate no-op, see doc comment.
		case strings.HasPrefix(line, "prunable"):
			// A worktree git considers prunable (optionally "prunable
			// <reason>") — deliberate no-op, see doc comment.
		case line == "":
			flush()
		}
	}
	flush()
	return infos
}

// BranchDetect resolves cwd to its repo and currently checked-out branch
// (interfaces.md's scm op catalog). Unlike WorktreeAdd/Remove/List, it receives cwd as an
// explicit wire argument rather than resolving it from this process's own
// working directory.
//
// Repo is the repo root's basename (via repoRootFor, so it is the same
// value regardless of which one of the repo's own worktrees cwd happens
// to sit inside) — purely local git state, with no assumption of any
// particular remote/hosting convention: scm has no remote-sync concept at
// all (GOAL-MIN-1), so deriving Repo from a remote's URL (as pg-pr's
// own GitHub-flavored `branch detect` does) would reintroduce exactly the
// remote-awareness this capability is designed without. Base(root) is
// correct for bare and --separate-git-dir repos too, now that repoRootFor
// itself resolves root correctly for both [bug: this bead, review finding
// 34 — checked, no further change needed here beyond the repoRootFor fix:
// root is the bare directory itself for a bare repo, and the real
// working-tree top-level for a --separate-git-dir main worktree, so
// Base(root) already names the right thing in both cases].
//
// Detached HEAD deliberately mirrors WorktreeAdd/WorktreeList's own
// established treatment of the identical underlying git condition (`git
// branch --show-current` prints nothing, with exit 0, on detached HEAD):
// an empty Branch is the well-formed, honest answer — "there is no
// current branch" — not an error, exactly as WorktreeInfo.Branch is left
// empty for a detached worktree rather than failing the call over it
// [bug: this bead, review finding 34]. Unlike WorktreeInfo,
// schema.BranchInfo carries no secondary Ref-style field to record WHICH
// commit/ref is checked out when Branch is empty, and neither the
// interface doc (`branch_detect`: `{cwd}` → `{repo, branch}`) nor any
// existing caller (cmd/pg-connector/scm.go's humanizeBranchInfo prints
// only repo/branch) asks for one — widening the wire shape to add one is a
// real design decision with no doc/caller authority behind it, so this
// fix stops at making the existing behavior deliberate (documented,
// tested below) rather than guessing at a schema change.
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
