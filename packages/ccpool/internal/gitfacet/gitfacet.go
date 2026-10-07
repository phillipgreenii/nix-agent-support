// Package gitfacet resolves the git-dependent session-location facets reported
// by ccpool list --json (repo root, worktree, branch). Every facet fails SOFT:
// outside a git work tree (or when git is absent / errors) the corresponding
// field is nil, never an error, so a single bad cwd can't fail the whole list.
package gitfacet

import (
	"context"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/x/gitclient"
)

// Facets are the git-dependent location facets for a cwd. A nil field means the
// facet is unavailable (cwd is not inside a git work tree, or git failed).
type Facets struct {
	// RepoRoot is the MAIN repository root: for a normal checkout it equals
	// Worktree; for a linked worktree it is the directory containing the shared
	// .git (parent of --git-common-dir), NOT the linked worktree root.
	RepoRoot *string
	// Worktree is the working-tree root for cwd (git rev-parse --show-toplevel).
	Worktree *string
	// Branch is the checked-out branch; nil when detached (rev-parse reports
	// "HEAD") or unavailable.
	Branch *string
}

// resolveTimeout bounds ALL git calls Resolve makes for one cwd. ccpool list
// resolves one cwd per session row, so an unbounded git call on a loaded host
// (or a hung filesystem) stalled the whole listing until the caller's own
// deadline SIGKILLed it (pg2-zzf54: ccpool-probe saw exit -1 on loaded-host
// runs). On expiry the remaining facets are simply left nil, the same soft-fail
// as any other git failure. A healthy git answers all of them in tens of
// milliseconds; the budget is generous so a merely slow host still resolves.
const resolveTimeout = 3 * time.Second

// Resolve returns the git facets for cwd. It never returns an error: any
// failed sub-query leaves that facet nil. A cwd outside a git work tree (or a
// missing git binary) yields an all-nil Facets. This is the app-side
// soft-fail policy; it is unchanged by the migration to x/gitclient below.
//
// Resolve takes no context because its only caller (ccpool list) has none to
// thread through the gitFn seam it plugs into (cmd/ccpool/list.go); it applies
// its own resolveTimeout deadline instead.
func Resolve(cwd string) Facets {
	return resolveWithin(cwd, resolveTimeout)
}

// resolveWithin is Resolve with an explicit deadline, split out so tests can
// exercise the expiry path deterministically (a non-positive timeout is an
// already-expired deadline).
func resolveWithin(cwd string, timeout time.Duration) Facets {
	var f Facets
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// gitclient.Discover walks up from cwd to the repository toplevel and
	// anchors a Client there -- exactly the "where am I" case this package
	// exists for (bead pg2-svfbb's design, "The client -- gitclient/client.go").
	// It also doubles as the cheap "are we in a repo at all?" probe:
	// ErrNotARepository (cwd outside a git work tree) or a missing git
	// binary both land here and leave every facet nil, matching the old raw
	// git() helper's soft-fail behavior.
	client, err := gitclient.Discover(ctx, cwd)
	if err != nil {
		return f
	}

	top, err := client.Toplevel(ctx)
	if err != nil {
		return f
	}
	f.Worktree = &top

	// Repo root = parent of the shared git common dir. For a normal checkout the
	// common dir is "<root>/.git", so its parent is the worktree root; for a
	// linked worktree it points at the MAIN repo's .git, so its parent is the
	// main repo root (which differs from the linked worktree).
	if commonDir, err := client.CommonDir(ctx); err == nil {
		root := filepath.Dir(commonDir)
		f.RepoRoot = &root
	}

	// Branch: nil on detached HEAD. The pre-migration implementation ran
	// `rev-parse --abbrev-ref HEAD`, which reports the literal string "HEAD"
	// on a detached checkout; gitclient's CurrentBranch (`branch
	// --show-current`) instead returns the typed sentinel
	// gitclient.ErrDetachedHEAD for the same case. Folding EVERY
	// CurrentBranch error (ErrDetachedHEAD included) into "leave Branch nil"
	// is the mapping: it preserves the exact observable behavior callers
	// already depend on without needing to special-case the sentinel.
	if branch, err := client.CurrentBranch(ctx); err == nil {
		f.Branch = &branch
	}

	return f
}
